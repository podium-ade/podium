package routines

// Routines on Linear events (docs/agent.md, "On a Linear event"). The Linear agent app's
// webhook stores each data change for the routines that ask for it (store.StoreRoutineEvents);
// this tick runs each such routine with what is waiting.
//
// An event differs from a scheduled time in one way that shapes all of this: it is work
// nobody else will do. A busy chat skips a scheduled run, because the next one reads the newer
// record anyway; it leaves an event waiting, for the run after the chat is free. The waiting
// is bounded — a day, and MaxPendingEvents per routine — and a routine whose runs come faster
// than MaxEventRunsPerHour is paused, because a routine that changes what it watches can
// otherwise run itself in a loop.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/podium-ade/podium/internal/agent/chat"
	"github.com/podium-ade/podium/internal/agent/store"
)

const (
	// MaxEventsPerRun is how many waiting events one run is given, oldest first. More wait for
	// the next run.
	MaxEventsPerRun = 20
	// MaxPendingEvents is how many events wait for one routine. Beyond it the oldest are
	// dropped: a routine that far behind is better caught up from Linear than replayed.
	MaxPendingEvents = 200
	// PendingEventTTL is how long an event waits before it is dropped unrun.
	PendingEventTTL = 24 * time.Hour
	// keptEventTTL is how long an event given to a run is kept: the run's brief and divider
	// read it, and a run is long settled within a week.
	keptEventTTL = 7 * 24 * time.Hour
	// MaxEventRunsPerHour event runs of one routine within an hour pause it.
	MaxEventRunsPerHour = 60
)

// fireEvents prunes what nobody will run, then runs each routine that has events waiting.
func (s *Scheduler) fireEvents(ctx context.Context) {
	if s.events == nil {
		return
	}
	now := s.now().UTC()
	if n, err := s.events.PruneRoutineEvents(ctx, now.Add(-PendingEventTTL), now.Add(-keptEventTTL)); err != nil {
		s.logger.WarnContext(ctx, "pruning routines' Linear events failed", "error", err)
	} else if n > 0 {
		s.logger.InfoContext(ctx, "pruned routines' Linear events", "deleted", n)
	}
	waiting, err := s.events.RoutinesWithPendingEvents(ctx, dueBatch)
	if err != nil {
		s.logger.WarnContext(ctx, "listing routines with Linear events waiting failed", "error", err)
		return
	}
	for _, r := range waiting {
		if ctx.Err() != nil {
			return
		}
		s.fireEvent(ctx, r, now)
	}
}

// fireEvent runs one linear routine with the events waiting for it, or leaves them waiting and
// says why.
func (s *Scheduler) fireEvent(ctx context.Context, r store.Routine, now time.Time) {
	if !r.OnLinear() || !r.Enabled {
		return
	}
	if dropped, err := s.events.CapPendingRoutineEvents(ctx, r.ID, MaxPendingEvents); err != nil {
		s.logger.WarnContext(ctx, "capping a routine's waiting Linear events failed", "routine_id", r.ID, "error", err)
	} else if dropped > 0 {
		s.logger.WarnContext(ctx, "dropped a routine's oldest waiting Linear events",
			"routine_id", r.ID, "dropped", dropped, "kept", MaxPendingEvents)
	}
	if r.Scope == store.RoutineScopePersonal {
		if _, ok, err := s.store.CanonicalWebLogin(ctx, r.OwnerLogin); err == nil && !ok {
			s.pause(ctx, r, "Its owner is no longer a Podium member.", now)
			return
		}
	}
	if n, err := s.events.CountRoutineRunsSince(ctx, r.ID, store.RoutineTriggerEvent, now.Add(-time.Hour)); err == nil && n >= MaxEventRunsPerHour {
		s.pause(ctx, r, fmt.Sprintf("%d runs from Linear events within an hour. A routine that changes what it "+
			"watches can start itself again; look at the last runs, then resume it.", n), now)
		return
	}
	if capped, reason := s.overCap(ctx, r, now); capped {
		// Over a cap, the events wait: the cap's day or the person's 24 hours end, and the
		// work is still there to do.
		s.logger.InfoContext(ctx, "a routine's Linear events wait: over its spend cap", "routine_id", r.ID, "reason", reason)
		return
	}
	busy, err := s.busy(ctx, r)
	if err != nil {
		s.logger.WarnContext(ctx, "asking whether a routine's chat is busy failed", "routine_id", r.ID, "error", err)
		return
	}
	if busy {
		return
	}
	if _, err := s.startEvents(ctx, r, now); err != nil && !errors.Is(err, errClaimed) {
		s.logger.InfoContext(ctx, "a routine's Linear events did not run", "routine_id", r.ID, "reason", err)
	}
}

// startEvents claims a run, gives it the oldest waiting events and hands it to the chat. An
// event is given to one run only, and a run that does not start gives its events back, so a
// start that fails loses nothing and one that succeeds is the only run to see them.
func (s *Scheduler) startEvents(ctx context.Context, r store.Routine, now time.Time) (store.RoutineRun, error) {
	run, ok, err := s.store.ClaimRoutineRun(ctx, r.ID, now, store.RoutineTriggerEvent, store.RoutineRunRunning)
	if err != nil {
		return store.RoutineRun{}, err
	}
	if !ok {
		return store.RoutineRun{}, errClaimed
	}
	n, err := s.events.AttachRoutineEvents(ctx, run.ID, r.ID, MaxEventsPerRun)
	if err != nil || n == 0 {
		// Another tick took them, or none could be given: the run has nothing to do.
		if derr := s.events.DeleteUnstartedRoutineRun(context.WithoutCancel(ctx), run.ID); derr != nil {
			s.logger.WarnContext(ctx, "removing a run with no events failed", "run_id", run.ID, "error", derr)
		}
		if err == nil {
			err = errClaimed
		}
		return store.RoutineRun{}, err
	}
	if err := s.runner.RunRoutine(ctx, chatRun(r, run)); err != nil {
		s.releaseEvents(ctx, run.ID)
		status, summary := store.RoutineRunFailed, "The run could not start: "+err.Error()
		if errors.Is(err, chat.ErrChatBusy) {
			status, summary, err = store.RoutineRunSkippedOverlap, "", ErrBusy
		}
		if _, _, serr := s.store.SettleRoutineRun(ctx, run.ID, status, summary, nil); serr != nil {
			s.logger.WarnContext(ctx, "settling a run that did not start failed", "run_id", run.ID, "error", serr)
		}
		if status == store.RoutineRunFailed {
			// Its events go back to waiting, so a start that keeps failing would otherwise
			// be tried every tick.
			s.checkFailures(ctx, r.ID, now)
		}
		return run, err
	}
	s.logger.InfoContext(ctx, "a routine run started", "routine_id", r.ID, "run_id", run.ID,
		"trigger", store.RoutineTriggerEvent, "events", n)
	return run, nil
}

// releaseEvents puts a run's events back to waiting. It is written outside the caller's
// cancellation: an event given to a run that never ran must not be lost to a shutdown.
func (s *Scheduler) releaseEvents(ctx context.Context, runID string) {
	if s.events == nil {
		return
	}
	if _, err := s.events.ReleaseRoutineEvents(context.WithoutCancel(ctx), runID); err != nil {
		s.logger.WarnContext(ctx, "putting a run's Linear events back failed", "run_id", runID, "error", err)
	}
}
