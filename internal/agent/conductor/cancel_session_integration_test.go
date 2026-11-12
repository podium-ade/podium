//go:build integration

package conductor_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/agent/conductor"
	"github.com/podium-ade/podium/internal/agent/conductor/fakesource"
	"github.com/podium-ade/podium/internal/agent/store"
	podiumv1 "github.com/podium-ade/podium/internal/proto/podium/v1"
)

// startStoppable is a conductor whose tasks run until a test ends them, and end cancelled
// when they are cancelled: the shape of a long playbook turn somebody presses Stop on.
func startStoppable(t *testing.T, events func(taskID string) []*podiumv1.TaskEvent) (
	*store.Store, *fakePodium, *fakesource.Source, *running, func(),
) {
	t.Helper()
	st := newStore(t)
	fake := newFakePodium(t)
	release := fake.HoldTasks()
	t.Cleanup(release)
	fake.terminal = podiumv1.TaskStatus_TASK_STATUS_CANCELLED
	fake.events = events
	src := fakesource.New(conductor.KindDev)
	t.Cleanup(src.Close)
	r := start(t, st, fake, src)
	return st, fake, src, r, release
}

const stopReason = "stopped from the Linear agent session"

// A Stop in the middle of a task cancels the task through the control plane, and the turn
// is recorded as cancelled, as any cancelled turn is.
func TestStoppingASessionCancelsItsRunningTask(t *testing.T) {
	st, fake, src, r, _ := startStoppable(t, nil)
	ctx := context.Background()
	ev := inbound("C1/80.1", "a long job")
	require.NoError(t, src.Send(ctx, ev))
	waitFor(t, 30*time.Second, "the task", func() bool { return len(fake.TaskIDs()) == 1 })

	stopped, err := r.cond.CancelSessionTurn(ctx, ev.SourceKey, stopReason)
	require.NoError(t, err)
	assert.True(t, stopped)
	cancels := fake.Cancels()
	require.Len(t, cancels, 1)
	assert.Equal(t, fake.TaskIDs()[0], cancels[0].GetTaskId())
	assert.Equal(t, stopReason, cancels[0].GetReason())
	waitFor(t, 30*time.Second, "the turn to be recorded cancelled", func() bool {
		return turnStatus(st, ev.SourceKey) == store.TurnCancelled
	})
	assert.Contains(t, reactions(src.Records()), string(conductor.ReactionFailed))
}

// A follow-up queued behind the stopped turn never runs: the Stop ends the session's work.
func TestStoppingASessionDropsWhatIsQueuedBehindIt(t *testing.T) {
	st, fake, src, r, _ := startStoppable(t, nil)
	ctx := context.Background()
	first := inbound("C1/81.1", "a long job")
	require.NoError(t, src.Send(ctx, first))
	waitFor(t, 30*time.Second, "the task", func() bool { return len(fake.TaskIDs()) == 1 })
	require.NoError(t, src.Send(ctx, asker("C1/81.1", "and then this", "bob@podium.test")))
	waitFor(t, 30*time.Second, "the follow-up to be queued", func() bool { return queued(t, st, first.SourceKey) == 1 })

	stopped, err := r.cond.CancelSessionTurn(ctx, first.SourceKey, stopReason)
	require.NoError(t, err)
	assert.True(t, stopped)
	assert.Zero(t, queued(t, st, first.SourceKey))
	waitFor(t, 30*time.Second, "the turn to be recorded cancelled", func() bool {
		return turnStatus(st, first.SourceKey) == store.TurnCancelled
	})
	time.Sleep(time.Second)
	assert.Len(t, fake.Specs(), 1, "the queued follow-up started no task")
	assert.Len(t, startedBy(t, st, first.SourceKey), 1)
}

// Pressed twice, the second Stop finds the task already cancelled — still stopping, as far
// as anybody can tell — and once the turn has ended, finds nothing.
func TestStoppingASessionTwice(t *testing.T) {
	st, fake, src, r, _ := startStoppable(t, nil)
	ctx := context.Background()
	ev := inbound("C1/82.1", "a long job")
	require.NoError(t, src.Send(ctx, ev))
	waitFor(t, 30*time.Second, "the task", func() bool { return len(fake.TaskIDs()) == 1 })

	stopped, err := r.cond.CancelSessionTurn(ctx, ev.SourceKey, stopReason)
	require.NoError(t, err)
	require.True(t, stopped)
	waitFor(t, 30*time.Second, "the turn to be recorded cancelled", func() bool {
		return turnStatus(st, ev.SourceKey) == store.TurnCancelled
	})
	stopped, err = r.cond.CancelSessionTurn(ctx, ev.SourceKey, stopReason)
	require.NoError(t, err)
	assert.False(t, stopped, "nothing is running any more")
	assert.Len(t, fake.Cancels(), 1)
}

func TestStoppingASessionNobodyHasStartedStopsNothing(t *testing.T) {
	_, fake, _, r, _ := startStoppable(t, nil)
	stopped, err := r.cond.CancelSessionTurn(context.Background(), "linear:session:never-heard-of", stopReason)
	require.NoError(t, err)
	assert.False(t, stopped)
	assert.Empty(t, fake.Cancels())
}

// A turn parked on a question is released by a Stop: its task is cancelled, and the next
// message is a turn of its own rather than a reply injected into the task that was stopped.
func TestStoppingASessionReleasesATurnParkedOnAQuestion(t *testing.T) {
	st, fake, src, r, _ := startStoppable(t, func(taskID string) []*podiumv1.TaskEvent {
		return []*podiumv1.TaskEvent{messageEvent(taskID, 1, conductor.MsgQuestion, "Which branch?")}
	})
	ctx := context.Background()
	ev := inbound("C1/83.1", "fix the build")
	require.NoError(t, src.Send(ctx, ev))
	waitFor(t, 30*time.Second, "the question", func() bool {
		return len(posts(src.Records(), conductor.OutQuestion)) == 1
	})

	stopped, err := r.cond.CancelSessionTurn(ctx, ev.SourceKey, stopReason)
	require.NoError(t, err)
	assert.True(t, stopped)
	waitFor(t, 30*time.Second, "the turn to be recorded cancelled", func() bool {
		return turnStatus(st, ev.SourceKey) == store.TurnCancelled
	})

	require.NoError(t, src.Send(ctx, inbound("C1/83.1", "main")))
	waitFor(t, 30*time.Second, "a turn of its own", func() bool { return len(fake.Specs()) == 2 })
	assert.Empty(t, fake.Injects(), "nothing goes into the task that was stopped")
}
