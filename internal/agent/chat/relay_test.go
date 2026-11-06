package chat

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/agent/store"
)

// memBus is NOTIFY/LISTEN in memory: every Notify reaches every listener, its own sender's
// included, which is exactly what Postgres does.
type memBus struct {
	mu        sync.Mutex
	listeners []func(string)
	connected []func()
}

func (b *memBus) Notify(_ context.Context, _, payload string) error {
	b.mu.Lock()
	ls := append([]func(string){}, b.listeners...)
	b.mu.Unlock()
	for _, l := range ls {
		l(payload)
	}
	return nil
}

func (b *memBus) Listen(ctx context.Context, _ string, connected func(), fn func(string), _ func(error)) error {
	b.mu.Lock()
	b.listeners = append(b.listeners, fn)
	b.connected = append(b.connected, connected)
	b.mu.Unlock()
	connected()
	<-ctx.Done()
	return nil
}

// reconnect is every listener's LISTEN coming back after a drop.
func (b *memBus) reconnect() {
	b.mu.Lock()
	cs := append([]func(){}, b.connected...)
	b.mu.Unlock()
	for _, c := range cs {
		c()
	}
}

type relayRows struct {
	msgs map[uint64]store.ChatMessage
	gone map[string]bool
}

func (r relayRows) GetChat(_ context.Context, id string) (store.Chat, error) {
	if r.gone[id] {
		return store.Chat{}, store.ErrNotFound
	}
	return store.Chat{ID: id, Title: "re-read"}, nil
}

func (r relayRows) GetChatMessage(_ context.Context, _ string, seq uint64) (store.ChatMessage, error) {
	m, ok := r.msgs[seq]
	if !ok {
		return store.ChatMessage{}, store.ErrNotFound
	}
	return m, nil
}

func (r relayRows) ListChatPullRequests(context.Context, string) ([]store.ChatPullRequest, error) {
	return nil, nil
}

func (r relayRows) ListQueuedTurnsForChat(context.Context, string) ([]store.QueuedTurn, error) {
	return []store.QueuedTurn{{Login: "bob", Author: "bob", SourceKind: "chat", MessageSeq: 7}}, nil
}

func relayPair(t *testing.T, rows relayRows) (a, b *Broadcaster, bus *memBus) {
	t.Helper()
	bus = &memBus{}
	a, b = NewBroadcaster(), NewBroadcaster()
	ra := NewRelay(a, bus, rows, nil)
	rb := NewRelay(b, bus, rows, nil)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = ra.Run(ctx) }()
	go func() { _ = rb.Run(ctx) }()
	require.Eventually(t, func() bool {
		bus.mu.Lock()
		defer bus.mu.Unlock()
		return len(bus.listeners) == 2
	}, 5*time.Second, 10*time.Millisecond)
	return a, b, bus
}

func next(t *testing.T, sub *Subscriber) Frame {
	t.Helper()
	select {
	case f := <-sub.Frames():
		return f
	case <-time.After(5 * time.Second):
		t.Fatal("no frame")
		return Frame{}
	}
}

func none(t *testing.T, sub *Subscriber) {
	t.Helper()
	select {
	case f := <-sub.Frames():
		t.Fatalf("unexpected frame %+v", f)
	case <-time.After(100 * time.Millisecond):
	}
}

// A message crosses as a pointer and arrives as the row now stands; a status crosses whole;
// the sender's own watchers see each frame exactly once.
func TestRelayCarriesFramesToTheOtherConductor(t *testing.T) {
	rows := relayRows{msgs: map[uint64]store.ChatMessage{3: {ChatID: "c1", Seq: 3, Text: "as stored"}}}
	a, b, _ := relayPair(t, rows)
	ctx := context.Background()
	onA := a.Subscribe(ctx, "c1")
	onB := b.Subscribe(ctx, "c1")

	a.Publish("c1", Frame{Kind: FrameMessage, Message: store.ChatMessage{ChatID: "c1", Seq: 3, Text: "as published"}})
	assert.Equal(t, "as published", next(t, onA).Message.Text)
	assert.Equal(t, "as stored", next(t, onB).Message.Text, "B re-reads the row")
	none(t, onA)

	a.Publish("c1", Frame{Kind: FrameStatus, State: StatusFinished, TaskID: "task_1"})
	next(t, onA)
	got := next(t, onB)
	assert.Equal(t, StatusFinished, got.State)
	assert.Equal(t, "task_1", got.TaskID)

	a.Publish("c1", Frame{Kind: FrameChat})
	next(t, onA)
	assert.Equal(t, "re-read", next(t, onB).Chat.Title)

	// A queue crosses as a pointer too: B re-reads the turns waiting.
	a.Publish("c1", Frame{Kind: FrameQueue})
	next(t, onA)
	q := next(t, onB)
	assert.Equal(t, FrameQueue, q.Kind)
	require.Len(t, q.Queue, 1)
	assert.Equal(t, "bob", q.Queue[0].Login)
}

// A progress line crosses whole, cut to fit a NOTIFY.
func TestRelayCutsALongProgressLine(t *testing.T) {
	a, b, _ := relayPair(t, relayRows{})
	onB := b.Subscribe(context.Background(), "c1")
	long := strings.Repeat("é", progressRunes+50)
	a.Publish("c1", Frame{Kind: FrameProgress, Progress: long})
	got := next(t, onB)
	assert.Equal(t, progressRunes+1, len([]rune(got.Progress)), "cut on a rune boundary, with an ellipsis")
}

// A chat another conductor deleted reaches B's watchers as a tombstone, so their streams end
// rather than wait for a frame that never comes.
func TestRelayCarriesADeletedChatAsATombstone(t *testing.T) {
	a, b, _ := relayPair(t, relayRows{gone: map[string]bool{"c1": true}})
	onB := b.Subscribe(context.Background(), "c1")
	a.Publish("c1", Frame{Kind: FrameChat})
	got := next(t, onB)
	assert.Equal(t, FrameChat, got.Kind)
	assert.Equal(t, "c1", got.Chat.ID)
	assert.NotNil(t, got.Chat.DeletedAt, "a chat that is gone is shown as deleted")
}

// JSON escapes <, > and & as six bytes each, so a progress line within the rune cap can still
// be over NOTIFY's limit once encoded. It is cut until it fits, and it still crosses.
func TestRelayFitsAnEscapeHeavyProgressLineIntoANotify(t *testing.T) {
	w := wire{Origin: "cond_x", Chat: "c1", Kind: FrameProgress, Text: cutRunes(strings.Repeat("<", 3000), progressRunes)}
	raw, err := encodeWire(w)
	require.NoError(t, err)
	assert.LessOrEqual(t, len(raw), store.MaxNotifyPayload)
	assert.Contains(t, string(raw), `\u003c`, "still a progress line")

	short, err := encodeWire(wire{Origin: "cond_x", Chat: "c1", Kind: FrameProgress, Text: "\x00\x01"})
	require.NoError(t, err)
	assert.LessOrEqual(t, len(short), store.MaxNotifyPayload)
}

// A chat nobody on B watches is not re-read on B; a message B cannot find is dropped.
func TestRelaySkipsWhatNobodyWatches(t *testing.T) {
	a, b, _ := relayPair(t, relayRows{msgs: map[uint64]store.ChatMessage{}})
	onB := b.Subscribe(context.Background(), "c2")
	a.Publish("c1", Frame{Kind: FrameMessage, Message: store.ChatMessage{ChatID: "c1", Seq: 1}})
	a.Publish("c2", Frame{Kind: FrameMessage, Message: store.ChatMessage{ChatID: "c2", Seq: 9}})
	none(t, onB)
}

// After a reconnect, what was announced meanwhile is unknowable: every watcher re-reads.
func TestRelayResyncsWatchersAfterAReconnect(t *testing.T) {
	_, b, bus := relayPair(t, relayRows{})
	onB := b.Subscribe(context.Background(), "c1")
	bus.reconnect()
	select {
	case <-onB.Resync():
	case <-time.After(5 * time.Second):
		t.Fatal("a reconnect must ask watchers to re-read")
	}
}
