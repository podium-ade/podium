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
	task, err := s.CreateTask(ctx, NewTask{Spec: spec.TaskSpec{Image: "alpine:3"}, MaxAttempts: 4})
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

	require.NoError(t, s.SandboxDeleted(ctx, "boat", "sbx-1"))
	reaps, err = s.ListSandboxReaps(ctx, "boat")
	require.NoError(t, err)
	require.Empty(t, reaps)

	require.NoError(t, s.AssignTask(ctx, task.ID, node.ID, "lease_2", time.Now().Add(time.Minute)))
	_, err = s.TransitionTask(ctx, task.ID, ActiveStatuses, StatusQueued, Patch{ReapSandbox: "lost"})
	require.NoError(t, err)
	reaps, err = s.ListSandboxReaps(ctx, "boat")
	require.NoError(t, err)
	require.Empty(t, reaps, "a sandbox a node already deleted is not queued again")
}

// A finished task's sandbox counts until a node says it is gone: while its preview is held,
// and while it is torn down after the task or its preview ends. One no node ever answers for
// stops counting after the grace period, and a sandbox both held and queued counts once.
func TestSandboxesKept(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	node := mustCreateNode(t, s, "boat-a", []string{"sandbox.provider=boat"})
	kept := func() map[string]int32 {
		k, err := s.SandboxesKept(ctx)
		require.NoError(t, err)
		return k
	}
	sandboxTask := func(id string, preview bool) Task {
		task, err := s.CreateTask(ctx, NewTask{Spec: spec.TaskSpec{Image: "alpine:3"}})
		require.NoError(t, err)
		require.NoError(t, s.AssignTask(ctx, task.ID, node.ID, "lease_"+id, time.Now().Add(time.Minute)))
		_, err = s.SetTaskSandbox(ctx, task.ID, "lease_"+id, "boat", id)
		require.NoError(t, err)
		if preview {
			_, err = s.UpsertPreview(ctx, NewPreview{TaskID: task.ID, NodeID: node.ID, Via: "sandbox", TTL: time.Hour})
			require.NoError(t, err)
		}
		return task
	}
	finish := func(task Task, at time.Time) {
		_, err := s.TransitionTask(ctx, task.ID, nil, StatusFailed, Patch{FinishedAt: &at})
		require.NoError(t, err)
	}

	done := sandboxTask("sbx-1", false)
	held := sandboxTask("sbx-2", true)
	require.Empty(t, kept(), "a running task's sandbox is the running task, not a kept one")

	finish(done, time.Now())
	require.Equal(t, map[string]int32{"boat": 1}, kept(), "being torn down")
	require.NoError(t, s.SandboxDeleted(ctx, "boat", "sbx-1"))
	require.Empty(t, kept())

	long := time.Now().Add(-time.Hour)
	finish(held, long)
	_, _, err := s.HoldPreview(ctx, held.ID, long)
	require.NoError(t, err)
	require.Equal(t, map[string]int32{"boat": 1}, kept(), "held long after its task finished")
	_, err = s.q.QueueSandboxReap(ctx, db.QueueSandboxReapParams{TaskID: held.ID, Reason: "lost"})
	require.NoError(t, err)
	require.Equal(t, map[string]int32{"boat": 1}, kept(), "held and queued is still one sandbox")
	require.NoError(t, s.SandboxDeleted(ctx, "boat", "sbx-2"))
	require.Empty(t, kept())

	released := sandboxTask("sbx-3", true)
	finish(released, long)
	_, _, err = s.HoldPreview(ctx, released.ID, long)
	require.NoError(t, err)
	_, _, err = s.ReleasePreview(ctx, released.ID, "released")
	require.NoError(t, err)
	require.Equal(t, map[string]int32{"boat": 1}, kept(), "released, and its node is still deleting it")

	finish(sandboxTask("sbx-4", false), long)
	require.Equal(t, map[string]int32{"boat": 1}, kept(), "no node answered for an hour: no longer counted")
}
