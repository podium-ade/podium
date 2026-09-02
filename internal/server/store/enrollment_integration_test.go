//go:build integration

package store

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestEnrollmentTokenPlaintextNeverReachesTheDatabase(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)

	plaintext, id, err := s.CreateEnrollmentToken(ctx, []string{"demo"}, time.Hour, "alvaro")
	require.NoError(t, err)
	require.NotEmpty(t, id)

	raw, err := base64.RawURLEncoding.DecodeString(plaintext)
	require.NoError(t, err, "the token is base64url")
	require.Len(t, raw, 32, "32 bytes of entropy")

	// Dump every column of the row as text and prove the secret is not in any of them.
	var dump string
	require.NoError(t, s.pool.QueryRow(ctx,
		"select row_to_json(t)::text from enrollment_tokens t where id = $1", id).Scan(&dump))
	require.NotContains(t, dump, plaintext)
	require.False(t, strings.Contains(dump, plaintext[:16]), "not even a prefix of the token is stored")

	// What is stored is exactly the SHA-256.
	var storedHash []byte
	require.NoError(t, s.pool.QueryRow(ctx,
		"select token_hash from enrollment_tokens where id = $1", id).Scan(&storedHash))
	require.Equal(t, hex.EncodeToString(HashToken(plaintext)), hex.EncodeToString(storedHash))
	require.Len(t, storedHash, 32)
}

func TestConsumeEnrollmentTokenIsSingleUse(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	node := mustCreateNode(t, s, "n1", nil)

	plaintext, id, err := s.CreateEnrollmentToken(ctx, []string{"demo", "browser"}, time.Hour, "alvaro")
	require.NoError(t, err)

	labels, err := s.ConsumeEnrollmentToken(ctx, plaintext, node.ID)
	require.NoError(t, err)
	require.Equal(t, []string{"demo", "browser"}, labels)

	var usedBy *string
	require.NoError(t, s.pool.QueryRow(ctx,
		"select used_by_node_id from enrollment_tokens where id = $1", id).Scan(&usedBy))
	require.NotNil(t, usedBy)
	require.Equal(t, node.ID, *usedBy)

	// Reuse is refused.
	_, err = s.ConsumeEnrollmentToken(ctx, plaintext, node.ID)
	require.ErrorIs(t, err, ErrTokenUsed)

	// An unknown token is not found.
	_, err = s.ConsumeEnrollmentToken(ctx, "not-a-real-token", node.ID)
	require.ErrorIs(t, err, ErrNotFound)
}

func TestConsumeEnrollmentTokenRejectsExpired(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	node := mustCreateNode(t, s, "n1", nil)

	plaintext, id, err := s.CreateEnrollmentToken(ctx, []string{"demo"}, time.Hour, "alvaro")
	require.NoError(t, err)
	_, err = s.pool.Exec(ctx,
		"update enrollment_tokens set expires_at = now() - interval '1 second' where id = $1", id)
	require.NoError(t, err)

	_, err = s.ConsumeEnrollmentToken(ctx, plaintext, node.ID)
	require.ErrorIs(t, err, ErrTokenExpired)

	var usedAt *time.Time
	require.NoError(t, s.pool.QueryRow(ctx,
		"select used_at from enrollment_tokens where id = $1", id).Scan(&usedAt))
	require.Nil(t, usedAt, "a rejected consume must not mark the token used")
}

func TestConsumeEnrollmentTokenIsAtomicUnderRace(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)

	plaintext, _, err := s.CreateEnrollmentToken(ctx, []string{"demo"}, time.Hour, "alvaro")
	require.NoError(t, err)

	const racers = 8
	nodes := make([]Node, racers)
	for i := range nodes {
		nodes[i] = mustCreateNode(t, s, "racer-"+strconv.Itoa(i), nil)
	}

	var wg sync.WaitGroup
	errs := make([]error, racers)
	start := make(chan struct{})
	wg.Add(racers)
	for i := 0; i < racers; i++ {
		go func(i int) {
			defer wg.Done()
			<-start
			_, errs[i] = s.ConsumeEnrollmentToken(ctx, plaintext, nodes[i].ID)
		}(i)
	}
	close(start)
	wg.Wait()

	winners := 0
	for i, err := range errs {
		if err == nil {
			winners++
			continue
		}
		require.ErrorIs(t, err, ErrTokenUsed, "racer %d got an unexpected error", i)
	}
	require.Equal(t, 1, winners, "exactly one enrollment may redeem a token")
}

func TestCreateEnrollmentTokenDefaultsTTL(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)

	_, id, err := s.CreateEnrollmentToken(ctx, nil, 0, "alvaro")
	require.NoError(t, err)

	var expires time.Time
	require.NoError(t, s.pool.QueryRow(ctx,
		"select expires_at from enrollment_tokens where id = $1", id).Scan(&expires))
	require.WithinDuration(t, time.Now().UTC().Add(DefaultEnrollmentTokenTTL), expires.UTC(), time.Minute)

	var labels string
	require.NoError(t, s.pool.QueryRow(ctx,
		"select labels::text from enrollment_tokens where id = $1", id).Scan(&labels))
	require.JSONEq(t, `[]`, labels)
}
