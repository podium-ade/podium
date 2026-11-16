package routines

// A routine on Linear events that runs in each ticket's conversation (docs/agent.md, "On a
// Linear event"). Its waiting events are grouped by what they are about, and each group runs as
// a turn in that issue's or project's conversation: the same session every agent session on the
// issue is a turn of (linear:<issue id>), which holds everything said about it — delegations,
// @mentions, other routines' runs and the tasks they started. The run reads all of that, and
// decides whether its events are new work, a duplicate, or something to add to work already
// running. Events about a cycle or a label are no conversation, and run in the routine's own
// chat, opened the first time one does.
//
// It is still one run of the routine at a time. The routine's state is one object, rewritten by
// every run; two runs in two conversations would each write their own and the later would
// overwrite the earlier. So a tick starts the subject that has waited longest, and the others
// wait for the ticks after it. A subject whose conversation is busy — a turn is running or
// waiting there — is passed over for the next one that is not: its events wait for the
// conversation, as a second prompt in it would.

import (
	"context"
	"errors"
	"time"

	"github.com/podium-ade/podium/internal/agent/store"
)

// ConversationPreamble is put before the instruction of a run in an issue's or project's
// conversation. That conversation is shared with every other turn about the issue, so the run is
// told where it is, and that it decides whether its events are anything new.
const ConversationPreamble = "This run is a turn in the conversation of the Linear issue or project its " +
	"events are about. Earlier turns on it — people who delegated or mentioned you on it, other routines' " +
	"runs, and the tasks any of them started — are in the conversation above, and tasks still running " +
	"are in your brief. Do not redo work that is done or start work that is already running: when the " +
	"events ask for nothing new, finish idle; when a running task should know about them, add to that " +
	"task instead of starting another.\n\n"

// perSubject reports whether r's events run in each ticket's conversation on this scheduler.
func (s *Scheduler) perSubject(r store.Routine) bool {
	return s.subjects != nil && r.PerSubject()
}

// fireSubjects runs the subject of r's waiting events that has waited longest and whose
// conversation is free, or leaves them all waiting.
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
		var busy bool
		if key := store.LinearConversationKey(sub.Key); key != "" {
			busy, err = s.keyBusy(ctx, key)
		} else {
			// Events about a cycle, a label or nothing in particular run in the routine's
			// own chat, which may not be open yet.
			busy, err = s.busy(ctx, r)
		}
		if err != nil {
			s.logger.WarnContext(ctx, "asking whether a routine's conversation is busy failed",
				"routine_id", r.ID, "subject", sub.Key, "error", err)
			return
		}
		if busy {
			continue
		}
		if _, err := s.startEventRun(ctx, r, now, &sub); err != nil && !errors.Is(err, errClaimed) {
			s.logger.InfoContext(ctx, "a routine's Linear events did not run", "routine_id", r.ID,
				"subject", sub.Key, "reason", err)
		}
		// One run of the routine at a time, whether or not this one started.
		return
	}
}

// subjectConversation is the chat of the conversation sub's run goes in, opened now when the
// issue has none, and records it on the run. The routine's owner is recorded as somebody who
// touched the issue, which is what lets them read the conversation their routine ran in.
func (s *Scheduler) subjectConversation(ctx context.Context, r store.Routine, run store.RoutineRun, key, title string) (string, error) {
	chat, created, err := s.subjects.EnsureLinearConversation(ctx, key, title)
	if err != nil {
		return "", err
	}
	if created {
		s.logger.InfoContext(ctx, "opened a Linear conversation for a routine's run", "routine_id", r.ID,
			"source_key", key, "chat_id", chat.ID)
	}
	if err := s.subjects.SetRoutineRunChat(ctx, run.ID, chat.ID); err != nil {
		// The run still goes ahead: the chat it is in is only what its run list links to.
		s.logger.WarnContext(ctx, "recording the conversation of a routine run failed", "run_id", run.ID, "error", err)
	}
	if r.OwnerLogin != "" {
		if err := s.subjects.RecordLinearPeople(ctx, key, []store.LinearPerson{
			{Login: r.OwnerLogin, Via: store.LinearViaRoutine},
		}, s.now()); err != nil {
			s.logger.WarnContext(ctx, "recording a routine's owner on its issue failed", "run_id", run.ID, "error", err)
		}
	}
	return chat.ID, nil
}
