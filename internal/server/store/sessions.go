package store

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"time"

	"github.com/podium-ade/podium/internal/ids"
	"github.com/podium-ade/podium/internal/server/store/db"
)

// DefaultSessionTTL is how long a Google sign-in cookie lasts.
const DefaultSessionTTL = 7 * 24 * time.Hour

const sessionTokenBytes = 32

// CreateSession mints a session for login. The plaintext is returned once and never stored;
// only its SHA-256 reaches Postgres.
func (s *Store) CreateSession(ctx context.Context, login string, ttl time.Duration) (string, error) {
	if login == "" {
		return "", fmt.Errorf("create session: login is required")
	}
	if ttl <= 0 {
		ttl = DefaultSessionTTL
	}
	raw := make([]byte, sessionTokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("create session: %w", err)
	}
	plaintext := base64.RawURLEncoding.EncodeToString(raw)
	id := ids.New("sess")
	if _, err := s.q.CreateSession(ctx, db.CreateSessionParams{
		ID:        id,
		TokenHash: HashToken(plaintext),
		Login:     login,
		ExpiresAt: time.Now().UTC().Add(ttl),
	}); err != nil {
		return "", fmt.Errorf("insert session for %s: %w", login, err)
	}
	return plaintext, nil
}

// GetSession looks up a live session by plaintext token. Expired and unknown tokens are
// ErrNotFound.
func (s *Store) GetSession(ctx context.Context, plaintext string) (Session, error) {
	if plaintext == "" {
		return Session{}, fmt.Errorf("session: %w", ErrNotFound)
	}
	row, err := s.q.GetSessionByTokenHash(ctx, HashToken(plaintext))
	if noRows(err) {
		return Session{}, fmt.Errorf("session: %w", ErrNotFound)
	}
	if err != nil {
		return Session{}, fmt.Errorf("select session: %w", err)
	}
	return Session{
		ID:        row.ID,
		Login:     row.Login,
		ExpiresAt: row.ExpiresAt.UTC(),
		CreatedAt: row.CreatedAt.UTC(),
	}, nil
}

// DeleteSession forgets a plaintext token. Missing is not an error: logout is idempotent.
func (s *Store) DeleteSession(ctx context.Context, plaintext string) error {
	if plaintext == "" {
		return nil
	}
	if err := s.q.DeleteSessionByTokenHash(ctx, HashToken(plaintext)); err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}
