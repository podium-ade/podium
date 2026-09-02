package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"

	"github.com/alvaroibarguen/podium/internal/ids"
	"github.com/alvaroibarguen/podium/internal/server/store/db"
)

// DefaultEnrollmentTokenTTL is used when CreateEnrollmentToken is given a non-positive ttl.
const DefaultEnrollmentTokenTTL = time.Hour

// enrollmentTokenBytes is the entropy behind an enrollment token.
const enrollmentTokenBytes = 32

// HashToken is the one-way function guarding both enrollment tokens and node keys: raw
// SHA-256 over the presented string. Only the digest is ever written to Postgres.
func HashToken(plaintext string) []byte {
	sum := sha256.Sum256([]byte(plaintext))
	return sum[:]
}

// CreateEnrollmentToken mints a single-use enrollment token. The plaintext is returned to the
// caller exactly once and never reaches the database — only its SHA-256 does.
func (s *Store) CreateEnrollmentToken(ctx context.Context, labels []string, ttl time.Duration, createdBy string) (string, string, error) {
	if ttl <= 0 {
		ttl = DefaultEnrollmentTokenTTL
	}
	raw := make([]byte, enrollmentTokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", "", fmt.Errorf("generate enrollment token: %w", err)
	}
	plaintext := base64.RawURLEncoding.EncodeToString(raw)

	labelsJSON, err := json.Marshal(nonNilStrings(labels))
	if err != nil {
		return "", "", fmt.Errorf("marshal enrollment token labels: %w", err)
	}
	id := ids.New("etok")
	if _, err := s.q.CreateEnrollmentToken(ctx, db.CreateEnrollmentTokenParams{
		ID:        id,
		TokenHash: HashToken(plaintext),
		Labels:    labelsJSON,
		ExpiresAt: time.Now().UTC().Add(ttl),
		CreatedBy: createdBy,
	}); err != nil {
		return "", "", fmt.Errorf("insert enrollment token: %w", err)
	}
	return plaintext, id, nil
}

// ConsumeEnrollmentToken redeems a token for nodeID and returns the labels it carries. The
// redemption is a single UPDATE, so two racing enrollments cannot both succeed: the loser gets
// ErrTokenUsed. An expired token gets ErrTokenExpired, an unknown one ErrNotFound.
func (s *Store) ConsumeEnrollmentToken(ctx context.Context, plaintext, nodeID string) ([]string, error) {
	hash := HashToken(plaintext)
	row, err := s.q.ConsumeEnrollmentToken(ctx, db.ConsumeEnrollmentTokenParams{
		UsedByNodeID: ptr(nodeID),
		TokenHash:    hash,
	})
	if noRows(err) {
		return nil, s.explainTokenFailure(ctx, hash)
	}
	if err != nil {
		return nil, fmt.Errorf("consume enrollment token: %w", err)
	}
	var labels []string
	if err := json.Unmarshal(row.Labels, &labels); err != nil {
		return nil, fmt.Errorf("decode labels of enrollment token %s: %w", row.ID, err)
	}
	return labels, nil
}

// explainTokenFailure turns "the UPDATE matched nothing" into the specific reason.
func (s *Store) explainTokenFailure(ctx context.Context, hash []byte) error {
	row, err := s.q.GetEnrollmentTokenByHash(ctx, hash)
	if noRows(err) {
		return fmt.Errorf("enrollment token: %w", ErrNotFound)
	}
	if err != nil {
		return fmt.Errorf("inspect enrollment token: %w", err)
	}
	if row.UsedAt != nil {
		return fmt.Errorf("enrollment token %s: %w", row.ID, ErrTokenUsed)
	}
	if !row.ExpiresAt.After(time.Now().UTC()) {
		return fmt.Errorf("enrollment token %s expired at %s: %w", row.ID, row.ExpiresAt.UTC(), ErrTokenExpired)
	}
	// Lost a race between the failed UPDATE and this read.
	return fmt.Errorf("enrollment token %s: %w", row.ID, ErrTokenUsed)
}
