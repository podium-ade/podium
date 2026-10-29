//go:build integration

package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestListAndDeleteWorkspaceSnapshots(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)

	for _, id := range []string{"sess_a", "sess_b"} {
		_, err := s.PutWorkspaceSnapshot(ctx, WorkspaceSnapshot{SessionID: id, ObjectKey: "k/" + id, SizeBytes: 10, SHA256: "x"})
		require.NoError(t, err)
	}
	sp := testSpec()
	sp.WorkspaceSession = "sess_b"
	_, err := s.CreateTask(ctx, NewTask{Spec: sp, RequestedBy: "dev", MaxAttempts: 1})
	require.NoError(t, err)

	rows, err := s.ListWorkspaceSnapshots(ctx)
	require.NoError(t, err)
	require.Len(t, rows, 2)
	inUse := map[string]bool{}
	for _, r := range rows {
		inUse[r.SessionID] = r.InUse
	}
	require.Equal(t, map[string]bool{"sess_a": false, "sess_b": true}, inUse, "a queued task holds its session")

	ok, err := s.DeleteWorkspaceSnapshot(ctx, "sess_a", "k/stale")
	require.NoError(t, err)
	require.False(t, ok, "a key the row no longer points at deletes nothing")
	ok, err = s.DeleteWorkspaceSnapshot(ctx, "sess_a", "k/sess_a")
	require.NoError(t, err)
	require.True(t, ok)
	_, err = s.GetWorkspaceSnapshot(ctx, "sess_a")
	require.ErrorIs(t, err, ErrNotFound)
}

func TestSnapshotRetentionDefaultsToKeepingEverything(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)

	got, err := s.GetSnapshotRetention(ctx)
	require.NoError(t, err)
	require.Zero(t, got.MaxAge)
	require.Zero(t, got.MaxTotalBytes)

	set, err := s.SetSnapshotRetention(ctx, SnapshotRetention{MaxAge: 72 * time.Hour, MaxTotalBytes: 5 << 30, UpdatedBy: "ada"})
	require.NoError(t, err)
	require.Equal(t, 72*time.Hour, set.MaxAge)
	got, err = s.GetSnapshotRetention(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(5<<30), got.MaxTotalBytes)
	require.Equal(t, "ada", got.UpdatedBy)

	_, err = s.SetSnapshotRetention(ctx, SnapshotRetention{MaxAge: -time.Second})
	require.Error(t, err)
}
