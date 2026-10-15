package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/podium-ade/podium/internal/server/store/db"
)

// SandboxProvider is one row of sandbox_providers: a cloud sandbox provider's settings and
// its credential as AES-256-GCM output. The plaintext never reaches this package.
type SandboxProvider struct {
	Name       string
	Settings   map[string]string
	Ciphertext []byte
	Nonce      []byte
	KeyID      string
	CreatedBy  string
	UpdatedAt  time.Time
}

// UpsertSandboxProvider writes a provider and returns the stored row.
func (s *Store) UpsertSandboxProvider(ctx context.Context, in SandboxProvider) (SandboxProvider, error) {
	if in.Name == "" {
		return SandboxProvider{}, fmt.Errorf("upsert sandbox provider: name is required")
	}
	if len(in.Ciphertext) == 0 || len(in.Nonce) == 0 {
		return SandboxProvider{}, fmt.Errorf("upsert sandbox provider %s: ciphertext and nonce are required", in.Name)
	}
	settings, err := json.Marshal(nonNil(in.Settings))
	if err != nil {
		return SandboxProvider{}, err
	}
	row, err := s.q.UpsertSandboxProvider(ctx, db.UpsertSandboxProviderParams{
		Name: in.Name, Settings: settings, Ciphertext: in.Ciphertext, Nonce: in.Nonce, KeyID: in.KeyID, CreatedBy: in.CreatedBy,
	})
	if err != nil {
		return SandboxProvider{}, fmt.Errorf("upsert sandbox provider %s: %w", in.Name, err)
	}
	return sandboxProviderFromRow(row)
}

// GetSandboxProvider returns one provider, ciphertext included, or ErrNotFound.
func (s *Store) GetSandboxProvider(ctx context.Context, name string) (SandboxProvider, error) {
	row, err := s.q.GetSandboxProvider(ctx, name)
	if noRows(err) {
		return SandboxProvider{}, fmt.Errorf("sandbox provider %s: %w", name, ErrNotFound)
	}
	if err != nil {
		return SandboxProvider{}, fmt.Errorf("select sandbox provider %s: %w", name, err)
	}
	return sandboxProviderFromRow(row)
}

// ListSandboxProviders returns every provider, by name, ciphertext included.
func (s *Store) ListSandboxProviders(ctx context.Context) ([]SandboxProvider, error) {
	rows, err := s.q.ListSandboxProviders(ctx)
	if err != nil {
		return nil, fmt.Errorf("list sandbox providers: %w", err)
	}
	out := make([]SandboxProvider, 0, len(rows))
	for _, row := range rows {
		p, err := sandboxProviderFromRow(row)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

// DeleteSandboxProvider removes one provider, or returns ErrNotFound.
func (s *Store) DeleteSandboxProvider(ctx context.Context, name string) error {
	n, err := s.q.DeleteSandboxProvider(ctx, name)
	if err != nil {
		return fmt.Errorf("delete sandbox provider %s: %w", name, err)
	}
	if n == 0 {
		return fmt.Errorf("sandbox provider %s: %w", name, ErrNotFound)
	}
	return nil
}

// RotateSandboxProviders re-encrypts every provider credential in one transaction, the way
// RotateRegistries does for registry passwords.
func (s *Store) RotateSandboxProviders(
	ctx context.Context,
	reencrypt func(SandboxProvider) (ciphertext, nonce []byte, keyID string, err error),
) (int, error) {
	rotated := 0
	err := s.inTx(ctx, func(q *db.Queries) error {
		rows, err := q.ListSandboxProvidersForUpdate(ctx)
		if err != nil {
			return fmt.Errorf("lock sandbox providers for rotation: %w", err)
		}
		for _, row := range rows {
			p, err := sandboxProviderFromRow(row)
			if err != nil {
				return err
			}
			ciphertext, nonce, keyID, err := reencrypt(p)
			if err != nil {
				return fmt.Errorf("re-encrypt sandbox provider %s: %w", row.Name, err)
			}
			n, err := q.ReEncryptSandboxProvider(ctx, db.ReEncryptSandboxProviderParams{
				Name: row.Name, Ciphertext: ciphertext, Nonce: nonce, KeyID: keyID,
			})
			if err != nil {
				return fmt.Errorf("store rotated sandbox provider %s: %w", row.Name, err)
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

func sandboxProviderFromRow(row db.SandboxProvider) (SandboxProvider, error) {
	settings := map[string]string{}
	if len(row.Settings) > 0 {
		if err := json.Unmarshal(row.Settings, &settings); err != nil {
			return SandboxProvider{}, fmt.Errorf("sandbox provider %s: settings: %w", row.Name, err)
		}
	}
	return SandboxProvider{
		Name:       row.Name,
		Settings:   settings,
		Ciphertext: row.Ciphertext,
		Nonce:      row.Nonce,
		KeyID:      row.KeyID,
		CreatedBy:  row.CreatedBy,
		UpdatedAt:  row.UpdatedAt.UTC(),
	}, nil
}

func nonNil(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}
