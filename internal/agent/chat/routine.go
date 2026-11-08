package chat

// A routine's scheduled run in its chat (conductor/routine.go). The scheduler hands the chat a
// run; the chat turns it into an event the way a send would — the chat's own session, its
// owner as the speaker — except that nothing is written into the record: the instruction is the
// routine's, and it goes into the brief, not the transcript.
//
// While the run is going, what it says is held here rather than stored: its thinking and its
// tool calls are shown live, as progress, and kept in memory. When it ends the conductor says
// whether to keep them. A run that found nothing to do leaves nothing in the chat and does not
// mark it unread; one that did something is written in, under a divider, ahead of its answer.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/podium-ade/podium/internal/agent/conductor"
	"github.com/podium-ade/podium/internal/agent/profiles"
	"github.com/podium-ade/podium/internal/agent/store"
)

// RoutineRun is a scheduled run, as the scheduler hands it to the chat.
type RoutineRun struct {
	ChatID      string
	Login       string
	Instruction string
	RoutineID   string
	RunID       string
	// Override is the routine's own model and effort. Empty follows the chat's assistant.
	Override profiles.Override
}

// ErrChatBusy is a run for a chat that is answering, or has something waiting.
var ErrChatBusy = errors.New("chat: a turn is running or waiting in this chat")

// held is what a run has said so far.
type held struct {
	msgs []store.ChatMessage
}

var _ conductor.RoutineHolder = (*Source)(nil)

// RunRoutine starts a routine's scheduled run in its chat. It refuses a chat that is busy on
// this conductor; the scheduler has already asked the database about the others.
func (s *Source) RunRoutine(ctx context.Context, run RoutineRun) error {
	if run.ChatID == "" || strings.TrimSpace(run.Instruction) == "" {
		return errors.New("run routine: a chat and an instruction are required")
	}
	chat, err := s.store.GetChat(ctx, run.ChatID)
	if err != nil {
		return err
	}
	if chat.Login == "" || chat.Login != run.Login {
		return fmt.Errorf("run routine: chat %s is not %s's", run.ChatID, run.Login)
	}
	personalityName, personalityPrompt, personalityModel, err := s.personalityFor(ctx, chat)
	if err != nil {
		return err
	}
	override := run.Override
	if override.Empty() {
		override = personalityModel
	}

	s.mu.Lock()
	st := s.live[run.ChatID]
	if st == nil {
		st = &live{}
		s.live[run.ChatID] = st
	}
	if st.busy {
		s.mu.Unlock()
		return ErrChatBusy
	}
	st.busy, st.spoke = true, false
	st.held = &held{}
	s.mu.Unlock()

	sourceKey := chat.SourceKey
	if sourceKey == "" {
		sourceKey = store.ChatSourceKey(run.ChatID)
	}
	ev := conductor.InboundEvent{
		SourceKind:        conductor.SourceChat,
		SourceKey:         sourceKey,
		Ref:               run.ChatID,
		Author:            run.Login,
		Login:             run.Login,
		Text:              run.Instruction,
		TS:                time.Now().UTC(),
		URL:               s.URL(run.ChatID),
		Override:          override,
		BriefKind:         conductor.SourceChat,
		PersonalityName:   personalityName,
		PersonalityPrompt: personalityPrompt,
		Mentioned:         true,
		ChatID:            run.ChatID,
		Routine:           &conductor.RoutineRef{RoutineID: run.RoutineID, RunID: run.RunID},
	}
	select {
	case s.events <- ev:
		return nil
	case <-ctx.Done():
		s.mu.Lock()
		st.busy, st.held = false, nil
		s.mu.Unlock()
		return fmt.Errorf("run routine in chat %s: %w", run.ChatID, ctx.Err())
	}
}

// HoldsRun implements conductor.RoutineHolder.
func (s *Source) HoldsRun(ref string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.live[ref] != nil && s.live[ref].held != nil
}

// ReleaseRun implements conductor.RoutineHolder: what the run said goes into the record under
// a divider, or is dropped.
func (s *Source) ReleaseRun(ctx context.Context, ref string, keep bool, divider string) {
	s.mu.Lock()
	var h *held
	if st := s.live[ref]; st != nil {
		h, st.held = st.held, nil
	}
	s.mu.Unlock()
	if h == nil || !keep {
		return
	}
	if strings.TrimSpace(divider) != "" {
		s.appendAndShow(ctx, store.ChatMessage{ChatID: ref, Role: store.RoleNotice, Text: divider, TS: time.Now().UTC()})
	}
	for _, m := range h.msgs {
		s.appendAndShow(ctx, m)
	}
}

func (s *Source) appendAndShow(ctx context.Context, m store.ChatMessage) {
	msg, err := s.store.AppendChatMessage(ctx, m)
	if err != nil {
		s.logger.WarnContext(ctx, "writing a routine run's output into its chat failed",
			"chat_id", m.ChatID, "role", m.Role, "error", err)
		return
	}
	s.bcast.Publish(m.ChatID, Frame{Kind: FrameMessage, Message: msg})
}

// hold keeps m for the run going in its chat, and reports whether there is one. A progress
// line is still shown live; a tool call is kept for later.
func (s *Source) hold(ref string, m store.ChatMessage) bool {
	s.mu.Lock()
	st := s.live[ref]
	if st == nil || st.held == nil {
		s.mu.Unlock()
		return false
	}
	st.held.msgs = append(st.held.msgs, m)
	s.mu.Unlock()
	if m.Role == store.RoleProgress {
		s.bcast.Publish(ref, Frame{Kind: FrameProgress, Progress: m.Text, TaskID: m.TaskID})
	}
	return true
}
