package secrets

import (
	"context"
	"errors"
	"fmt"
	"regexp"

	"github.com/podium-ade/podium/internal/server/store"
)

var (
	sandboxProviderRE = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	settingKeyRE      = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
)

// sandboxProviderAAD binds a credential to its provider, so a ciphertext copied onto another
// provider's row does not decrypt.
func sandboxProviderAAD(name string) string { return "sandbox-provider:" + name }

// SandboxProviderConfig is a provider's settings with its credential attached. Secret is
// plaintext: it goes straight onto a node's stream and is never stored or logged.
type SandboxProviderConfig struct {
	Settings map[string]string
	Secret   []byte
}

// SetSandboxProvider stores a provider's settings, its concurrency cap and, when secret is not
// empty, its credential, encrypted under the master key. An empty secret keeps the stored
// one; a provider with none stored yet must be given one. The plaintext is zeroed before it
// returns.
func (s *Service) SetSandboxProvider(
	ctx context.Context, actor, name string, settings map[string]string, maxConcurrent int32, secret []byte,
) (store.SandboxProvider, error) {
	defer Zero(secret)
	if !s.Enabled() {
		return store.SandboxProvider{}, ErrNoKey
	}
	if !sandboxProviderRE.MatchString(name) {
		return store.SandboxProvider{}, fmt.Errorf("%w: sandbox provider name %q must match %s", ErrInvalidSecret, name, sandboxProviderRE)
	}
	for k := range settings {
		if !settingKeyRE.MatchString(k) || k == "secret" {
			return store.SandboxProvider{}, fmt.Errorf("%w: setting %q is not a setting name; a credential goes in secret", ErrInvalidSecret, k)
		}
	}
	if maxConcurrent < 0 {
		return store.SandboxProvider{}, fmt.Errorf("%w: max_concurrent must not be negative", ErrInvalidSecret)
	}
	row := store.SandboxProvider{Name: name, Settings: settings, MaxConcurrent: maxConcurrent, CreatedBy: actor}
	if len(secret) > 0 {
		ciphertext, nonce, err := s.key.Encrypt(sandboxProviderAAD(name), secret)
		if err != nil {
			return store.SandboxProvider{}, err
		}
		row.Ciphertext, row.Nonce, row.KeyID = ciphertext, nonce, s.key.ID()
	} else {
		old, err := s.store.GetSandboxProvider(ctx, name)
		if errors.Is(err, store.ErrNotFound) {
			return store.SandboxProvider{}, fmt.Errorf("%w: sandbox provider %s needs its API key or token", ErrInvalidSecret, name)
		}
		if err != nil {
			return store.SandboxProvider{}, err
		}
		row.Ciphertext, row.Nonce, row.KeyID = old.Ciphertext, old.Nonce, old.KeyID
	}
	out, err := s.store.UpsertSandboxProvider(ctx, row)
	if err != nil {
		return store.SandboxProvider{}, err
	}
	s.audit(ctx, actor, store.ActionSandboxProviderSet, name, map[string]any{
		"secret_changed": len(secret) > 0, "key_id": out.KeyID, "max_concurrent": maxConcurrent,
	})
	s.logger.InfoContext(ctx, "sandbox provider set", "provider", name, "actor", actor, "secret_changed", len(secret) > 0)
	out.Ciphertext, out.Nonce = nil, nil
	return out, nil
}

// ListSandboxProviders returns every provider, ciphertext stripped.
func (s *Service) ListSandboxProviders(ctx context.Context) ([]store.SandboxProvider, error) {
	rows, err := s.store.ListSandboxProviders(ctx)
	if err != nil {
		return nil, err
	}
	for i := range rows {
		rows[i].Ciphertext, rows[i].Nonce = nil, nil
	}
	return rows, nil
}

// DeleteSandboxProvider removes a provider. Its nodes stop taking work.
func (s *Service) DeleteSandboxProvider(ctx context.Context, actor, name string) error {
	if err := s.store.DeleteSandboxProvider(ctx, name); err != nil {
		return err
	}
	s.audit(ctx, actor, store.ActionSandboxProviderDelete, name, nil)
	s.logger.InfoContext(ctx, "sandbox provider deleted", "provider", name, "actor", actor)
	return nil
}

// ResolveSandboxProvider decrypts a provider's credential for a node of that provider.
// found is false when no admin has configured it, or the server has no master key.
func (s *Service) ResolveSandboxProvider(ctx context.Context, nodeID, name string) (SandboxProviderConfig, bool, error) {
	if !s.Enabled() {
		return SandboxProviderConfig{}, false, nil
	}
	row, err := s.store.GetSandboxProvider(ctx, name)
	if errors.Is(err, store.ErrNotFound) {
		return SandboxProviderConfig{}, false, nil
	}
	if err != nil {
		return SandboxProviderConfig{}, false, err
	}
	secret, err := s.key.Decrypt(sandboxProviderAAD(name), row.Ciphertext, row.Nonce)
	if err != nil {
		return SandboxProviderConfig{}, false, fmt.Errorf("sandbox provider %s (stored under key %s, server holds %s): %w",
			name, row.KeyID, s.key.ID(), err)
	}
	s.audit(ctx, "nodes", store.ActionSandboxProviderResolve, name, map[string]any{"node_id": nodeID})
	return SandboxProviderConfig{Settings: row.Settings, Secret: secret}, true, nil
}
