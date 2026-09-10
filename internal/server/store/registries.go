package store

import (
	"context"
	"fmt"
	"time"

	"github.com/podium-ade/podium/internal/server/store/db"
)

// Registry is one row of the registries table: the login for one registry host, with the
// password as AES-256-GCM output. The plaintext never reaches this package.
type Registry struct {
	Host       string
	Username   string
	Ciphertext []byte
	Nonce      []byte
	KeyID      string
	CreatedBy  string
	UpdatedAt  time.Time
}

// UpsertRegistry writes a registry credential and returns the stored row. A host that
// already exists keeps its created_by and takes the new login.
func (s *Store) UpsertRegistry(ctx context.Context, in Registry) (Registry, error) {
	if in.Host == "" || in.Username == "" {
		return Registry{}, fmt.Errorf("upsert registry: host and username are required")
	}
	if len(in.Ciphertext) == 0 || len(in.Nonce) == 0 {
		return Registry{}, fmt.Errorf("upsert registry %s: ciphertext and nonce are required", in.Host)
	}
	row, err := s.q.UpsertRegistry(ctx, db.UpsertRegistryParams{
		Host:       in.Host,
		Username:   in.Username,
		Ciphertext: in.Ciphertext,
		Nonce:      in.Nonce,
		KeyID:      in.KeyID,
		CreatedBy:  in.CreatedBy,
	})
	if err != nil {
		return Registry{}, fmt.Errorf("upsert registry %s: %w", in.Host, err)
	}
	return registryFromRow(row), nil
}

// GetRegistries returns the credentials for the given hosts, keyed by host. A host with no
// row is simply absent: an image from a registry Podium holds no login for is pulled
// anonymously, which is the common case.
func (s *Store) GetRegistries(ctx context.Context, hosts []string) (map[string]Registry, error) {
	if len(hosts) == 0 {
		return map[string]Registry{}, nil
	}
	rows, err := s.q.GetRegistries(ctx, hosts)
	if err != nil {
		return nil, fmt.Errorf("select registries: %w", err)
	}
	out := make(map[string]Registry, len(rows))
	for _, row := range rows {
		out[row.Host] = registryFromRow(row)
	}
	return out, nil
}

// ListRegistries returns every registry credential, by host, ciphertext included.
func (s *Store) ListRegistries(ctx context.Context) ([]Registry, error) {
	rows, err := s.q.ListRegistries(ctx)
	if err != nil {
		return nil, fmt.Errorf("list registries: %w", err)
	}
	out := make([]Registry, 0, len(rows))
	for _, row := range rows {
		out = append(out, registryFromRow(row))
	}
	return out, nil
}

// DeleteRegistry removes one registry credential, or returns ErrNotFound.
func (s *Store) DeleteRegistry(ctx context.Context, host string) error {
	n, err := s.q.DeleteRegistry(ctx, host)
	if err != nil {
		return fmt.Errorf("delete registry %s: %w", host, err)
	}
	if n == 0 {
		return fmt.Errorf("registry %s: %w", host, ErrNotFound)
	}
	return nil
}

// RotateRegistries re-encrypts every registry password in one transaction, the way
// RotateSecrets does for secrets.
func (s *Store) RotateRegistries(
	ctx context.Context,
	reencrypt func(Registry) (ciphertext, nonce []byte, keyID string, err error),
) (int, error) {
	rotated := 0
	err := s.inTx(ctx, func(q *db.Queries) error {
		rows, err := q.ListRegistriesForUpdate(ctx)
		if err != nil {
			return fmt.Errorf("lock registries for rotation: %w", err)
		}
		for _, row := range rows {
			ciphertext, nonce, keyID, err := reencrypt(registryFromRow(row))
			if err != nil {
				return fmt.Errorf("re-encrypt registry %s: %w", row.Host, err)
			}
			n, err := q.ReEncryptRegistry(ctx, db.ReEncryptRegistryParams{
				Host: row.Host, Ciphertext: ciphertext, Nonce: nonce, KeyID: keyID,
			})
			if err != nil {
				return fmt.Errorf("store rotated registry %s: %w", row.Host, err)
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

func registryFromRow(row db.Registry) Registry {
	return Registry{
		Host:       row.Host,
		Username:   row.Username,
		Ciphertext: row.Ciphertext,
		Nonce:      row.Nonce,
		KeyID:      row.KeyID,
		CreatedBy:  row.CreatedBy,
		UpdatedAt:  row.UpdatedAt.UTC(),
	}
}
