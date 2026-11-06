package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"unicode/utf8"

	"github.com/podium-ade/podium/internal/agent/store"
	"github.com/podium-ade/podium/internal/ids"
)

// A Relay carries a chat's live frames between conductors that share one database
// (docs/agent.md, "Live updates across conductors").
//
// Every frame worth keeping is already a row when it is published, so what crosses the wire
// is a pointer, not the frame: "chat X has message 12". The conductor that hears it re-reads
// the row and publishes it to its own subscribers. That keeps notifications small, makes an
// edited row (attachments resolved later, an edit synced from Slack) arrive as it now is, and
// means a duplicate notification is harmless — subscribers already key messages by seq.
//
// The two frames that are not rows travel in full: a turn's status, and a progress line,
// which is cut to fit the NOTIFY limit. A conductor ignores what it sent itself: its own
// subscribers already have it.

// Bus is the part of the store a relay needs.
type Bus interface {
	Notify(ctx context.Context, channel, payload string) error
	Listen(ctx context.Context, channel string, connected func(), fn func(payload string), failed func(error)) error
}

// RelayStore is what a relay re-reads a frame from.
type RelayStore interface {
	GetChat(ctx context.Context, id string) (store.Chat, error)
	GetChatMessage(ctx context.Context, chatID string, seq uint64) (store.ChatMessage, error)
	ListChatPullRequests(ctx context.Context, chatID string) ([]store.ChatPullRequest, error)
	ListQueuedTurnsForChat(ctx context.Context, chatID string) ([]store.QueuedTurn, error)
}

// relayQueue is how many frames may wait to be announced. Publish never blocks on the
// database; a burst beyond this is dropped from the relay (never from this process's own
// subscribers), and the other conductors' watchers recover on their next re-read.
const relayQueue = 1024

// progressRunes caps a relayed progress line, well inside store.MaxNotifyPayload once the
// envelope and multi-byte runes are counted.
const progressRunes = 1500

// wire is one announcement.
type wire struct {
	Origin string    `json:"o"`
	Chat   string    `json:"c"`
	Kind   FrameKind `json:"k"`
	Seq    uint64    `json:"s,omitempty"`
	State  string    `json:"st,omitempty"`
	TaskID string    `json:"t,omitempty"`
	Text   string    `json:"p,omitempty"`
}

// Relay connects one Broadcaster to the others through the database.
type Relay struct {
	b      *Broadcaster
	bus    Bus
	store  RelayStore
	logger *slog.Logger
	origin string
	queue  chan wire
}

// NewRelay wires b to the bus. Nothing is sent or heard until Run.
func NewRelay(b *Broadcaster, bus Bus, st RelayStore, logger *slog.Logger) *Relay {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	r := &Relay{
		b: b, bus: bus, store: st, logger: logger,
		origin: ids.New("cond"),
		queue:  make(chan wire, relayQueue),
	}
	b.setRelay(r.offer)
	return r
}

// offer queues a frame for announcement. It never blocks.
func (r *Relay) offer(chatID string, f Frame) {
	w := wire{Origin: r.origin, Chat: chatID, Kind: f.Kind}
	switch f.Kind {
	case FrameMessage:
		w.Seq = f.Message.Seq
	case FrameStatus:
		w.State, w.TaskID = f.State, f.TaskID
	case FrameProgress:
		w.Text, w.TaskID = cutRunes(f.Progress, progressRunes), f.TaskID
	case FrameChat, FramePullRequests, FrameQueue:
	default:
		return
	}
	select {
	case r.queue <- w:
	default:
		r.logger.Warn("chat relay queue is full; another conductor's watchers will catch up on their next re-read",
			"chat_id", chatID, "kind", string(f.Kind))
	}
}

// Run announces this conductor's frames and publishes everyone else's, until ctx ends.
func (r *Relay) Run(ctx context.Context) error {
	go r.send(ctx)
	var connectedBefore atomic.Bool
	return r.bus.Listen(ctx, store.ChatChannel, func() {
		// The first connection missed nothing. A reconnect may have: every watcher here
		// re-reads, because nobody can say which chats were spoken in meanwhile.
		if connectedBefore.Swap(true) {
			r.b.ResyncAll()
		}
	}, func(payload string) {
		r.hear(ctx, payload)
	}, func(err error) {
		r.logger.WarnContext(ctx, "the chat relay lost its connection; reconnecting", "error", err)
	})
}

func (r *Relay) send(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case w := <-r.queue:
			raw, err := encodeWire(w)
			if err != nil {
				r.logger.WarnContext(ctx, "a chat frame does not fit a NOTIFY; other conductors will catch up on their next re-read",
					"chat_id", w.Chat, "kind", string(w.Kind), "error", err)
				continue
			}
			if err := r.bus.Notify(ctx, store.ChatChannel, string(raw)); err != nil && ctx.Err() == nil {
				r.logger.WarnContext(ctx, "announcing a chat frame to other conductors failed",
					"chat_id", w.Chat, "kind", string(w.Kind), "error", err)
			}
		}
	}
}

// hear rebuilds an announced frame and publishes it to this process's subscribers.
func (r *Relay) hear(ctx context.Context, payload string) {
	var w wire
	if err := json.Unmarshal([]byte(payload), &w); err != nil || w.Chat == "" {
		return
	}
	if w.Origin == r.origin || !r.b.watched(w.Chat) {
		return
	}
	f, err := r.rebuild(ctx, w)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			r.logger.WarnContext(ctx, "re-reading an announced chat frame failed",
				"chat_id", w.Chat, "kind", string(w.Kind), "error", err)
		}
		return
	}
	r.b.publishLocal(w.Chat, f)
}

func (r *Relay) rebuild(ctx context.Context, w wire) (Frame, error) {
	switch w.Kind {
	case FrameMessage:
		msg, err := r.store.GetChatMessage(ctx, w.Chat, w.Seq)
		if err != nil {
			return Frame{}, err
		}
		return Frame{Kind: FrameMessage, Message: msg}, nil
	case FrameChat:
		chat, err := r.store.GetChat(ctx, w.Chat)
		if errors.Is(err, store.ErrNotFound) {
			// Deleted on the conductor that announced it. The watchers here have to hear
			// that too, or their streams outlive the chat.
			return Frame{Kind: FrameChat, Chat: Tombstone(w.Chat)}, nil
		}
		if err != nil {
			return Frame{}, err
		}
		return Frame{Kind: FrameChat, Chat: chat}, nil
	case FramePullRequests:
		prs, err := r.store.ListChatPullRequests(ctx, w.Chat)
		if err != nil {
			return Frame{}, err
		}
		return Frame{Kind: FramePullRequests, PullRequests: prs}, nil
	case FrameQueue:
		q, err := r.store.ListQueuedTurnsForChat(ctx, w.Chat)
		if err != nil {
			return Frame{}, err
		}
		return Frame{Kind: FrameQueue, Queue: q}, nil
	case FrameStatus:
		return Frame{Kind: FrameStatus, State: w.State, TaskID: w.TaskID}, nil
	case FrameProgress:
		return Frame{Kind: FrameProgress, Progress: w.Text, TaskID: w.TaskID}, nil
	}
	return Frame{}, store.ErrNotFound
}

// encodeWire is w as a NOTIFY payload, never over store.MaxNotifyPayload. The rune cap on a
// progress line is not enough on its own: JSON escapes <, >, & and control characters as six
// bytes each, so a line of 1500 angle brackets is about 9 KB once encoded. A payload over the
// limit has its progress text halved until it fits; only a frame that cannot fit even with no
// text is an error.
func encodeWire(w wire) ([]byte, error) {
	for {
		raw, err := json.Marshal(w)
		if err != nil {
			return nil, err
		}
		if len(raw) <= store.MaxNotifyPayload {
			return raw, nil
		}
		if w.Text == "" {
			return nil, fmt.Errorf("a %d-byte frame is over the %d-byte NOTIFY limit", len(raw), store.MaxNotifyPayload)
		}
		// cutRunes keeps an ellipsis, so a line already down to a rune or two goes outright,
		// or this would never end.
		if n := utf8.RuneCountInString(w.Text); n <= 2 {
			w.Text = ""
		} else {
			w.Text = cutRunes(w.Text, n/2)
		}
	}
}

// cutRunes is s cut to at most n runes, on a rune boundary.
func cutRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n]) + "…"
}
