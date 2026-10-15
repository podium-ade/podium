//go:build integration

package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/pkg/spec"
)

// A task's sandbox is recorded for its current attempt only, and queueing its reap twice
// queues it once.
func TestTaskSandboxAndItsReap(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	node := mustCreateNode(t, s, "boat-a", []string{"sandbox.provider=boat"})
	task, err := s.CreateTask(ctx, NewTask{Spec: spec.TaskSpec{Image: "alpine:3"}})
	require.NoError(t, err)

	provider, err := s.QueueSandboxReap(ctx, task.ID, "lost")
	require.NoError(t, err)
	require.Empty(t, provider, "a task with no sandbox has nothing to reap")

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

	provider, err = s.QueueSandboxReap(ctx, task.ID, "lost")
	require.NoError(t, err)
	require.Equal(t, "boat", provider)
	provider, err = s.QueueSandboxReap(ctx, task.ID, "lost again")
	require.NoError(t, err)
	require.Empty(t, provider, "already queued")

	reaps, err := s.ListSandboxReaps(ctx, "boat")
	require.NoError(t, err)
	require.Len(t, reaps, 1)
	require.Equal(t, "sbx-1", reaps[0].SandboxID)
	require.Equal(t, task.ID, reaps[0].TaskID)

	require.NoError(t, s.DeleteSandboxReap(ctx, "boat", "sbx-1"))
	reaps, err = s.ListSandboxReaps(ctx, "boat")
	require.NoError(t, err)
	require.Empty(t, reaps)
}
