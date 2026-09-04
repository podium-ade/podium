package chat

import (
	"context"
	"sync"

	"github.com/alvaroibarguen/podium/internal/agent/store"
)

// subscriberBuffer is how many frames a subscriber may fall behind by before frames start
// being dropped. A turn produces a handful of progress lines and one answer, so this is
// several turns of slack for a browser that is briefly busy.
const subscriberBuffer = 64

// FrameKind says which member of a Frame is set. It exists so the API layer can map a
// frame onto the proto oneof without type switching on empty strings.
type FrameKind string

// The frame kinds.
const (
	// FrameMessage is a stored message. It may repeat with the same seq when a turn's
	// attachments are resolved after the message was posted; a consumer keyed on seq
	// replaces rather than appends.
	FrameMessage FrameKind = "message"
	// FrameProgress is a line a turn said on its way to an answer. It is never stored.
	FrameProgress FrameKind = "progress"
	// FrameStatus is a turn starting, finishing or failing.
	FrameStatus FrameKind = "status"
)

// Turn states a status frame carries. They are the source's three reactions in the words
// the wire uses.
const (
	StatusStarted  = "started"
	StatusFinished = "finished"
	StatusFailed   = "failed"
)

// Frame is one thing that happened in a chat.
type Frame struct {
	Kind FrameKind
	// Message is set for FrameMessage.
	Message store.ChatMessage
	// Progress is set for FrameProgress.
	Progress string
	// State and TaskID are set for FrameStatus.
	State  string
	TaskID string
}

// durable reports whether losing this frame would lose something a reload could not
// rebuild. A progress line is ephemeral by design; a message is a row and a status is what
// re-enables the composer, so dropping either has to be admitted to the subscriber.
func (f Frame) durable() bool { return f.Kind != FrameProgress }

// Subscriber is one live watcher of one chat.
//
// Frames is the stream. Resync fires when this subscriber fell behind and a durable frame
// was dropped: the consumer must re-read the chat from the last seq it saw. It is a
// separate one-slot channel on purpose — the reason a frame was dropped is that Frames was
// full, so the signal cannot travel down Frames.
type Subscriber struct {
	frames chan Frame
	resync chan struct{}

	b      *Broadcaster
	chatID string
	once   sync.Once
}

// Frames is the frame stream. It is never closed; the consumer stops on its own context.
func (s *Subscriber) Frames() <-chan Frame { return s.frames }

// Resync fires at most once per fall-behind. Re-read the chat from the last seq seen.
func (s *Subscriber) Resync() <-chan struct{} { return s.resync }

// Close unsubscribes. It is idempotent and safe to call from a defer.
func (s *Subscriber) Close() {
	s.once.Do(func() { s.b.remove(s.chatID, s) })
}

// Broadcaster fans a chat's frames out to whoever is watching it, in this process.
//
// It is deliberately in-process rather than LISTEN/NOTIFY: the conductor is one process by
// design, and the store is already the durable half of the story — every frame worth
// keeping is a row before it is broadcast. A second conductor would see the rows and miss
// the live frames, which is a reason not to run two, not a reason to build a bus.
type Broadcaster struct {
	mu   sync.Mutex
	subs map[string]map[*Subscriber]struct{}
}

// NewBroadcaster returns an empty broadcaster.
func NewBroadcaster() *Broadcaster {
	return &Broadcaster{subs: map[string]map[*Subscriber]struct{}{}}
}

// Subscribe starts watching one chat. The subscriber is removed when ctx is cancelled or
// when Close is called, whichever happens first.
func (b *Broadcaster) Subscribe(ctx context.Context, chatID string) *Subscriber {
	sub := &Subscriber{
		frames: make(chan Frame, subscriberBuffer),
		resync: make(chan struct{}, 1),
		b:      b,
		chatID: chatID,
	}
	b.mu.Lock()
	if b.subs[chatID] == nil {
		b.subs[chatID] = map[*Subscriber]struct{}{}
	}
	b.subs[chatID][sub] = struct{}{}
	b.mu.Unlock()

	go func() {
		<-ctx.Done()
		sub.Close()
	}()
	return sub
}

// Publish offers one frame to every subscriber of a chat.
//
// Every send is non-blocking: one browser that has stopped reading must not stall a turn.
// A dropped progress line is forgotten, because the next one supersedes it. A dropped
// durable frame raises the subscriber's resync signal instead, and the subscriber re-reads
// from the store — so a slow subscriber loses its place in the stream but never loses a
// message.
func (b *Broadcaster) Publish(chatID string, f Frame) {
	b.mu.Lock()
	subs := make([]*Subscriber, 0, len(b.subs[chatID]))
	for sub := range b.subs[chatID] {
		subs = append(subs, sub)
	}
	b.mu.Unlock()

	for _, sub := range subs {
		select {
		case sub.frames <- f:
		default:
			if !f.durable() {
				continue
			}
			select {
			case sub.resync <- struct{}{}:
			default:
			}
		}
	}
}

// Subscribers is how many watchers a chat has. Tests read it; nothing else does.
func (b *Broadcaster) Subscribers(chatID string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.subs[chatID])
}

func (b *Broadcaster) remove(chatID string, sub *Subscriber) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.subs[chatID], sub)
	if len(b.subs[chatID]) == 0 {
		delete(b.subs, chatID)
	}
}
