//go:build integration

package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOneConductorDoesAPieceOfWorkAtATime(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	key := SessionLease("sess_1")

	ok, err := s.AcquireLease(ctx, key, "a", time.Minute)
	require.NoError(t, err)
	require.True(t, ok)
	ok, err = s.AcquireLease(ctx, key, "b", time.Minute)
	require.NoError(t, err)
	assert.False(t, ok, "a live lease is not taken")
	ok, err = s.AcquireLease(ctx, key, "a", time.Minute)
	require.NoError(t, err)
	assert.True(t, ok, "re-acquiring your own lease renews it")

	// b hears a message while a serves and a has nothing warm: it is left for a.
	warm, ok, err := s.HandOffToOwner(ctx, key, "b", []byte(`{"Text":"also this"}`))
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Empty(t, warm)

	released, err := s.ReleaseLease(ctx, key, "a")
	require.NoError(t, err)
	assert.False(t, released, "a message is waiting, so the owner keeps the session")
	pending, err := s.TakePending(ctx, key, "a")
	require.NoError(t, err)
	assert.JSONEq(t, `{"Text":"also this"}`, string(pending))
	pending, err = s.TakePending(ctx, key, "a")
	require.NoError(t, err)
	assert.Nil(t, pending, "taken once")

	// With a warm task, the message is injected by whoever heard it, and nothing is left.
	require.NoError(t, s.SetWarmTask(ctx, key, "a", "task_7"))
	warm, ok, err = s.HandOffToOwner(ctx, key, "b", []byte(`{"Text":"and this"}`))
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, "task_7", warm)
	require.NoError(t, s.SetWarmTask(ctx, key, "a", ""))

	released, err = s.ReleaseLease(ctx, key, "a")
	require.NoError(t, err)
	assert.True(t, released)
	_, ok, err = s.HandOffToOwner(ctx, key, "b", []byte(`{}`))
	require.NoError(t, err)
	assert.False(t, ok, "nobody holds it, so b takes it itself")
	ok, err = s.AcquireLease(ctx, key, "b", time.Minute)
	require.NoError(t, err)
	assert.True(t, ok)
}

func TestALeaseNobodyRenewsIsAnybodys(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	key := DelegationLease("dlg_1")

	ok, err := s.AcquireLease(ctx, key, "dead", 50*time.Millisecond)
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, s.SetWarmTask(ctx, key, "dead", "task_1"))
	time.Sleep(100 * time.Millisecond)

	_, ok, err = s.HandOffToOwner(ctx, key, "live", []byte(`{}`))
	require.NoError(t, err)
	assert.False(t, ok, "an expired owner is not handed anything")
	ok, err = s.AcquireLease(ctx, key, "live", time.Minute)
	require.NoError(t, err)
	assert.True(t, ok)
	warm, ok, err := s.HandOffToOwner(ctx, key, "third", []byte(`{}`))
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Empty(t, warm, "a takeover does not inherit the dead owner's warm task")
	require.NoError(t, s.RenewLeases(ctx, "live", []string{key}, time.Minute))
}
