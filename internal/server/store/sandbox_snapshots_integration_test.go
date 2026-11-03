//go:build integration

package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// A session's image is replaced in one transaction with retiring the one before, and dropping
// one retires it too unless the provider no longer has it.
func TestSandboxSnapshotsRetireWhatTheyReplace(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)

	require.NoError(t, s.PutSandboxSnapshot(ctx, SandboxSnapshot{SessionID: "sess_a", Provider: "modal", ImageID: "im-1", NodeID: "n1", TaskID: "t1"}))
	require.NoError(t, s.PutSandboxSnapshot(ctx, SandboxSnapshot{SessionID: "sess_b", Provider: "modal", ImageID: "im-b"}))
	retired, err := s.ListRetiredSandboxSnapshots(ctx)
	require.NoError(t, err)
	require.Empty(t, retired, "a session's first image replaces nothing")

	require.NoError(t, s.PutSandboxSnapshot(ctx, SandboxSnapshot{SessionID: "sess_a", Provider: "modal", ImageID: "im-2", TaskID: "t2"}))
	require.NoError(t, s.PutSandboxSnapshot(ctx, SandboxSnapshot{SessionID: "sess_a", Provider: "modal", ImageID: "im-2", TaskID: "t2"}))
	got, err := s.GetSandboxSnapshot(ctx, "sess_a")
	require.NoError(t, err)
	require.Equal(t, "im-2", got.ImageID)
	require.Equal(t, "t2", got.TaskID)
	retired, err = s.ListRetiredSandboxSnapshots(ctx)
	require.NoError(t, err)
	require.Len(t, retired, 1, "the same image again retires nothing")
	require.Equal(t, "im-1", retired[0].ImageID)
	require.Equal(t, "modal", retired[0].Provider)

	sp := testSpec()
	sp.WorkspaceSession = "sess_b"
	_, err = s.CreateTask(ctx, NewTask{Spec: sp, RequestedBy: "dev", MaxAttempts: 1})
	require.NoError(t, err)
	rows, err := s.ListSandboxSnapshots(ctx)
	require.NoError(t, err)
	require.Len(t, rows, 2)
	require.Equal(t, "sess_a", rows[0].SessionID, "newest first")
	inUse := map[string]bool{}
	for _, r := range rows {
		inUse[r.SessionID] = r.InUse
	}
	require.Equal(t, map[string]bool{"sess_a": false, "sess_b": true}, inUse)

	ok, err := s.DropSandboxSnapshot(ctx, SandboxSnapshot{SessionID: "sess_a", Provider: "modal", ImageID: "im-1"}, true)
	require.NoError(t, err)
	require.False(t, ok, "an image the row no longer points at drops nothing")
	ok, err = s.DropSandboxSnapshot(ctx, got, true)
	require.NoError(t, err)
	require.True(t, ok)
	_, err = s.GetSandboxSnapshot(ctx, "sess_a")
	require.ErrorIs(t, err, ErrNotFound)

	b, err := s.GetSandboxSnapshot(ctx, "sess_b")
	require.NoError(t, err)
	ok, err = s.DropSandboxSnapshot(ctx, b, false)
	require.NoError(t, err)
	require.True(t, ok)
	retired, err = s.ListRetiredSandboxSnapshots(ctx)
	require.NoError(t, err)
	ids := map[string]bool{}
	for _, r := range retired {
		ids[r.ImageID] = true
	}
	require.Equal(t, map[string]bool{"im-1": true, "im-2": true}, ids, "a forgotten image is not retired")

	require.NoError(t, s.FailRetiredSandboxSnapshot(ctx, "im-1", "modal is down"))
	require.NoError(t, s.ForgetRetiredSandboxSnapshot(ctx, "im-2"))
	retired, err = s.ListRetiredSandboxSnapshots(ctx)
	require.NoError(t, err)
	require.Len(t, retired, 1)
	require.Equal(t, 1, retired[0].Attempts)
	require.Equal(t, "modal is down", retired[0].LastError)
}

func TestSandboxSnapshotRetentionDefaultsToKeepingEverything(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	got, err := s.GetSandboxSnapshotRetention(ctx)
	require.NoError(t, err)
	require.Zero(t, got.MaxAge)
	require.Zero(t, got.MaxCount)

	_, err = s.SetSandboxSnapshotRetention(ctx, SandboxSnapshotRetention{MaxAge: 72 * time.Hour, MaxCount: 50, UpdatedBy: "ada"})
	require.NoError(t, err)
	got, err = s.GetSandboxSnapshotRetention(ctx)
	require.NoError(t, err)
	require.Equal(t, 72*time.Hour, got.MaxAge)
	require.Equal(t, int64(50), got.MaxCount)
	require.Equal(t, "ada", got.UpdatedBy)

	_, err = s.SetSandboxSnapshotRetention(ctx, SandboxSnapshotRetention{MaxCount: -1})
	require.Error(t, err)
}
