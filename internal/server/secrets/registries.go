package secrets

import (
	"context"
	"fmt"

	"github.com/podium-ade/podium/internal/server/store"
	"github.com/podium-ade/podium/pkg/spec"
)

// RegistryCredential is one registry login with its password attached. Password is
// plaintext: it goes straight into an Assign and is never stored or logged.
type RegistryCredential struct {
	Host     string
	Username string
	Password []byte
}

// SetRegistry stores the login for one registry host, the password encrypted under the
// master key with the host as additional data. The plaintext is zeroed before it returns.
func (s *Service) SetRegistry(ctx context.Context, actor, host, username string, password []byte) (store.Registry, error) {
	defer Zero(password)
	if !s.Enabled() {
		return store.Registry{}, ErrNoKey
	}
	host, err := spec.ValidateRegistryHost(host)
	if err != nil {
		return store.Registry{}, fmt.Errorf("%w: %w", ErrInvalidSecret, err)
	}
	if username == "" {
		return store.Registry{}, fmt.Errorf("%w: registry %s has an empty username", ErrInvalidSecret, host)
	}
	if len(password) == 0 {
		return store.Registry{}, fmt.Errorf("%w: registry %s has an empty password", ErrInvalidSecret, host)
	}
	ciphertext, nonce, err := s.key.Encrypt(host, password)
	if err != nil {
		return store.Registry{}, err
	}
	row, err := s.store.UpsertRegistry(ctx, store.Registry{
		Host: host, Username: username, Ciphertext: ciphertext, Nonce: nonce, KeyID: s.key.ID(), CreatedBy: actor,
	})
	if err != nil {
		return store.Registry{}, err
	}
	s.audit(ctx, actor, store.ActionRegistrySet, host, map[string]any{"username": username, "key_id": row.KeyID})
	s.logger.InfoContext(ctx, "registry set", "host", host, "username", username, "actor", actor)
	return row, nil
}

// ListRegistries returns metadata for every registry credential, ciphertext stripped.
func (s *Service) ListRegistries(ctx context.Context) ([]store.Registry, error) {
	rows, err := s.store.ListRegistries(ctx)
	if err != nil {
		return nil, err
	}
	for i := range rows {
		rows[i].Ciphertext = nil
		rows[i].Nonce = nil
	}
	return rows, nil
}

// DeleteRegistry removes a registry credential. Tasks already assigned keep the copy in
// their Assign; the next pull from that host is anonymous.
func (s *Service) DeleteRegistry(ctx context.Context, actor, host string) error {
	host = spec.NormalizeRegistryHost(host)
	if err := s.store.DeleteRegistry(ctx, host); err != nil {
		return err
	}
	s.audit(ctx, actor, store.ActionRegistryDelete, host, nil)
	s.logger.InfoContext(ctx, "registry deleted", "host", host, "actor", actor)
	return nil
}

// ResolveRegistries returns the credentials for the registries the given images are pulled
// from, and nothing for the rest: a registry Podium holds no login for is pulled anonymously,
// and a task never learns about a registry it does not use. A server without a master key
// holds no credentials, so it resolves none rather than failing every task.
func (s *Service) ResolveRegistries(ctx context.Context, taskID string, images []string) ([]RegistryCredential, error) {
	if !s.Enabled() || len(images) == 0 {
		return nil, nil
	}
	hosts := make([]string, 0, len(images))
	for _, image := range images {
		host, err := spec.RegistryHost(image)
		if err != nil {
			// A malformed reference is the executor's to report; it is not a credential problem.
			continue
		}
		hosts = append(hosts, host)
	}
	hosts = sortedUnique(hosts)
	rows, err := s.store.GetRegistries(ctx, hosts)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}

	out := make([]RegistryCredential, 0, len(rows))
	for _, host := range hosts {
		row, ok := rows[host]
		if !ok {
			continue
		}
		password, err := s.key.Decrypt(row.Host, row.Ciphertext, row.Nonce)
		if err != nil {
			return nil, fmt.Errorf("registry %s (stored under key %s, server holds %s): %w",
				host, row.KeyID, s.key.ID(), err)
		}
		out = append(out, RegistryCredential{Host: host, Username: row.Username, Password: password})
	}
	used := make([]string, 0, len(out))
	for _, c := range out {
		used = append(used, c.Host)
	}
	s.audit(ctx, "scheduler", store.ActionRegistryResolve, taskID, map[string]any{"hosts": used})
	return out, nil
}
