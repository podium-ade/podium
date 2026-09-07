// Package chat is the web chat: the third conductor.Source, and the only one whose
// conversation Podium itself holds.
//
// Slack has threads and Linear has issues; a browser has nothing, so the chats and
// chat_messages tables ARE the conversation. What a turn is handed as history, what a
// reload renders and what the chat list shows all come out of them. Progress is the one
// exception: it is fanned out to whoever is watching and never stored, because a
// half-finished thought is not a record.
//
// Everything in a chat is content. A human wrote the user messages and a task wrote the
// assistant ones; this package stores and relays both and interprets neither.
package chat

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/alvaroibarguen/podium/internal/agent/conductor"
	"github.com/alvaroibarguen/podium/internal/agent/profiles"
	"github.com/alvaroibarguen/podium/internal/agent/store"
)

// ErrTurnRunning is what Send returns while a turn of that chat is still in flight. The UI
// disables the composer; this is the guarantee behind it.
var ErrTurnRunning = errors.New("a turn is already running for this chat")

// eventBuffer is how many inbound messages may be waiting for the turn loop. One human
// typing cannot outrun this, and a full channel makes Send block rather than lose a
// message.
const eventBuffer = 32

// Store is the part of the conductor's database this source reads and writes. It is an
// interface so the source is testable without Postgres, and so it is visible exactly which
// operations the chat needs — a source touching the store at all is a departure from
// the Slack and Linear sources, and it is the point: there is nowhere else for a web
// conversation to live.
type Store interface {
	GetChat(ctx context.Context, id string) (store.Chat, error)
	AppendChatMessage(ctx context.Context, msg store.ChatMessage) (store.ChatMessage, error)
	ListChatMessages(ctx context.Context, chatID string, fromSeq uint64) ([]store.ChatMessage, error)
	AttachToLastAssistantMessage(
		ctx context.Context, chatID string, file store.ChatAttachment,
	) (store.ChatMessage, error)
	ChatTurnRunning(ctx context.Context, chatID string) (bool, error)
	SetChatPlaybook(ctx context.Context, id, playbook string) (store.Chat, error)
	SetChatTitle(ctx context.Context, id, title string) (store.Chat, error)
	LinkChatPullRequest(ctx context.Context, pr store.ChatPullRequest) (bool, error)
	AttachChatPullRequest(ctx context.Context, pr store.ChatPullRequest) (store.ChatPullRequest, error)
	DetachChatPullRequest(ctx context.Context, chatID, url string) error
	ListChatPullRequests(ctx context.Context, chatID string) ([]store.ChatPullRequest, error)
}

// Options is what a Source needs.
type Options struct {
	Store Store
	// DisplayName is the bot's name, used as the author of its own transcript entries.
	DisplayName string
	// UIURL is the web UI as a HUMAN reaches it. It becomes the deep link in the brief and
	// in a retained memory's provenance; a chat memory with no URL is a chip that goes
	// nowhere.
	UIURL string
	// DefaultPlaybook is the profile's chat_default_playbook (falling back to default_playbook),
	// carried on every event as the source's default. It is injected rather than read from a
	// profile here because a source knows nothing about profiles — and it matters: without
	// it the chat would silently run default_playbook, and chat_default_playbook would be a UI
	// hint rather than the profile decision it is meant to be.
	//
	// It is a function rather than a string because the profile decision can change while
	// this process runs: an operator setting the chat default in the web UI must reach the
	// next message, not the next restart.
	DefaultPlaybook func() string
	Logger          *slog.Logger
}

// SendRequest is one human message arriving from the browser.
type SendRequest struct {
	ChatID string
	// Login is the caller, as podium-server asserted it. It owns the chat and authors the
	// message.
	Login string
	Text  string
	// Playbook is the playbook chip's choice, which wins outright: a human picking a chip after
	// typing is expressing the later intent. Empty leaves the choice to a leading /playbook in
	// Text, and then to the profile's chat default.
	Playbook string
	// Override is the composer's model picker: what THIS message runs on, whatever the
	// playbook's own default is. Empty everywhere means the playbook decides.
	Override profiles.Override
}

// live is what this process knows about a chat that the database does not know yet.
type live struct {
	// busy is set the instant a message is accepted and cleared when the turn's outcome
	// lands. It closes the window between Send returning and the conductor writing the
	// turn row: without it two sends milliseconds apart would both be accepted and the
	// second would be queued rather than refused.
	busy bool
	// spoke records whether anything durable has been stored since this turn started, so a
	// failure with no words of its own still leaves something behind for a reload.
	spoke bool
}

// Source is the chat as the conductor sees it: a conductor.Source like any other.
type Source struct {
	store    Store
	bcast    *Broadcaster
	name     string
	uiURL    string
	playbook func() string
	logger   *slog.Logger
	events   chan conductor.InboundEvent

	mu   sync.Mutex
	live map[string]*live
}

var _ conductor.Source = (*Source)(nil)

// New returns a chat source. It has no loop of its own to run: a browser calling
// SendChatMessage is what delivers an event.
func New(opts Options) (*Source, error) {
	if opts.Store == nil {
		return nil, errors.New("chat: a store is required")
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	playbook := opts.DefaultPlaybook
	if playbook == nil {
		playbook = func() string { return "" }
	}
	return &Source{
		store:    opts.Store,
		bcast:    NewBroadcaster(),
		name:     opts.DisplayName,
		uiURL:    strings.TrimSuffix(opts.UIURL, "/"),
		playbook: playbook,
		logger:   logger,
		events:   make(chan conductor.InboundEvent, eventBuffer),
		live:     map[string]*live{},
	}, nil
}

// Kind implements conductor.Source. It is "chat", not "dev": the two share the brief's
// source.kind and nothing else, and a turn only retains a memory when the kind is a real
// one (step 19).
func (s *Source) Kind() string { return conductor.SourceChat }

// Events implements conductor.Source.
func (s *Source) Events() <-chan conductor.InboundEvent { return s.events }

// Subscribe starts watching one chat's live frames.
func (s *Source) Subscribe(ctx context.Context, chatID string) *Subscriber {
	return s.bcast.Subscribe(ctx, chatID)
}

// Subscribers is how many browsers are watching one chat. Tests read it; nothing else does.
func (s *Source) Subscribers(chatID string) int { return s.bcast.Subscribers(chatID) }

// URL is the deep link to one chat, as a human reaches it. Empty when no UI URL is known.
func (s *Source) URL(chatID string) string {
	if s.uiURL == "" {
		return ""
	}
	return s.uiURL + "/agent/chat/" + chatID
}

// Send stores one human message and starts a turn on it.
//
// The order matters: the running check and the busy flag are taken together, then the row
// is written, then the frame goes out, then the event. A caller that gets ErrTurnRunning
// has changed nothing.
func (s *Source) Send(ctx context.Context, req SendRequest) (store.ChatMessage, error) {
	if req.ChatID == "" {
		return store.ChatMessage{}, errors.New("send: a chat id is required")
	}
	if strings.TrimSpace(req.Text) == "" {
		return store.ChatMessage{}, errors.New("send: a message needs some text")
	}
	chat, err := s.store.GetChat(ctx, req.ChatID)
	if err != nil {
		return store.ChatMessage{}, err
	}
	if chat.Login != req.Login {
		// Reported as "no such chat" rather than "not yours": the existence of another
		// login's chat is itself something this caller has no business learning.
		return store.ChatMessage{}, fmt.Errorf("%w: chat %s", store.ErrNotFound, req.ChatID)
	}
	if err := s.claim(ctx, req.ChatID); err != nil {
		return store.ChatMessage{}, err
	}

	msg, err := s.store.AppendChatMessage(ctx, store.ChatMessage{
		ChatID: req.ChatID,
		Role:   store.RoleUser,
		Text:   req.Text,
		TS:     time.Now().UTC(),
	})
	if err != nil {
		s.release(req.ChatID)
		return store.ChatMessage{}, err
	}
	s.bcast.Publish(req.ChatID, Frame{Kind: FrameMessage, Message: msg})
	s.remember(ctx, chat, req)

	// The chip is knowledge and the profile's chat default is only a fallback, so they
	// travel as different fields: a /playbook the human typed loses to the chip and beats the
	// default.
	ev := conductor.InboundEvent{
		SourceKind:      conductor.SourceChat,
		SourceKey:       store.ChatSourceKey(req.ChatID),
		Ref:             req.ChatID,
		Author:          req.Login,
		Text:            req.Text,
		TS:              msg.TS,
		URL:             s.URL(req.ChatID),
		Playbook:        req.Playbook,
		DefaultPlaybook: s.playbook(),
		Override:        req.Override,
		BriefKind:       conductor.SourceChat,
	}
	select {
	case s.events <- ev:
	case <-ctx.Done():
		// The message is stored and will be in the next turn's transcript, but nothing is
		// going to answer it, so the composer must not stay disabled.
		s.release(req.ChatID)
		return store.ChatMessage{}, fmt.Errorf("send to chat %s: %w", req.ChatID, ctx.Err())
	}
	return msg, nil
}

// remember records the playbook this chat started with and names it from the first query.
// Both writes are first-wins: a later message cannot change either, and a title the caller
// supplied at create is left alone.
func (s *Source) remember(ctx context.Context, chat store.Chat, req SendRequest) {
	playbook := req.Playbook
	if playbook == "" {
		if m := profiles.PlaybookPrefixRE.FindStringSubmatch(req.Text); m != nil {
			playbook = m[1]
		} else {
			playbook = s.playbook()
		}
	}
	if chat.Playbook == "" && playbook != "" {
		updated, err := s.store.SetChatPlaybook(ctx, req.ChatID, playbook)
		if err != nil {
			s.logger.WarnContext(ctx, "recording the chat's playbook failed",
				"chat_id", req.ChatID, "playbook", playbook, "error", err)
		} else {
			chat = updated
		}
	}
	if chat.AutoTitle && chat.Title == store.DefaultChatTitle {
		if title := TitleFromQuery(req.Text); title != "" {
			updated, err := s.store.SetChatTitle(ctx, req.ChatID, title)
			if err != nil {
				s.logger.WarnContext(ctx, "naming the chat from its first query failed",
					"chat_id", req.ChatID, "error", err)
			} else {
				chat = updated
			}
		}
	}
	s.bcast.Publish(req.ChatID, Frame{Kind: FrameChat, Chat: chat})
}

// SetAutoTitle is the model-written name of a chat, applied only while AutoTitle is still
// true. The first turn writes ChatTitleArtifact; the conductor reads it and calls this.
func (s *Source) SetAutoTitle(ctx context.Context, ref, title string) error {
	title = SanitizeTitle(title)
	if title == "" {
		return nil
	}
	chat, err := s.store.SetChatTitle(ctx, ref, title)
	if err != nil {
		return err
	}
	s.bcast.Publish(ref, Frame{Kind: FrameChat, Chat: chat})
	return nil
}

// Running reports whether a turn of this chat is in flight, from both halves of the story:
// what this process is about to do and what the database says was left running by a process
// that died.
func (s *Source) Running(ctx context.Context, chatID string) (bool, error) {
	s.mu.Lock()
	busy := s.live[chatID] != nil && s.live[chatID].busy
	s.mu.Unlock()
	if busy {
		return true, nil
	}
	return s.store.ChatTurnRunning(ctx, chatID)
}

// claim takes the chat's one turn slot, or refuses with ErrTurnRunning.
func (s *Source) claim(ctx context.Context, chatID string) error {
	running, err := s.store.ChatTurnRunning(ctx, chatID)
	if err != nil {
		return err
	}
	if running {
		return ErrTurnRunning
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.live[chatID]
	if st == nil {
		st = &live{}
		s.live[chatID] = st
	}
	if st.busy {
		return ErrTurnRunning
	}
	st.busy = true
	st.spoke = false
	return nil
}

// release gives the slot back. Called on every way a turn can end, including the ways that
// never reach a task.
func (s *Source) release(chatID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if st := s.live[chatID]; st != nil {
		st.busy = false
	}
}

// noteSpoke records that something durable was stored for the turn in flight.
func (s *Source) noteSpoke(chatID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if st := s.live[chatID]; st != nil {
		st.spoke = true
	}
}

// tookSpoke reports whether anything durable was stored for this turn.
func (s *Source) tookSpoke(chatID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.live[chatID] != nil && s.live[chatID].spoke
}

// FetchTranscript implements conductor.Source: the whole conversation, oldest first. The
// brief's 96 KiB cap is what truncates a long one, oldest entry first (step 17).
func (s *Source) FetchTranscript(ctx context.Context, ref string) ([]conductor.BriefEntry, error) {
	chat, err := s.store.GetChat(ctx, ref)
	if err != nil {
		return nil, err
	}
	msgs, err := s.store.ListChatMessages(ctx, ref, 0)
	if err != nil {
		return nil, err
	}
	out := make([]conductor.BriefEntry, 0, len(msgs))
	for _, m := range msgs {
		author := chat.Login
		if m.Role == store.RoleAssistant {
			author = s.name
		}
		out = append(out, conductor.BriefEntry{
			Role:   m.Role,
			Author: author,
			TS:     conductor.BriefTimestamp(m.TS),
			Text:   m.Text,
		})
	}
	return out, nil
}

// Post implements conductor.Source.
//
// A final or a failure is a row: it is the answer, and it has to survive a reload. Progress
// is not: it is pushed to whoever is watching and forgotten, which is why a reloaded chat
// shows the answer and no trail of thinking.
func (s *Source) Post(ctx context.Context, ref string, out conductor.Outbound) (string, error) {
	if out.Type == conductor.OutProgress {
		s.bcast.Publish(ref, Frame{Kind: FrameProgress, Progress: out.Text, TaskID: out.TaskID})
		// A progress message has no id, but the turn loop edits whatever Post returned, so
		// it needs something non-empty to hold on to.
		return progressMessageID, nil
	}
	msg, err := s.store.AppendChatMessage(ctx, store.ChatMessage{
		ChatID: ref,
		Role:   store.RoleAssistant,
		Text:   out.Text,
		TS:     time.Now().UTC(),
	})
	if err != nil {
		return "", err
	}
	s.noteSpoke(ref)
	if out.Type == conductor.OutFailure {
		// The conductor posts its own plain-words failure before it reacts, and this is
		// it. Releasing the slot here means a chat whose turn died before the turn row was
		// finished is usable again immediately; the store's own check still refuses a send
		// while that row says running.
		s.release(ref)
	}
	s.bcast.Publish(ref, Frame{Kind: FrameMessage, Message: msg})
	return strconv.FormatUint(msg.Seq, 10), nil
}

// progressMessageID is the id Post returns for a progress line. Progress is not stored, so
// there is nothing to address; Edit publishes a new frame whatever it is given.
const progressMessageID = "progress"

// Edit implements conductor.Source. The turn loop edits its placeholder to show the newest
// progress; here that is simply another progress frame, because nothing kept the old one.
func (s *Source) Edit(_ context.Context, ref, _ string, out conductor.Outbound) error {
	s.bcast.Publish(ref, Frame{Kind: FrameProgress, Progress: out.Text, TaskID: out.TaskID})
	return nil
}

// Attach implements conductor.Source.
//
// The bytes are deliberately not read. The browser is on the same origin as the control
// plane and fetches GET /artifacts/{id} with its own credential, so relaying megabytes
// through the conductor would buy nothing; what the chat needs is the artifact's identity.
// It lands on the newest assistant message — the turn loop posts a final before it resolves
// that final's attachments (step 17) — and the message frame is published again with the
// same seq, which a client keyed on seq replaces rather than appends.
func (s *Source) Attach(ctx context.Context, ref string, file conductor.Attachment) error {
	if file.ArtifactID == "" {
		return fmt.Errorf("attach %s to chat %s: no artifact id", file.Name, ref)
	}
	msg, err := s.store.AttachToLastAssistantMessage(ctx, ref, store.ChatAttachment{
		ArtifactID:  file.ArtifactID,
		Name:        file.Name,
		ContentType: file.ContentType,
		SizeBytes:   file.Size,
	})
	if err != nil {
		return err
	}
	s.bcast.Publish(ref, Frame{Kind: FrameMessage, Message: msg})
	return nil
}

// React implements conductor.Source: the turn's state, as an ephemeral frame. A reload
// re-derives it from the turn row, so nothing here needs to be stored — except a failure
// that never said anything, which would otherwise leave a chat looking like the question
// was ignored.
func (s *Source) React(ctx context.Context, ref string, kind conductor.Reaction) error {
	switch kind {
	case conductor.ReactionWorking:
		s.bcast.Publish(ref, Frame{Kind: FrameStatus, State: StatusStarted})
		return nil
	case conductor.ReactionDone:
		s.release(ref)
		s.bcast.Publish(ref, Frame{Kind: FrameStatus, State: StatusFinished})
		return nil
	case conductor.ReactionFailed:
		spoke := s.tookSpoke(ref)
		s.release(ref)
		var err error
		if !spoke {
			err = s.storeSilentFailure(ctx, ref)
		}
		s.bcast.Publish(ref, Frame{Kind: FrameStatus, State: StatusFailed})
		return err
	default:
		return fmt.Errorf("chat: unknown reaction %q", kind)
	}
}

// storeSilentFailure leaves a row behind for a turn that failed without saying anything, so
// a reload shows why the question was never answered.
func (s *Source) storeSilentFailure(ctx context.Context, ref string) error {
	msg, err := s.store.AppendChatMessage(ctx, store.ChatMessage{
		ChatID: ref,
		Role:   store.RoleAssistant,
		Text:   "Podium could not complete this turn. An operator should check the conductor's log.",
		TS:     time.Now().UTC(),
	})
	if err != nil {
		return err
	}
	s.bcast.Publish(ref, Frame{Kind: FrameMessage, Message: msg})
	return nil
}
