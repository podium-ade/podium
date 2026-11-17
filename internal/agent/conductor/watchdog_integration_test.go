//go:build integration

package conductor_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/podium-ade/podium/internal/agent/conductor"
	"github.com/podium-ade/podium/internal/agent/conductor/fakesource"
	"github.com/podium-ade/podium/internal/agent/store"
	podiumv1 "github.com/podium-ade/podium/internal/proto/podium/v1"
)

// quietTask scripts a task that started 20 minutes ago, finished `make test` 15 minutes ago
// and has said nothing since, with its last heartbeat at lastBeat.
func quietTask(lastBeat time.Duration) func(string) []*podiumv1.TaskEvent {
	now := time.Now()
	return func(taskID string) []*podiumv1.TaskEvent {
		started := &podiumv1.TaskEvent{
			TaskId: taskID, Seq: 1, Kind: podiumv1.TaskEventKind_TASK_EVENT_KIND_STARTED,
			Ts: timestamppb.New(now.Add(-20 * time.Minute)),
		}
		tool := messageEvent(taskID, 2, conductor.MsgActivity, `{"kind":"tool","tool":"bash","title":"make test","status":"completed"}`)
		tool.Ts = timestamppb.New(now.Add(-15 * time.Minute))
		beat := messageEvent(taskID, 3, conductor.MsgHeartbeat, `{"v":1}`)
		beat.Ts = timestamppb.New(now.Add(-lastBeat))
		return []*podiumv1.TaskEvent{started, tool, beat}
	}
}

func startWatched(t *testing.T, events func(string) []*podiumv1.TaskEvent) (*running, *fakePodium, *fakesource.Source) {
	t.Helper()
	st := newStore(t)
	fake := newFakePodium(t)
	t.Cleanup(fake.HoldTasks())
	fake.events = events
	src := fakesource.New(conductor.KindDev)
	t.Cleanup(src.Close)
	r := startWith(t, st, fake, src, func(o *conductor.Options) { o.WatchdogQuietAfter = 10 * time.Minute })

	require.NoError(t, src.Send(context.Background(), inbound("C1/1.1", "run the gates and push")))
	// The heartbeat is the last scripted event, so once the watch has it, it has them all.
	waitFor(t, 30*time.Second, "the task's events to be watched", func() bool {
		ids := fake.TaskIDs()
		return len(ids) == 1 && r.cond.HeardHeartbeatForTest(ids[0])
	})
	return r, fake, src
}

func watchdogQuestions(src *fakesource.Source) []fakesource.Record {
	var out []fakesource.Record
	for _, rec := range posts(src.Records(), conductor.OutQuestion) {
		if strings.Contains(rec.Text, "Reply **wait**") {
			out = append(out, rec)
		}
	}
	return out
}

// A task whose runtime is alive but which has reported nothing for longer than the threshold
// is asked about, once, and restart interrupts it in place rather than replacing it.
func TestAQuietTaskIsAskedAboutAndRestartInterruptsIt(t *testing.T) {
	r, fake, src := startWatched(t, quietTask(30*time.Second))
	ctx := context.Background()

	r.cond.CheckQuietTasksForTest(ctx, time.Now())
	waitFor(t, 10*time.Second, "the question", func() bool { return len(watchdogQuestions(src)) == 1 })
	q := watchdogQuestions(src)[0]
	assert.Contains(t, q.Text, "has reported nothing for 15 minutes")
	assert.Contains(t, q.Text, "`make test`")
	assert.Contains(t, q.Text, "runtime is still alive")

	r.cond.CheckQuietTasksForTest(ctx, time.Now().Add(time.Minute))
	assert.Len(t, watchdogQuestions(src), 1, "one question at a time")

	require.NoError(t, src.Send(ctx, inbound("C1/1.1", "Restart")))
	waitFor(t, 10*time.Second, "the interrupt", func() bool { return len(fake.Injects()) == 1 })
	assert.Contains(t, fake.Injects()[0].GetText(), "asked me to restart you")
	assert.Empty(t, fake.Cancels(), "a quiet task is interrupted, not cancelled")
	assert.Len(t, fake.Specs(), 1, "and not replaced")
}

// A task whose heartbeat has stopped is asked about as wedged, and end cancels it.
func TestAWedgedTaskIsAskedAboutAndEndCancelsIt(t *testing.T) {
	r, fake, src := startWatched(t, quietTask(5*time.Minute))
	ctx := context.Background()

	r.cond.CheckQuietTasksForTest(ctx, time.Now())
	waitFor(t, 10*time.Second, "the question", func() bool { return len(watchdogQuestions(src)) == 1 })
	assert.Contains(t, watchdogQuestions(src)[0].Text, "has stopped responding")

	// Anything that is not a choice is an ordinary message, for the task.
	require.NoError(t, src.Send(ctx, inbound("C1/1.1", "what is it doing?")))
	require.NoError(t, src.Send(ctx, inbound("C1/1.1", "end")))
	waitFor(t, 10*time.Second, "the cancel", func() bool { return len(fake.Cancels()) == 1 })
	assert.Equal(t, fake.TaskIDs()[0], fake.Cancels()[0].GetTaskId())
	assert.Equal(t, "ended after it went quiet", fake.Cancels()[0].GetReason())
}

// wait leaves the task alone and asks again only after another full interval.
func TestWaitPutsTheQuestionOffForAnotherInterval(t *testing.T) {
	r, fake, src := startWatched(t, quietTask(30*time.Second))
	ctx := context.Background()

	r.cond.CheckQuietTasksForTest(ctx, time.Now())
	waitFor(t, 10*time.Second, "the question", func() bool { return len(watchdogQuestions(src)) == 1 })
	require.NoError(t, src.Send(ctx, inbound("C1/1.1", "wait")))
	waitFor(t, 10*time.Second, "the acknowledgement", func() bool {
		for _, rec := range posts(src.Records(), conductor.OutProgress) {
			if strings.HasPrefix(rec.Text, "Leaving it running.") {
				return true
			}
		}
		return false
	})

	r.cond.CheckQuietTasksForTest(ctx, time.Now().Add(9*time.Minute))
	assert.Len(t, watchdogQuestions(src), 1)
	r.cond.CheckQuietTasksForTest(ctx, time.Now().Add(11*time.Minute))
	waitFor(t, 10*time.Second, "the second question", func() bool { return len(watchdogQuestions(src)) == 2 })
	assert.Empty(t, fake.Injects())
	assert.Empty(t, fake.Cancels())
}

// A delegated task whose heartbeat stopped cannot read an inject, so restart replaces it: the
// old task is cancelled and a new one runs the same playbook and instruction, told that the
// workspace may hold what the old one left.
func TestRestartingAWedgedDelegatedTaskStartsItAgain(t *testing.T) {
	st := newStore(t)
	fake := newFakePodium(t)
	t.Cleanup(fake.HoldTasks())
	wedged := quietTask(5 * time.Minute)
	fake.events = func(taskID string) []*podiumv1.TaskEvent {
		if taskID == "task_01" {
			return wedged(taskID)
		}
		return nil
	}
	src := fakesource.New(conductor.KindDev)
	t.Cleanup(src.Close)

	host := hostRuntime(t, "sk-test")
	r := startWith(t, st, fake, src, func(o *conductor.Options) {
		o.Host = host
		o.WatchdogQuietAfter = 10 * time.Minute
	})
	host.TurnURL = turnAPI(t, r.cond).URL

	message := func(ts, text string, env map[string]string) conductor.InboundEvent {
		ev := inbound("C1/40.1/"+ts, text)
		ev.SourceKey = conductor.KindDev + ":C1:40.1"
		ev.Env = hostEnv(env)
		return ev
	}
	ctx := context.Background()
	first := message("40.1", "run the gates and push",
		map[string]string{hostFakeDelegateEnv: "dogfood|run the gates and push"})
	require.NoError(t, src.Send(ctx, first))
	waitFor(t, 60*time.Second, "the delegated task to be watched", func() bool {
		return turnStatus(st, first.SourceKey) == store.TurnSucceeded && len(fake.TaskIDs()) == 1 &&
			r.cond.HeardHeartbeatForTest("task_01")
	})
	r.cond.CheckQuietTasksForTest(ctx, time.Now())
	waitFor(t, 10*time.Second, "the question", func() bool { return len(watchdogQuestions(src)) == 1 })
	assert.Contains(t, watchdogQuestions(src)[0].Text, "The `dogfood` task has stopped responding")

	require.NoError(t, src.Send(ctx, message("40.2", "restart", nil)))
	waitFor(t, 60*time.Second, "a second task", func() bool { return len(fake.TaskIDs()) == 2 })
	require.Len(t, fake.Cancels(), 1)
	assert.Equal(t, "task_01", fake.Cancels()[0].GetTaskId())

	dlgs := delegationsOf(t, st, first.SourceKey)
	require.Len(t, dlgs, 2)
	var again store.Delegation
	for _, d := range dlgs {
		if d.TaskID == "task_02" {
			again = d
		}
	}
	assert.Equal(t, "dogfood", again.Playbook)
	assert.True(t, strings.HasSuffix(again.Instruction, "\n\nrun the gates and push"), again.Instruction)
	assert.Contains(t, again.Instruction, "stopped responding")
}
