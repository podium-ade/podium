package store

import (
	"context"
	"fmt"
	"time"

	"github.com/alvaroibarguen/podium/internal/server/store/db"
)

// Secret is one row of the secrets table. Ciphertext and Nonce are AES-256-GCM output;
// the plaintext never reaches this package, and this type is therefore safe to log —
// though there is no reason to.
type Secret struct {
	Name       string
	Ciphertext []byte
	Nonce      []byte
	Version    int32
	KeyID      string
	CreatedBy  string
	UpdatedAt  time.Time
}

// UpsertSecret writes a secret and returns the stored row. A name that already exists
// keeps its created_by and has its version incremented, so the row records who first
// introduced the name and how many times its value has moved.
func (s *Store) UpsertSecret(ctx context.Context, in Secret) (Secret, error) {
	if in.Name == "" {
		return Secret{}, fmt.Errorf("upsert secret: name is required")
	}
	if len(in.Ciphertext) == 0 || len(in.Nonce) == 0 {
		return Secret{}, fmt.Errorf("upsert secret %s: ciphertext and nonce are required", in.Name)
	}
	row, err := s.q.UpsertSecret(ctx, db.UpsertSecretParams{
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

// GetSecret returns one secret, or ErrNotFound.
func (s *Store) GetSecret(ctx context.Context, name string) (Secret, error) {
	row, err := s.q.GetSecret(ctx, name)
	if noRows(err) {
		return Secret{}, fmt.Errorf("secret %s: %w", name, ErrNotFound)
	}
	if err != nil {
		return Secret{}, fmt.Errorf("select secret %s: %w", name, err)
	}
	return secretFromRow(row), nil
}

// GetSecrets returns the named secrets, keyed by name. Names with no row are simply
// absent from the map; resolving is the caller's job, because only the caller knows
// whether a missing name is fatal.
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

// ListSecrets returns every secret, by name. The rows carry ciphertext; an API that
// returns them to a user must project it away.
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

// DeleteSecret removes one secret, or returns ErrNotFound.
func (s *Store) DeleteSecret(ctx context.Context, name string) error {
	n, err := s.q.DeleteSecret(ctx, name)
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
				Name: row.Name, Ciphertext: ciphertext, Nonce: nonce, KeyID: keyID,
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

func secretFromRow(row db.Secret) Secret {
	return Secret{
		Name:       row.Name,
		Ciphertext: row.Ciphertext,
		Nonce:      row.Nonce,
		Version:    row.Version,
		KeyID:      row.KeyID,
		CreatedBy:  row.CreatedBy,
		UpdatedAt:  row.UpdatedAt.UTC(),
	}
}
