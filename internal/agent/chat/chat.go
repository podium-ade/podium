// Package chat is the web chat: the third conductor.Source, and the only one whose
// conversation Podium itself holds.
//
// Slack has threads and Linear has issues; a browser has nothing, so the chats and
// chat_messages tables ARE the conversation. What a turn is handed as history, what a
// reload renders and what the chat list shows all come out of them — including what a task
// said on its way to an answer, which is stored under its own role. The one thing that is
// not a row is the placeholder: that is the conductor announcing a turn, and the running
// indicator is where it belongs.
//
// Everything in a chat is content. A human wrote the user messages and a task wrote the
// assistant and progress ones; this package stores and relays them all and interprets none.
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

	"github.com/podium-ade/podium/internal/agent/conductor"
	"github.com/podium-ade/podium/internal/agent/profiles"
	"github.com/podium-ade/podium/internal/agent/store"
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
	SetChatChoice(ctx context.Context, id string, c store.ChatChoice) (store.Chat, error)
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
	UIURL  string
	Logger *slog.Logger
}

// SendRequest is one human message arriving from the browser.
type SendRequest struct {
	ChatID string
	// Login is the caller, as podium-server asserted it. It owns the chat and authors the
	// message.
	Login string
	Text  string
	// Override is the composer's model picker: what ANSWERS this message. Empty everywhere
	// means the assistant's own model, which is the profile's.
	//
	// It does not reach a task the turn delegates: that runs on its playbook's model. "Answer
	// me on Grok" is about the conversation, not about how a container does its job.
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
	store  Store
	bcast  *Broadcaster
	name   string
	uiURL  string
	logger *slog.Logger
	events chan conductor.InboundEvent

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
	return &Source{
		store:  opts.Store,
		bcast:  NewBroadcaster(),
		name:   opts.DisplayName,
		uiURL:  strings.TrimSuffix(opts.UIURL, "/"),
		logger: logger,
		events: make(chan conductor.InboundEvent, eventBuffer),
		live:   map[string]*live{},
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

// Mirrored implements conductor.MirrorWatcher: a row the conductor wrote into a mirrored
// thread reaches whoever has that chat open, exactly as this source's own rows do.
func (s *Source) Mirrored(chatID string, msg store.ChatMessage) {
	s.bcast.Publish(chatID, Frame{Kind: FrameMessage, Message: msg})
}

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

	// No playbook travels with a chat message. The assistant answers it, and the playbooks
	// are what that turn delegates to — one per piece of work, decided by the turn.
	ev := conductor.InboundEvent{
		SourceKind: conductor.SourceChat,
		SourceKey:  store.ChatSourceKey(req.ChatID),
		Ref:        req.ChatID,
		Author:     req.Login,
		Text:       req.Text,
		TS:         msg.TS,
		URL:        s.URL(req.ChatID),
		Override:   req.Override,
		BriefKind:  conductor.SourceChat,
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

// remember records what this message was answered on and names the chat from its first
// query.
//
// The choice is NOT first-wins: a person may change model mid-conversation, and the row
// follows the latest message so the composer opens on it next time rather than making them
// pick again. The title still is: one is chosen once, and a title the caller supplied at
// create is left alone.
func (s *Source) remember(ctx context.Context, chat store.Chat, req SendRequest) {
	choice := store.ChatChoice{
		Agent:  req.Override.Agent,
		Model:  req.Override.Model,
		Effort: req.Override.Effort,
	}
	if choice != chat.ChatChoice {
		updated, err := s.store.SetChatChoice(ctx, req.ChatID, choice)
		if err != nil {
			s.logger.WarnContext(ctx, "recording what a chat is answered on failed; "+
				"the next message will need it picked again",
				"chat_id", req.ChatID, "error", err)
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

// FetchTranscript implements conductor.Source: the questions and answers, oldest first. The
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
		// A turn's own half-finished thoughts are not history. They are in the transcript a
		// human reads, and leaving them out of this one keeps the brief's cap for the
		// questions and answers it is meant to carry.
		if m.Role == store.RoleProgress {
			continue
		}
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
// Everything said is a row, progress included: a conversation Podium holds itself has
// nowhere else to keep it. Every row carries out.TaskID, which is empty for the assistant's
// own words and set for a delegated task's — the only thing that tells them apart once they
// are both lines in the same transcript.
func (s *Source) Post(ctx context.Context, ref string, out conductor.Outbound) (string, error) {
	if out.Type == conductor.OutProgress {
		// A progress message has no id, but the turn loop edits whatever Post returned, so
		// it needs something non-empty to hold on to.
		return progressMessageID, s.progress(ctx, ref, out)
	}
	msg, err := s.store.AppendChatMessage(ctx, store.ChatMessage{
		ChatID: ref,
		Role:   store.RoleAssistant,
		Text:   out.Text,
		TS:     time.Now().UTC(),
		TaskID: out.TaskID,
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

// progressMessageID is the id Post returns for a progress line. There is nothing to
// address: an edit appends rather than replaces, so the id is only a handle the turn loop
// needs to be non-empty.
const progressMessageID = "progress"

// Edit implements conductor.Source.
//
// The turn loop edits its placeholder to show the newest progress. A chat has no
// placeholder to edit — the running indicator is not a message — so each edit is another
// line in the transcript, which is what makes the trail readable after the fact instead of
// only while somebody was watching.
func (s *Source) Edit(ctx context.Context, ref, _ string, out conductor.Outbound) error {
	return s.progress(ctx, ref, out)
}

// progress relays one work-in-progress line, as a message with its own role.
//
// The placeholder is the exception: it is the conductor saying it has started, not a word
// from the task, and React already published the same news as a status frame — so it stays
// ephemeral and the running indicator is what shows it. The prefix goes too: it marks a
// work-in-progress line where nothing else can, and this chat styles the difference.
func (s *Source) progress(ctx context.Context, ref string, out conductor.Outbound) error {
	if out.Text == conductor.Placeholder {
		s.bcast.Publish(ref, Frame{Kind: FrameProgress, Progress: out.Text, TaskID: out.TaskID})
		return nil
	}
	text := strings.TrimPrefix(out.Text, conductor.ProgressPrefix)
	if strings.TrimSpace(text) == "" {
		return nil
	}
	// Deliberately no noteSpoke: a thought is not an answer, so a turn that dies after only
	// thinking still leaves its plain-words failure behind.
	msg, err := s.store.AppendChatMessage(ctx, store.ChatMessage{
		ChatID: ref,
		Role:   store.RoleProgress,
		Text:   text,
		TS:     time.Now().UTC(),
		TaskID: out.TaskID,
	})
	if err != nil {
		return err
	}
	s.bcast.Publish(ref, Frame{Kind: FrameMessage, Message: msg})
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
