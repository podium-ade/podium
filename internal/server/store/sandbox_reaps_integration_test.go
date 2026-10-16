//go:build integration

package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/server/store/db"
	"github.com/podium-ade/podium/pkg/spec"
)

// A task's sandbox is recorded for its current attempt only, and the transition that gives
// up on the attempt queues it for deletion.
func TestTaskSandboxAndItsReap(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	node := mustCreateNode(t, s, "boat-a", []string{"sandbox.provider=boat"})
	task, err := s.CreateTask(ctx, NewTask{Spec: spec.TaskSpec{Image: "alpine:3"}, MaxAttempts: 3})
	require.NoError(t, err)

	require.NoError(t, s.AssignTask(ctx, task.ID, node.ID, "lease_0", time.Now().Add(time.Minute)))
	_, err = s.TransitionTask(ctx, task.ID, ActiveStatuses, StatusQueued, Patch{ReapSandbox: "lost"})
	require.NoError(t, err)
	reaps, err := s.ListSandboxReaps(ctx, "boat")
	require.NoError(t, err)
	require.Empty(t, reaps, "a task with no sandbox has nothing to reap")

	require.NoError(t, s.AssignTask(ctx, task.ID, node.ID, "lease_1", time.Now().Add(time.Minute)))
	ok, err := s.SetTaskSandbox(ctx, task.ID, "lease_0", "boat", "sbx-stale")
	require.NoError(t, err)
	require.False(t, ok, "an event from another attempt records nothing")
	ok, err = s.SetTaskSandbox(ctx, task.ID, "lease_1", "boat", "sbx-1")
	require.NoError(t, err)
	require.True(t, ok)
	got, err := s.GetTask(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, "sbx-1", got.SandboxID)

	lost, err := s.TransitionTask(ctx, task.ID, ActiveStatuses, StatusQueued, Patch{ReapSandbox: "lost"})
	require.NoError(t, err)
	require.Equal(t, "boat", lost.SandboxProvider)
	reaps, err = s.ListSandboxReaps(ctx, "boat")
	require.NoError(t, err)
	require.Len(t, reaps, 1)
	require.Equal(t, "sbx-1", reaps[0].SandboxID)
	require.Equal(t, task.ID, reaps[0].TaskID)
	require.Equal(t, "lost", reaps[0].Reason)

	require.NoError(t, s.DeleteSandboxReap(ctx, "boat", "sbx-1"))
	reaps, err = s.ListSandboxReaps(ctx, "boat")
	require.NoError(t, err)
	require.Empty(t, reaps)
}

// A sandbox is kept up while its preview is held or its deletion is queued, and counted
// once when it is both.
func TestSandboxesKept(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	node := mustCreateNode(t, s, "boat-a", []string{"sandbox.provider=boat"})
	sandboxTask := func(lease, id string) Task {
		task, err := s.CreateTask(ctx, NewTask{Spec: spec.TaskSpec{Image: "alpine:3"}})
		require.NoError(t, err)
		require.NoError(t, s.AssignTask(ctx, task.ID, node.ID, lease, time.Now().Add(time.Minute)))
		_, err = s.SetTaskSandbox(ctx, task.ID, lease, "boat", id)
		require.NoError(t, err)
		_, err = s.UpsertPreview(ctx, NewPreview{TaskID: task.ID, NodeID: node.ID, Via: "sandbox", TTL: time.Hour})
		require.NoError(t, err)
		return task
	}
	kept := func() map[string]int32 {
		k, err := s.SandboxesKept(ctx)
		require.NoError(t, err)
		return k
	}

	held := sandboxTask("lease_1", "sbx-1")
	sandboxTask("lease_2", "sbx-2")
	require.Empty(t, kept(), "a preview whose command still runs is a running task, not a kept sandbox")

	_, _, err := s.HoldPreview(ctx, held.ID, time.Now())
	require.NoError(t, err)
	require.Equal(t, map[string]int32{"boat": 1}, kept())
	_, err = s.q.QueueSandboxReap(ctx, db.QueueSandboxReapParams{TaskID: held.ID, Reason: "lost"})
	require.NoError(t, err)
	require.Equal(t, map[string]int32{"boat": 1}, kept(), "held and queued is still one sandbox")

	_, _, err = s.ReleasePreview(ctx, held.ID, "released")
	require.NoError(t, err)
	require.Equal(t, map[string]int32{"boat": 1}, kept(), "released but not deleted yet")
	require.NoError(t, s.DeleteSandboxReap(ctx, "boat", "sbx-1"))
	require.Empty(t, kept())
}
