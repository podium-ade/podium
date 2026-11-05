//go:build integration

package conductor_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/agent/chathost"
	"github.com/podium-ade/podium/internal/agent/conductor"
	"github.com/podium-ade/podium/internal/agent/conductor/fakesource"
	"github.com/podium-ade/podium/internal/agent/store"
)

// blockingChatHost is a chat host whose every answer runs until it is stopped, the way a long
// model turn would. It records which turns were interrupted.
type blockingChatHost struct {
	mu          sync.Mutex
	started     []string
	interrupted []string
}

func (b *blockingChatHost) Open(context.Context, chathost.Location) error { return nil }
func (b *blockingChatHost) Close(string) error                            { return nil }
func (b *blockingChatHost) Steer(context.Context, string, string, string) error {
	return nil
}

func (b *blockingChatHost) Answer(ctx context.Context, t chathost.Turn) (chathost.Result, error) {
	b.mu.Lock()
	b.started = append(b.started, t.Prompt)
	b.mu.Unlock()
	if t.Started != nil {
		t.Started("ses_" + t.Location)
	}
	<-ctx.Done()
	b.mu.Lock()
	b.interrupted = append(b.interrupted, t.Prompt)
	b.mu.Unlock()
	return chathost.Result{Outcome: "interrupted"}, nil
}

func (b *blockingChatHost) counts() (started, interrupted int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.started), len(b.interrupted)
}

func startBlockingChatHost(t *testing.T, maxTurns int) (*store.Store, *fakesource.Source, *running, *blockingChatHost) {
	t.Helper()
	st := newStore(t)
	fake := newFakePodium(t)
	src := fakesource.New(conductor.KindDev)
	t.Cleanup(src.Close)
	host := &blockingChatHost{}
	r := startWith(t, st, fake, src, func(o *conductor.Options) {
		o.Host = hostRuntime(t, "sk-test")
		o.ChatHost = host
		o.ChatHostURL = "http://127.0.0.1:1"
		o.ChatHostMaxTurns = maxTurns
		o.ChatHostRender = func(context.Context, string) (string, error) { return "You are the test assistant.", nil }
	})
	return st, src, r, host
}

// TestStoppingAChatHostTurnInterruptsIt: Stop on a turn the chat host is answering ends the
// answer there and then, and the turn is recorded as cancelled, not failed.
func TestStoppingAChatHostTurnInterruptsIt(t *testing.T) {
	st, src, r, host := startBlockingChatHost(t, 0)

	ev := inbound("C1/70.1", "a long question")
	require.NoError(t, src.Send(context.Background(), ev))
	waitFor(t, 20*time.Second, "the chat host to be answering", func() bool {
		started, _ := host.counts()
		return started == 1
	})

	assert.True(t, r.cond.CancelHostTurn(ev.Ref), "a chat-host turn is stoppable")
	waitFor(t, 20*time.Second, "the turn to be recorded as cancelled", func() bool {
		return turnStatus(st, ev.SourceKey) == store.TurnCancelled
	})
	_, interrupted := host.counts()
	assert.Equal(t, 1, interrupted, "the chat host was told to stop")
	failures := posts(src.Records(), conductor.OutFailure)
	require.NotEmpty(t, failures)
	assert.Equal(t, "Stopped.", failures[len(failures)-1].Text)
}

// TestStoppingAChatHostTurnThatIsWaitingForRoom: a turn queued behind a full chat host is
// stoppable before it starts, and never starts once a slot frees up.
func TestStoppingAChatHostTurnThatIsWaitingForRoom(t *testing.T) {
	st, src, r, host := startBlockingChatHost(t, 1)

	first := inbound("C1/71.1", "holding the only slot")
	require.NoError(t, src.Send(context.Background(), first))
	waitFor(t, 20*time.Second, "the first turn to hold the slot", func() bool {
		started, _ := host.counts()
		return started == 1
	})
	second := inbound("C2/72.1", "waiting for room")
	require.NoError(t, src.Send(context.Background(), second))
	waitFor(t, 20*time.Second, "the second turn to be waiting", func() bool {
		return turnStatus(st, second.SourceKey) == store.TurnRunning
	})

	assert.True(t, r.cond.CancelHostTurn(second.Ref), "a waiting chat-host turn is stoppable")
	waitFor(t, 20*time.Second, "the waiting turn to be recorded as cancelled", func() bool {
		return turnStatus(st, second.SourceKey) == store.TurnCancelled
	})
	var said []string
	for _, rec := range posts(src.Records(), conductor.OutFailure) {
		if rec.Ref == second.Ref {
			said = append(said, rec.Text)
		}
	}
	assert.Equal(t, []string{"Stopped before I started."}, said)

	// Freeing the slot does not start the stopped turn.
	require.True(t, r.cond.CancelHostTurn(first.Ref))
	waitFor(t, 20*time.Second, "the first turn to end", func() bool {
		return turnStatus(st, first.SourceKey) == store.TurnCancelled
	})
	time.Sleep(200 * time.Millisecond)
	started, _ := host.counts()
	assert.Equal(t, 1, started, "the stopped turn never reached the chat host")
}
