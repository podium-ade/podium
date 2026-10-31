//go:build integration

package conductor_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/agent/conductor"
	"github.com/podium-ade/podium/internal/agent/conductor/fakesource"
	"github.com/podium-ade/podium/internal/agent/store"
	podiumv1 "github.com/podium-ade/podium/internal/proto/podium/v1"
)

func replica(id string, ttl time.Duration) func(*conductor.Options) {
	return func(o *conductor.Options) {
		o.ReplicaID = id
		o.LeaseTTL = ttl
	}
}

// TestTwoReplicasServeOneConversationOneTurnAtATime: messages of one conversation land on
// whichever conductor drains them. The one that does not serve it hands them over — into the
// running task, which is warm — and no second task is ever started beside the first.
func TestTwoReplicasServeOneConversationOneTurnAtATime(t *testing.T) {
	st := newStore(t)
	fake := newFakePodium(t)
	release := fake.HoldTasks()
	fake.events = func(taskID string) []*podiumv1.TaskEvent {
		return []*podiumv1.TaskEvent{messageEvent(taskID, 1, conductor.OutFinal, "answer from "+taskID)}
	}
	// One source, drained by both: each event goes to whichever conductor reads it first.
	src := fakesource.New(conductor.KindDev)
	t.Cleanup(src.Close)
	ctx := context.Background()
	startWith(t, st, fake, src, replica("a", time.Minute))
	startWith(t, st, fake, src, replica("b", time.Minute))

	require.NoError(t, src.Send(ctx, inbound("C1/1.1", "first")))
	waitFor(t, 30*time.Second, "the answer", func() bool {
		return len(posts(src.Records(), conductor.OutFinal)) == 1
	})
	for i := range 6 {
		require.NoError(t, src.Send(ctx, inbound("C1/1.1", "more "+string(rune('a'+i)))))
	}
	waitFor(t, 30*time.Second, "every follow-up to reach the one task", func() bool {
		return len(fake.Injects()) == 6
	})
	assert.Len(t, fake.Specs(), 1, "one conversation, one task, whichever conductor heard it")

	release()
	waitFor(t, 60*time.Second, "the turn to finish", func() bool {
		return turnStatus(st, conductor.KindDev+":C1:1.1") == store.TurnSucceeded
	})
}

// TestASurvivingReplicaAdoptsADeadOnesTurn: a conductor dies mid-turn and is never restarted.
// Once its lease runs out, another conductor follows the task and says the answer, once.
func TestASurvivingReplicaAdoptsADeadOnesTurn(t *testing.T) {
	st := newStore(t)
	fake := newFakePodium(t)
	release := fake.HoldTasks()
	fake.events = func(taskID string) []*podiumv1.TaskEvent {
		return []*podiumv1.TaskEvent{messageEvent(taskID, 1, conductor.OutProgress, "working")}
	}
	src := fakesource.New(conductor.KindDev)
	t.Cleanup(src.Close)
	ctx := context.Background()

	dying := startWith(t, st, fake, src, replica("dying", 600*time.Millisecond))
	ev := inbound("C1/1.1", "hello")
	require.NoError(t, src.Send(ctx, ev))
	waitFor(t, 30*time.Second, "the task", func() bool { return len(fake.Specs()) == 1 })
	waitFor(t, 30*time.Second, "the turn to know its task", func() bool {
		return turnStatus(st, ev.SourceKey) == store.TurnRunning && turnOf(t, st, ev.SourceKey).TaskID != ""
	})
	dying.stop()
	taskID := turnOf(t, st, ev.SourceKey).TaskID

	survivor := fakesource.New(conductor.KindDev)
	t.Cleanup(survivor.Close)
	startWith(t, st, fake, survivor, replica("survivor", 600*time.Millisecond))
	// The survivor started while the dead one's lease was still live, so it left the turn
	// alone; it adopts it once the lease has run out.
	fake.mu.Lock()
	fake.tasks[taskID].events = append(fake.tasks[taskID].events, messageEvent(taskID, 2, conductor.OutFinal, "adopted answer"))
	fake.mu.Unlock()
	release()
	waitFor(t, 30*time.Second, "the survivor to finish the turn", func() bool {
		return turnStatus(st, ev.SourceKey) == store.TurnSucceeded
	})
	finals := posts(survivor.Records(), conductor.OutFinal)
	require.Len(t, finals, 1)
	assert.Equal(t, "adopted answer", finals[0].Text)
}

// TestOnlyOneReplicaLeadsAndAnotherTakesOver: work only one conductor may do runs on one,
// and on another once the first stops.
func TestOnlyOneReplicaLeadsAndAnotherTakesOver(t *testing.T) {
	st := newStore(t)
	fake := newFakePodium(t)
	src := fakesource.New(conductor.KindDev)
	t.Cleanup(src.Close)
	ttl := 300 * time.Millisecond
	a := startWith(t, st, fake, src, replica("a", ttl))
	b := startWith(t, st, fake, src, replica("b", ttl))

	var mu sync.Mutex
	leading := map[string]bool{}
	most := 0
	lead := func(name string) func(context.Context) error {
		return func(ctx context.Context) error {
			mu.Lock()
			leading[name] = true
			most = max(most, len(leading))
			mu.Unlock()
			<-ctx.Done()
			mu.Lock()
			delete(leading, name)
			mu.Unlock()
			return nil
		}
	}
	aCtx, stopA := context.WithCancel(context.Background())
	go func() { _ = a.cond.WhileLeader(aCtx, "source:test", lead("a")) }()
	bCtx, stopB := context.WithCancel(context.Background())
	t.Cleanup(stopB)
	go func() { _ = b.cond.WhileLeader(bCtx, "source:test", lead("b")) }()

	waitFor(t, 10*time.Second, "a leader", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(leading) == 1
	})
	time.Sleep(3 * ttl)
	mu.Lock()
	first := leading["a"]
	assert.Equal(t, 1, most, "never two leaders at once")
	mu.Unlock()

	if first {
		stopA()
	} else {
		stopB()
	}
	waitFor(t, 10*time.Second, "the other to take over", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return (first && leading["b"]) || (!first && leading["a"])
	})
	stopA()
	mu.Lock()
	assert.Equal(t, 1, most)
	mu.Unlock()
}
