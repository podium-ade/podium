package secrets

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"

	"github.com/alvaroibarguen/podium/internal/server/store"
	"github.com/alvaroibarguen/podium/pkg/spec"
)

// ErrMissing is what Resolve returns when a task names a secret that does not exist. It is
// not retryable: another node would fail identically, and the task is failed before it is
// ever assigned.
var ErrMissing = errors.New("secrets: missing secret")

// ErrInvalidSecret marks a caller's mistake: a name that is not a valid secret name, or an
// empty value. It exists so the API layer can answer InvalidArgument without string
// matching.
var ErrInvalidSecret = errors.New("secrets: invalid secret")

// Provider is where a value comes from. Only the builtin, Postgres-backed provider exists;
// Vault, 1Password and cloud KMS would be further implementations of this interface and are
// deliberately out of scope.
type Provider interface {
	// Resolve returns the plaintext of one secret, or an error wrapping ErrMissing when
	// there is no such name.
	Resolve(ctx context.Context, name string) ([]byte, error)
}

// Resolved is one SecretRef with its value attached. Value is the only plaintext this
// package hands out; it goes straight into an Assign and is never stored or logged.
type Resolved struct {
	Name   string
	Target string
	Key    string
	Value  []byte
}

// Service is the secret store: encryption, the CRUD an operator drives, and the resolution
// the scheduler calls immediately before it assigns a task.
//
// A Service with a nil key is a server started without PODIUM_MASTER_KEY_FILE. Every
// operation on it fails with ErrNoKey rather than silently storing plaintext, so a
// misconfigured server is loudly useless rather than quietly unsafe.
type Service struct {
	store  *store.Store
	key    *Key
	logger *slog.Logger
}

// New returns the secret service. key may be nil, in which case secrets are disabled.
func New(st *store.Store, key *Key, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{store: st, key: key, logger: logger}
}

// Enabled reports whether a master key was configured.
func (s *Service) Enabled() bool { return s != nil && s.key != nil }

// KeyID is the configured master key's identifier, or "" when there is none.
func (s *Service) KeyID() string {
	if !s.Enabled() {
		return ""
	}
	return s.key.ID()
}

// Set stores a value under name, encrypted under the master key. The plaintext is zeroed
// before Set returns, so the caller must not reuse the slice.
func (s *Service) Set(ctx context.Context, actor, name string, value []byte) (store.Secret, error) {
	defer Zero(value)
	if !s.Enabled() {
		return store.Secret{}, ErrNoKey
	}
	if !spec.SecretNameRE.MatchString(name) {
		return store.Secret{}, fmt.Errorf("%w: %q is not a valid secret name (%s)",
			ErrInvalidSecret, name, spec.SecretNameRE.String())
	}
	if len(value) == 0 {
		return store.Secret{}, fmt.Errorf("%w: %s has an empty value", ErrInvalidSecret, name)
	}
	ciphertext, nonce, err := s.key.Encrypt(name, value)
	if err != nil {
		return store.Secret{}, err
	}
	row, err := s.store.UpsertSecret(ctx, store.Secret{
		Name: name, Ciphertext: ciphertext, Nonce: nonce, KeyID: s.key.ID(), CreatedBy: actor,
	})
	if err != nil {
		return store.Secret{}, err
	}
	s.audit(ctx, actor, store.ActionSecretSet, name, map[string]any{
		"version": row.Version, "key_id": row.KeyID, "bytes": len(value),
	})
	s.logger.InfoContext(ctx, "secret set", "name", name, "version", row.Version, "actor", actor)
	return row, nil
}

// List returns metadata for every secret. Ciphertext is stripped: nothing outside this
// package has any use for it.
func (s *Service) List(ctx context.Context) ([]store.Secret, error) {
	rows, err := s.store.ListSecrets(ctx)
	if err != nil {
		return nil, err
	}
	for i := range rows {
		rows[i].Ciphertext = nil
		rows[i].Nonce = nil
	}
	return rows, nil
}

// Delete removes a secret. Tasks already assigned keep the copy in their Assign; the next
// task that references the name fails to resolve.
func (s *Service) Delete(ctx context.Context, actor, name string) error {
	if err := s.store.DeleteSecret(ctx, name); err != nil {
		return err
	}
	s.audit(ctx, actor, store.ActionSecretDelete, name, nil)
	s.logger.InfoContext(ctx, "secret deleted", "name", name, "actor", actor)
	return nil
}

// Resolve turns a task's refs into the plaintext an Assign carries. A single missing name
// fails the whole task: a task that silently runs without half its credentials is worse
// than one that does not run.
//
// It writes one secret.resolve audit row naming the task and the secret names — never the
// values.
func (s *Service) Resolve(ctx context.Context, taskID string, refs []spec.SecretRef) ([]Resolved, error) {
	if len(refs) == 0 {
		return nil, nil
	}
	if !s.Enabled() {
		return nil, fmt.Errorf("%w: task %s references %d secret(s)", ErrNoKey, taskID, len(refs))
	}

	names := make([]string, 0, len(refs))
	for _, ref := range refs {
		names = append(names, ref.Name)
	}
	rows, err := s.store.GetSecrets(ctx, names)
	if err != nil {
		return nil, err
	}

	out := make([]Resolved, 0, len(refs))
	for _, ref := range refs {
		row, ok := rows[ref.Name]
		if !ok {
			return nil, fmt.Errorf("%w %q", ErrMissing, ref.Name)
		}
		value, err := s.key.Decrypt(row.Name, row.Ciphertext, row.Nonce)
		if err != nil {
			return nil, fmt.Errorf("secret %s (stored under key %s, server holds %s): %w",
				ref.Name, row.KeyID, s.key.ID(), err)
		}
		out = append(out, Resolved{Name: ref.Name, Target: ref.Target, Key: ref.Key, Value: value})
	}

	s.audit(ctx, "scheduler", store.ActionSecretResolve, taskID, map[string]any{"names": sortedUnique(names)})
	return out, nil
}

// CheckRefs reports whether every name a spec references exists, without decrypting
// anything. It is the admission check: a task naming a secret that is not there can be
// refused at `podium run` rather than sitting queued until some node happens to connect,
// which is what used to happen — the scheduler resolved at dispatch, and with an empty
// cluster there is no dispatch.
//
// It deliberately does not read a value. Admission answers "could this ever run?"; the
// value still comes out of the database once, at assignment, and lives in server memory for
// as short a time as it can.
func (s *Service) CheckRefs(ctx context.Context, refs []spec.SecretRef) error {
	if len(refs) == 0 {
		return nil
	}
	if !s.Enabled() {
		return fmt.Errorf("%w: this task references %d secret(s)", ErrNoKey, len(refs))
	}
	names := make([]string, 0, len(refs))
	for _, ref := range refs {
		names = append(names, ref.Name)
	}
	rows, err := s.store.GetSecrets(ctx, names)
	if err != nil {
		return err
	}
	var missing []string
	for _, name := range sortedUnique(names) {
		if _, ok := rows[name]; !ok {
			missing = append(missing, strconv.Quote(name))
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("%w %s", ErrMissing, strings.Join(missing, ", "))
	}
	return nil
}

// Rotate re-encrypts every stored secret from oldKey to newKey in one transaction. It is
// the offline `podium-server rotate-master-key` path: the server is not running, or is
// still running under the old key and will be restarted with the new one.
func Rotate(ctx context.Context, st *store.Store, oldKey, newKey *Key) (int, error) {
	if oldKey == nil || newKey == nil {
		return 0, ErrNoKey
	}
	if oldKey.Equal(newKey) {
		return 0, errors.New("secrets: the new master key is the old one; rotation would be a no-op")
	}
	return st.RotateSecrets(ctx, func(row store.Secret) ([]byte, []byte, string, error) {
		value, err := oldKey.Decrypt(row.Name, row.Ciphertext, row.Nonce)
		if err != nil {
			return nil, nil, "", err
		}
		defer Zero(value)
		ciphertext, nonce, err := newKey.Encrypt(row.Name, value)
		if err != nil {
			return nil, nil, "", err
		}
		return ciphertext, nonce, newKey.ID(), nil
	})
}

// audit records an action, logging rather than failing when the write does not land: an
// audit row that cannot be written must not stop a task from running.
func (s *Service) audit(ctx context.Context, actor, action, subject string, details map[string]any) {
	if err := s.store.Audit(ctx, actor, action, subject, details); err != nil {
		s.logger.WarnContext(ctx, "audit write failed", "action", action, "subject", subject, "error", err)
	}
}

func sortedUnique(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, v := range in {
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

// builtin is the Postgres-backed Provider. It is the only implementation; the interface
// exists so an external provider can be added without changing Resolve's callers.
type builtin struct{ svc *Service }

// Builtin returns the store-backed Provider for this service.
func (s *Service) Builtin() Provider { return builtin{s} }

func (b builtin) Resolve(ctx context.Context, name string) ([]byte, error) {
	if !b.svc.Enabled() {
		return nil, ErrNoKey
	}
	row, err := b.svc.store.GetSecret(ctx, name)
	if errors.Is(err, store.ErrNotFound) {
		return nil, fmt.Errorf("%w %q", ErrMissing, name)
	}
	if err != nil {
		return nil, err
	}
	return b.svc.key.Decrypt(row.Name, row.Ciphertext, row.Nonce)
}
