//go:build integration

package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A Retry claims the retried message's deliveries and nobody else's: other chats' due rows
// stay for the worker, unclaimed and with their attempts untouched.
func TestClaimOutboxForTakesOnlyThatMessage(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	mine, err := s.CreateChat(ctx, "alice", "mine")
	require.NoError(t, err)
	theirs, err := s.CreateChat(ctx, "bob", "theirs")
	require.NoError(t, err)
	due := time.Now().Add(-time.Second)
	p := OutboxPayload{Type: "final", Text: "the answer."}
	_, err = s.EnqueueOutbox(ctx, mine.ID, 2, "slack", "C1/1.1", p, due)
	require.NoError(t, err)
	_, err = s.EnqueueOutbox(ctx, mine.ID, 3, "slack", "C1/1.1", p, due)
	require.NoError(t, err)
	other, err := s.EnqueueOutbox(ctx, theirs.ID, 2, "slack", "C2/2.2", p, due)
	require.NoError(t, err)

	got, err := s.ClaimOutboxFor(ctx, mine.ID, 2, time.Minute)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, mine.ID, got[0].ChatID)
	assert.EqualValues(t, 2, got[0].MessageSeq)
	assert.Equal(t, 1, got[0].Attempts)

	still, err := s.GetOutbox(ctx, other.ID)
	require.NoError(t, err)
	assert.Zero(t, still.Attempts, "another chat's delivery is not claimed by somebody else's Retry")

	again, err := s.ClaimOutboxFor(ctx, mine.ID, 2, time.Minute)
	require.NoError(t, err)
	assert.Empty(t, again, "a claimed row is leased, not claimed twice")
}
