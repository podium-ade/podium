package conductor

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/agent/store"
)

// A Slack thread is a CONVERSATION now: the assistant answers it on this host and delegates
// the work, which is what lets a mention reach a playbook that a mention could never route
// to. Before this it was a task running whichever playbook the channel mapped to, and the
// only reachable one was the default.
func TestAConversationIsAnsweredHereForChatAndSlack(t *testing.T) {
	assert.True(t, aConversation(SourceChat))
	assert.True(t, aConversation(SourceSlack))
	assert.True(t, aConversation(KindDev), "the dev source is how both are tested")
	assert.False(t, aConversation("linear"), "a ticket is one piece of work, not a conversation")
}

// The runtime's schema allows three source kinds, and a DELEGATED task used to be told it
// came from the web chat whatever asked for it. Harmless while only chats could delegate;
// wrong the moment a Slack thread could, because the prompt names where the answer is going.
func TestBriefKindForNamesTheRealSource(t *testing.T) {
	assert.Equal(t, SourceSlack, briefKindFor(SourceSlack))
	assert.Equal(t, "linear", briefKindFor("linear"))
	assert.Equal(t, SourceChat, briefKindFor(SourceChat))
	assert.Equal(t, SourceChat, briefKindFor(KindDev), "the test-only source presents as chat")
}

// The cap is on this HOST, because a host turn is a process on it. Slack decides how many
// conversations there are, so the number of them running at once cannot.
func TestHostSlotsQueueBeyondTheCap(t *testing.T) {
	c := &Conductor{
		logger:    slog.New(slog.DiscardHandler),
		metrics:   NewMetrics(nil),
		hostSlots: make(chan struct{}, 2),
	}
	ctx := t.Context()

	first, ok := c.acquireHostSlot(ctx)
	require.True(t, ok)
	second, ok := c.acquireHostSlot(ctx)
	require.True(t, ok)

	// The third waits, and is let through the moment a slot comes back.
	got := make(chan struct{})
	go func() {
		third, ok := c.acquireHostSlot(ctx)
		if ok {
			third()
			close(got)
		}
	}()
	select {
	case <-got:
		t.Fatal("a third turn ran while both slots were held")
	case <-time.After(50 * time.Millisecond):
	}
	first()
	select {
	case <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("the queued turn never got the slot that was given back")
	}
	second()
}

// A conductor that is stopping does not leave a queued turn waiting forever, and hands back
// nothing to release.
func TestHostSlotsGiveUpWhenTheConductorStops(t *testing.T) {
	c := &Conductor{
		logger:    slog.New(slog.DiscardHandler),
		metrics:   NewMetrics(nil),
		hostSlots: make(chan struct{}, 1),
	}
	held, ok := c.acquireHostSlot(context.Background())
	require.True(t, ok)
	defer held()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	release, ok := c.acquireHostSlot(ctx)
	assert.False(t, ok)
	assert.NotNil(t, release, "the caller may still defer it")
	release()
}

// Every way a turn can end, and the mark it leaves. ✅ only for a turn that worked; ❌ for
// every other ending, cancellation included.
func TestReactionForEveryEnding(t *testing.T) {
	for status, want := range map[string]Reaction{
		store.TurnSucceeded: ReactionDone,
		store.TurnFailed:    ReactionFailed,
		store.TurnLost:      ReactionFailed,
		store.TurnCancelled: ReactionFailed,
		store.TurnTimeout:   ReactionFailed,
	} {
		assert.Equal(t, want, reactionFor(status), status)
	}
}
