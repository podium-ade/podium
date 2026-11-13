package routines

// A routine on Linear events with a chat per ticket (docs/agent.md, "On a Linear event"). Its
// waiting events are grouped by what they are about — an issue and the comments on it, a
// project and its updates, a cycle, a label — and each group runs in that subject's own chat,
// which keeps the subject's history: the next event about ENG-12 is answered with the earlier
// ones in view.
//
// It is still one run of the routine at a time. The routine's state is one object, rewritten by
// every run; two runs in two chats would each write their own and the later would overwrite
// the earlier. So a tick starts the subject that has waited longest, and the others wait for
// the ticks after it. A subject whose chat is busy — somebody is talking in it, or a turn is
// running there — is passed over for the next one that is not, so one conversation does not
// hold up every other ticket.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/podium-ade/podium/internal/agent/store"
)

const (
	// MaxSubjectChatsPerDay is how many chats per ticket one routine may open in 24 hours.
	// Past it the routine is paused: a routine on a busy workspace, or on events it causes
	// itself, would otherwise fill its owner's chat list.
	MaxSubjectChatsPerDay = 50
)

// perSubject reports whether r's events run in a chat per ticket on this scheduler.
func (s *Scheduler) perSubject(r store.Routine) bool {
	return s.subjects != nil && r.PerSubject()
}

// fireSubjects runs the subject of r's waiting events that has waited longest and whose chat
// is free, or leaves them all waiting.
func (s *Scheduler) fireSubjects(ctx context.Context, r store.Routine, now time.Time) {
	if running, err := s.subjects.RunningRoutineRun(ctx, r.ID); err == nil {
		s.logger.DebugContext(ctx, "a routine's Linear events wait: a run of it is going",
			"routine_id", r.ID, "run_id", running.ID)
		return
	} else if !errors.Is(err, store.ErrNotFound) {
		s.logger.WarnContext(ctx, "asking whether a routine has a run going failed", "routine_id", r.ID, "error", err)
		return
	}
	subjects, err := s.subjects.PendingRoutineSubjects(ctx, r.ID)
	if err != nil {
		s.logger.WarnContext(ctx, "listing what a routine's Linear events are about failed", "routine_id", r.ID, "error", err)
		return
	}
	for _, sub := range subjects {
		if ctx.Err() != nil {
			return
		}
		chatID := sub.ChatID
		if sub.Key == "" {
			// Events about nothing in particular run in the routine's own chat.
			chatID = r.ChatID
		}
		if chatID != "" {
			busy, err := s.chatBusy(ctx, chatID)
			if err != nil {
				s.logger.WarnContext(ctx, "asking whether a routine's chat is busy failed",
					"routine_id", r.ID, "chat_id", chatID, "error", err)
				return
			}
			if busy {
				continue
			}
		} else {
			opened, err := s.subjects.CountRoutineSubjectChatsSince(ctx, r.ID, now.Add(-24*time.Hour))
			if err != nil {
				s.logger.WarnContext(ctx, "counting a routine's chats per ticket failed", "routine_id", r.ID, "error", err)
				return
			}
			if opened >= MaxSubjectChatsPerDay {
				s.pause(ctx, r, fmt.Sprintf("It opened %d chats, one per ticket, within 24 hours, which is as many as "+
					"a routine may. Narrow its events or teams, or run it in its own chat, then resume it.", opened), now)
				return
			}
		}
		if _, err := s.startEventRun(ctx, r, now, &sub); err != nil && !errors.Is(err, errClaimed) {
			s.logger.InfoContext(ctx, "a routine's Linear events did not run", "routine_id", r.ID,
				"subject", sub.Key, "reason", err)
		}
		// One run of the routine at a time, whether or not this one started.
		return
	}
}

// subjectChat is the chat sub's run goes in, opened now when the subject has none, and records
// it on the run.
func (s *Scheduler) subjectChat(ctx context.Context, r store.Routine, run store.RoutineRun, sub store.RoutineSubject) (string, error) {
	chatID, created, err := s.subjects.EnsureRoutineSubjectChat(ctx, r, sub.Key, sub.Title)
	if err != nil {
		return "", err
	}
	if created {
		s.logger.InfoContext(ctx, "opened a routine's chat for a ticket", "routine_id", r.ID,
			"subject", sub.Key, "chat_id", chatID)
	}
	if err := s.subjects.SetRoutineRunChat(ctx, run.ID, chatID); err != nil {
		// The run still goes ahead: the chat it is in is only what its card links to.
		s.logger.WarnContext(ctx, "recording the chat of a routine run failed", "run_id", run.ID, "error", err)
	}
	return chatID, nil
}
