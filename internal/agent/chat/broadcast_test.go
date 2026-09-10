package chat

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/agent/store"
)

func msgFrame(seq uint64) Frame {
	return Frame{Kind: FrameMessage, Message: store.ChatMessage{ChatID: "chat_1", Seq: seq, Role: store.RoleAssistant}}
}

// recv reads one frame, or fails the test rather than hanging the suite.
func recv(t *testing.T, sub *Subscriber) Frame {
	t.Helper()
	select {
	case f := <-sub.Frames():
		return f
	case <-time.After(2 * time.Second):
		t.Fatal("no frame arrived")
		return Frame{}
	}
}

func TestEverySubscriberOfAChatSeesEveryFrame(t *testing.T) {
	b := NewBroadcaster()
	ctx := context.Background()
	one := b.Subscribe(ctx, "chat_1")
	two := b.Subscribe(ctx, "chat_1")
	other := b.Subscribe(ctx, "chat_2")
	defer one.Close()
	defer two.Close()
	defer other.Close()

	b.Publish("chat_1", Frame{Kind: FrameProgress, Progress: "reading the schema"})
	b.Publish("chat_1", msgFrame(1))

	for _, sub := range []*Subscriber{one, two} {
		assert.Equal(t, "reading the schema", recv(t, sub).Progress)
		assert.Equal(t, uint64(1), recv(t, sub).Message.Seq)
	}
	// A frame is delivered to the chat it belongs to and to no other.
	assert.Empty(t, other.Frames())
}

func TestASlowSubscriberLosesProgressSilentlyAndIsToldAboutAMessage(t *testing.T) {
	b := NewBroadcaster()
	sub := b.Subscribe(context.Background(), "chat_1")
	defer sub.Close()

	// Fill the buffer without reading any of it.
	for i := range subscriberBuffer {
		b.Publish("chat_1", msgFrame(uint64(i+1)))
	}
	require.Len(t, sub.Frames(), subscriberBuffer)
	require.Empty(t, sub.Resync(), "nothing has been dropped yet")

	// Progress on a full subscriber is dropped and forgotten: the next line supersedes it
	// anyway, and asking a browser to re-read for a thought is noise.
	b.Publish("chat_1", Frame{Kind: FrameProgress, Progress: "still working"})
	assert.Empty(t, sub.Resync(), "a dropped progress line is not worth a resync")

	// A dropped message is not forgotten. The frame is gone, but the subscriber is told to
	// re-read from its last seq, so the message itself cannot be lost.
	b.Publish("chat_1", msgFrame(999))
	select {
	case <-sub.Resync():
	case <-time.After(2 * time.Second):
		t.Fatal("a dropped message must raise a resync")
	}

	// The frames already queued are intact: falling behind costs a place in the stream,
	// not the stream.
	assert.Equal(t, uint64(1), recv(t, sub).Message.Seq)
	assert.Len(t, sub.Frames(), subscriberBuffer-1)
}

func TestTheResyncSignalIsRaisedOnceUntilItIsRead(t *testing.T) {
	b := NewBroadcaster()
	sub := b.Subscribe(context.Background(), "chat_1")
	defer sub.Close()
	for i := range subscriberBuffer + 5 {
		b.Publish("chat_1", msgFrame(uint64(i+1)))
	}
	// Five drops, one signal: the client re-reads once and catches up on all of them.
	assert.Len(t, sub.Resync(), 1)
}

func TestASubscriptionEndsWithItsContext(t *testing.T) {
	b := NewBroadcaster()
	ctx, cancel := context.WithCancel(context.Background())
	sub := b.Subscribe(ctx, "chat_1")
	require.Equal(t, 1, b.Subscribers("chat_1"))

	cancel()
	assert.Eventually(t, func() bool { return b.Subscribers("chat_1") == 0 },
		2*time.Second, 10*time.Millisecond, "cancelling the context must unsubscribe")

	// Publishing to a chat nobody watches is not an error.
	b.Publish("chat_1", msgFrame(1))
	sub.Close()
	sub.Close()
}

func TestCloseUnsubscribesAndForgetsTheChat(t *testing.T) {
	b := NewBroadcaster()
	sub := b.Subscribe(context.Background(), "chat_1")
	require.Equal(t, 1, b.Subscribers("chat_1"))
	sub.Close()
	assert.Equal(t, 0, b.Subscribers("chat_1"))
	// The map entry goes too, so a process that has served a million chats holds none.
	b.mu.Lock()
	defer b.mu.Unlock()
	assert.Empty(t, b.subs)
}
