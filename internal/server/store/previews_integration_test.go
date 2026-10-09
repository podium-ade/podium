//go:build integration

package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPreviewLifecycle(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	task := mustCreateTask(t, s, 1)

	p, err := s.UpsertPreview(ctx, NewPreview{
		TaskID: task.ID, NodeID: "node_a", Via: "lan", Address: "192.168.1.201",
		URLs: map[string]string{"web": "http://192.168.1.201:3000"}, TTL: 90 * time.Minute,
	})
	require.NoError(t, err)
	assert.True(t, p.Live())
	assert.Nil(t, p.ExpiresAt, "the ttl counts from the command's exit, not from publishing")
	assert.Equal(t, "http://192.168.1.201:3000", p.URLs["web"])

	exited := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	held, ok, err := s.HoldPreview(ctx, task.ID, exited)
	require.NoError(t, err)
	require.True(t, ok)
	require.NotNil(t, held.ExpiresAt)
	assert.True(t, held.ExpiresAt.Equal(exited.Add(90*time.Minute)), "expires_at = %s", held.ExpiresAt)

	_, ok, err = s.HoldPreview(ctx, task.ID, exited.Add(time.Hour))
	require.NoError(t, err)
	assert.False(t, ok, "a running ttl is not restarted by a replayed finished event")

	expired, err := s.ExpiredPreviews(ctx, exited.Add(89*time.Minute))
	require.NoError(t, err)
	assert.Empty(t, expired)
	expired, err = s.ExpiredPreviews(ctx, exited.Add(91*time.Minute))
	require.NoError(t, err)
	require.Len(t, expired, 1)

	rel, ok, err := s.ReleasePreview(ctx, task.ID, "released by dev")
	require.NoError(t, err)
	require.True(t, ok)
	assert.False(t, rel.Live())
	_, ok, err = s.ReleasePreview(ctx, task.ID, "again")
	require.NoError(t, err)
	assert.False(t, ok, "releasing is idempotent")

	got, err := s.ListPreviews(ctx, []string{task.ID, "task_other"})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "released by dev", got[task.ID].ReleaseReason)

	// Another attempt of the same task publishes afresh.
	again, err := s.UpsertPreview(ctx, NewPreview{TaskID: task.ID, NodeID: "node_b", Via: "tailnet", Address: "100.64.0.9", TTL: time.Hour})
	require.NoError(t, err)
	assert.True(t, again.Live())
	assert.Equal(t, "node_b", again.NodeID)
}

func TestOrphanedPreviewsAreTheOnesWhoseTaskEndedUnheld(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	running := mustCreateTask(t, s, 1)
	cancelled := mustCreateTask(t, s, 1)
	driveTo(t, s, running.ID, StatusRunning)
	driveTo(t, s, cancelled.ID, StatusCancelled)
	for _, id := range []string{running.ID, cancelled.ID} {
		_, err := s.UpsertPreview(ctx, NewPreview{TaskID: id, Via: "lan", Address: "10.0.0.1", TTL: time.Hour})
		require.NoError(t, err)
	}

	orphaned, err := s.OrphanedPreviews(ctx)
	require.NoError(t, err)
	require.Len(t, orphaned, 1)
	assert.Equal(t, cancelled.ID, orphaned[0].TaskID, "a running task's preview is not an orphan")
}
