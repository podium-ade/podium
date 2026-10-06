package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/podium-ade/podium/internal/server/store/db"
)

const (
	// SecretScopeGlobal is a secret the conductor attaches. It has no owner.
	SecretScopeGlobal = "global"
	// SecretScopePersonal is a secret one signed-in login wrote. Owner is that login.
	SecretScopePersonal = "personal"
	// ReservedSecretPrefix is the conductor's own namespace. A personal secret cannot take it.
	ReservedSecretPrefix = "podium.agent."
)

// ErrSecretNameTaken is returned when a name already exists in the other scope. Global and
// personal cover different jobs, so the same name is not allowed in both.
var ErrSecretNameTaken = errors.New("secret name exists in the other scope")

// ErrReservedSecretName is returned when a personal secret tries to use podium.agent.*.
var ErrReservedSecretName = errors.New("reserved secret name")

// Secret is one row of the secrets table. Ciphertext and Nonce are AES-256-GCM output;
// the plaintext never reaches this package, and this type is therefore safe to log —
// though there is no reason to.
type Secret struct {
	Scope      string
	Owner      string
	Name       string
	Ciphertext []byte
	Nonce      []byte
	Version    int32
	KeyID      string
	CreatedBy  string
	UpdatedAt  time.Time
}

// UpsertSecret writes a secret and returns the stored row. A name that already exists in
// the same scope and owner keeps its created_by and has its version incremented. An empty
// scope is global, which is how a caller from before scopes still stores one.
func (s *Store) UpsertSecret(ctx context.Context, in Secret) (Secret, error) {
	in, err := normalizeSecret(in)
	if err != nil {
		return Secret{}, err
	}
	if len(in.Ciphertext) == 0 || len(in.Nonce) == 0 {
		return Secret{}, fmt.Errorf("upsert secret %s: ciphertext and nonce are required", in.Name)
	}
	taken, err := s.q.SecretNameInOtherScope(ctx, db.SecretNameInOtherScopeParams{
		Name: in.Name, Scope: in.Scope,
	})
	if err != nil {
		return Secret{}, fmt.Errorf("check secret name %s: %w", in.Name, err)
	}
	if taken {
		return Secret{}, fmt.Errorf("secret %s: %w", in.Name, ErrSecretNameTaken)
	}
	row, err := s.q.UpsertSecret(ctx, db.UpsertSecretParams{
		Scope:      in.Scope,
		Owner:      in.Owner,
		Name:       in.Name,
		Ciphertext: in.Ciphertext,
		Nonce:      in.Nonce,
		KeyID:      in.KeyID,
		CreatedBy:  in.CreatedBy,
	})
	if err != nil {
		return Secret{}, fmt.Errorf("upsert secret %s: %w", in.Name, err)
	}
	return secretFromRow(row), nil
}

// GetSecret returns the global secret of this name, or ErrNotFound. A personal row of the
// same name is not this row: names are disjoint, and an empty owner means global.
func (s *Store) GetSecret(ctx context.Context, name string) (Secret, error) {
	return s.GetScopedSecret(ctx, SecretScopeGlobal, "", name)
}

// GetScopedSecret returns one secret, or ErrNotFound.
func (s *Store) GetScopedSecret(ctx context.Context, scope, owner, name string) (Secret, error) {
	if scope == "" {
		scope = SecretScopeGlobal
	}
	if scope == SecretScopeGlobal {
		owner = ""
	}
	row, err := s.q.GetSecret(ctx, db.GetSecretParams{Scope: scope, Owner: owner, Name: name})
	if noRows(err) {
		return Secret{}, fmt.Errorf("secret %s: %w", name, ErrNotFound)
	}
	if err != nil {
		return Secret{}, fmt.Errorf("select secret %s: %w", name, err)
	}
	return secretFromRow(row), nil
}

// SecretKey identifies one stored secret. An empty Scope is global, and a global Owner is empty.
type SecretKey struct {
	Scope string
	Owner string
	Name  string
}

// GetScopedSecrets returns the named secrets. Missing keys are absent from the map.
func (s *Store) GetScopedSecrets(ctx context.Context, keys []SecretKey) (map[SecretKey]Secret, error) {
	out := make(map[SecretKey]Secret, len(keys))
	if len(keys) == 0 {
		return out, nil
	}
	for _, key := range keys {
		row, err := s.GetScopedSecret(ctx, key.Scope, key.Owner, key.Name)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out[SecretKey{Scope: row.Scope, Owner: row.Owner, Name: row.Name}] = row
	}
	return out, nil
}

// GetSecrets returns the named global secrets, keyed by name. Names with no global row are
// absent. A personal row is never returned here.
func (s *Store) GetSecrets(ctx context.Context, names []string) (map[string]Secret, error) {
	if len(names) == 0 {
		return map[string]Secret{}, nil
	}
	rows, err := s.q.GetSecrets(ctx, names)
	if err != nil {
		return nil, fmt.Errorf("select secrets: %w", err)
	}
	out := make(map[string]Secret, len(rows))
	for _, row := range rows {
		out[row.Name] = secretFromRow(row)
	}
	return out, nil
}

// ListSecrets returns every secret. The rows carry ciphertext; an API that returns them
// to a user must project it away and must not use this list — it includes every owner.
func (s *Store) ListSecrets(ctx context.Context) ([]Secret, error) {
	rows, err := s.q.ListSecrets(ctx)
	if err != nil {
		return nil, fmt.Errorf("list secrets: %w", err)
	}
	out := make([]Secret, 0, len(rows))
	for _, row := range rows {
		out = append(out, secretFromRow(row))
	}
	return out, nil
}

// ListVisibleSecrets returns every global secret plus one login's personal secrets.
// An empty owner returns globals only.
func (s *Store) ListVisibleSecrets(ctx context.Context, owner string) ([]Secret, error) {
	rows, err := s.q.ListVisibleSecrets(ctx, owner)
	if err != nil {
		return nil, fmt.Errorf("list visible secrets: %w", err)
	}
	out := make([]Secret, 0, len(rows))
	for _, row := range rows {
		out = append(out, secretFromRow(row))
	}
	return out, nil
}

// DeleteSecret removes the global secret of this name, or returns ErrNotFound.
func (s *Store) DeleteSecret(ctx context.Context, name string) error {
	return s.DeleteScopedSecret(ctx, SecretScopeGlobal, "", name)
}

// DeleteScopedSecret removes one secret, or returns ErrNotFound.
func (s *Store) DeleteScopedSecret(ctx context.Context, scope, owner, name string) error {
	if scope == "" {
		scope = SecretScopeGlobal
	}
	if scope == SecretScopeGlobal {
		owner = ""
	}
	n, err := s.q.DeleteSecret(ctx, db.DeleteSecretParams{Scope: scope, Owner: owner, Name: name})
	if err != nil {
		return fmt.Errorf("delete secret %s: %w", name, err)
	}
	if n == 0 {
		return fmt.Errorf("secret %s: %w", name, ErrNotFound)
	}
	return nil
}

// RotateSecrets re-encrypts every secret in one transaction. reencrypt is called with each
// stored row and returns the replacement ciphertext, nonce and key id; a single error
// rolls the whole rotation back, so the table is never left half under one key and half
// under another. Every row is locked for the duration, so a concurrent SetSecret waits
// rather than being silently re-encrypted under the key it did not use.
func (s *Store) RotateSecrets(
	ctx context.Context,
	reencrypt func(Secret) (ciphertext, nonce []byte, keyID string, err error),
) (int, error) {
	rotated := 0
	err := s.inTx(ctx, func(q *db.Queries) error {
		rows, err := q.ListSecretsForUpdate(ctx)
		if err != nil {
			return fmt.Errorf("lock secrets for rotation: %w", err)
		}
		for _, row := range rows {
			ciphertext, nonce, keyID, err := reencrypt(secretFromRow(row))
			if err != nil {
				return fmt.Errorf("re-encrypt secret %s: %w", row.Name, err)
			}
			n, err := q.ReEncryptSecret(ctx, db.ReEncryptSecretParams{
				Scope: row.Scope, Owner: row.Owner, Name: row.Name,
				Ciphertext: ciphertext, Nonce: nonce, KeyID: keyID,
			})
			if err != nil {
				return fmt.Errorf("store rotated secret %s: %w", row.Name, err)
			}
			rotated += int(n)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return rotated, nil
}

func normalizeSecret(in Secret) (Secret, error) {
	if in.Name == "" {
		return Secret{}, fmt.Errorf("upsert secret: name is required")
	}
	if in.Scope == "" {
		in.Scope = SecretScopeGlobal
	}
	switch in.Scope {
	case SecretScopeGlobal:
		if in.Owner != "" {
			return Secret{}, fmt.Errorf("upsert secret %s: a global secret has no owner", in.Name)
		}
	case SecretScopePersonal:
		if in.Owner == "" {
			return Secret{}, fmt.Errorf("upsert secret %s: a personal secret needs an owner", in.Name)
		}
		if strings.HasPrefix(in.Name, ReservedSecretPrefix) || in.Name == "podium.agent" {
			return Secret{}, fmt.Errorf("upsert secret %s: %w", in.Name, ErrReservedSecretName)
		}
	default:
		return Secret{}, fmt.Errorf("upsert secret %s: unknown scope %q", in.Name, in.Scope)
	}
	return in, nil
}

func secretFromRow(row db.Secret) Secret {
	return Secret{
		Scope:      row.Scope,
		Owner:      row.Owner,
		Name:       row.Name,
		Ciphertext: row.Ciphertext,
		Nonce:      row.Nonce,
		Version:    row.Version,
		KeyID:      row.KeyID,
		CreatedBy:  row.CreatedBy,
		UpdatedAt:  row.UpdatedAt.UTC(),
	}
}
