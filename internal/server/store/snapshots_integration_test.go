//go:build integration

package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
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

// TestARetiredBaseIsKeptWhileAnythingRestoresFromIt: a replaced base is only free to delete
// once no snapshot is a difference from it and no session still starts from it.
func TestARetiredBaseIsKeptWhileAnythingRestoresFromIt(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	const oldBase = "snapshots/bases/app/snap_old.tar"
	require.NoError(t, st.RetireWorkspaceBase(ctx, oldBase))
	require.NoError(t, st.RetireWorkspaceBase(ctx, oldBase), "retiring twice is once")

	require.NoError(t, st.SetSessionOrigin(ctx, "sess_a", oldBase))
	_, err := st.PutWorkspaceSnapshot(ctx, WorkspaceSnapshot{SessionID: "sess_b", ObjectKey: "snapshots/sessions/b/1.tar",
		SizeBytes: 1, SHA256: "x", BaseKey: oldBase})
	require.NoError(t, err)
	got, err := st.GetWorkspaceSnapshot(ctx, "sess_b")
	require.NoError(t, err)
	assert.Equal(t, oldBase, got.BaseKey)
	origin, err := st.GetSessionOrigin(ctx, "sess_a")
	require.NoError(t, err)
	assert.Equal(t, oldBase, origin)

	free, err := st.UnreferencedRetiredBases(ctx)
	require.NoError(t, err)
	assert.Empty(t, free, "a session starts from it and a snapshot is a difference from it")

	require.NoError(t, st.DeleteSessionOrigin(ctx, "sess_a"))
	free, err = st.UnreferencedRetiredBases(ctx)
	require.NoError(t, err)
	assert.Empty(t, free, "the snapshot still needs it")

	ok, err := st.DeleteWorkspaceSnapshot(ctx, "sess_b", "snapshots/sessions/b/1.tar")
	require.NoError(t, err)
	require.True(t, ok)
	free, err = st.UnreferencedRetiredBases(ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{oldBase}, free)
	require.NoError(t, st.DeleteRetiredBase(ctx, oldBase))
	free, err = st.UnreferencedRetiredBases(ctx)
	require.NoError(t, err)
	assert.Empty(t, free)
	_, err = st.GetSessionOrigin(ctx, "sess_a")
	assert.ErrorIs(t, err, ErrNotFound)
}
