package routines

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/podium-ade/podium/internal/agent/chat"
	"github.com/podium-ade/podium/internal/agent/profiles"
	"github.com/podium-ade/podium/internal/agent/store"
)

const (
	// DefaultTick is how often the leading conductor looks for due routines and settles the
	// runs that have ended. A routine runs within a tick and its jitter of its minute.
	DefaultTick = 15 * time.Second
	// dueBatch bounds how many routines one tick fires.
	dueBatch = 50
	// neverStarted is how long a run may go without a turn before it is counted as failed:
	// the event was refused, or the conductor that took it died before the turn was written.
	neverStarted = 10 * time.Minute
	// FailureWindow is how far back the auto-pause counts failed runs.
	FailureWindow = time.Hour
	// MaxFailures failed runs within FailureWindow pause the routine until its owner resumes it.
	MaxFailures = 5
)

// Store is what the scheduler needs of the conductor's database.
type Store interface {
	DueRoutines(ctx context.Context, now time.Time, limit int) ([]store.Routine, error)
	GetRoutine(ctx context.Context, id string) (store.Routine, error)
	AdvanceRoutine(ctx context.Context, id string, next time.Time, last *time.Time) error
	SetRoutineEnabled(ctx context.Context, id string, enabled bool, reason string, next time.Time) (store.Routine, error)
	ClaimRoutineRun(ctx context.Context, routineID string, scheduledFor time.Time, trigger, status string) (store.RoutineRun, bool, error)
	SettleRoutineRun(ctx context.Context, id, status, summary string, cost *float64) (store.RoutineRun, bool, error)
	RunningRoutineRuns(ctx context.Context) ([]store.RoutineRun, error)
	TurnForRoutineRun(ctx context.Context, runID string) (store.Turn, error)
	NoteRoutineIdle(ctx context.Context, id string, idle bool) error
	RoutineFailuresSince(ctx context.Context, routineID string, since time.Time) (int, error)
	GetSessionByKey(ctx context.Context, sourceKey string) (store.Session, error)
	SessionBusy(ctx context.Context, sessionID string) (bool, error)
	CanonicalWebLogin(ctx context.Context, typed string) (string, bool, error)
	RoutineSpendSince(ctx context.Context, routineID string, since time.Time) (float64, error)
	PersonRoutineSpendSince(ctx context.Context, owner string, since time.Time) (float64, error)
	GetRoutineLimits(ctx context.Context) (store.RoutineLimits, error)
	PauseRoutineUntil(ctx context.Context, id, reason string, until, next time.Time) (store.Routine, error)
	DueToResume(ctx context.Context, now time.Time, limit int) ([]store.Routine, error)
	NotifyRoutine(ctx context.Context, login, chatID, actor, detail string) error
}

// Runner starts a run in a routine's chat. The web chat implements it.
type Runner interface {
	RunRoutine(ctx context.Context, run chat.RoutineRun) error
}

// Scheduler fires routines and settles their runs.
type Scheduler struct {
	store  Store
	runner Runner
	logger *slog.Logger
	tick   time.Duration
	now    func() time.Time
}

// Options configures a Scheduler.
type Options struct {
	Store  Store
	Runner Runner
	Logger *slog.Logger
	// Tick is how often to look; zero is DefaultTick.
	Tick time.Duration
	// Now is the clock; nil is time.Now.
	Now func() time.Time
}

// New returns a scheduler. It does nothing until Run.
func New(opts Options) (*Scheduler, error) {
	if opts.Store == nil || opts.Runner == nil {
		return nil, errors.New("routines: a store and a runner are required")
	}
	s := &Scheduler{store: opts.Store, runner: opts.Runner, logger: opts.Logger, tick: opts.Tick, now: opts.Now}
	if s.logger == nil {
		s.logger = slog.Default()
	}
	if s.tick <= 0 {
		s.tick = DefaultTick
	}
	if s.now == nil {
		s.now = time.Now
	}
	return s, nil
}

// Run ticks until ctx ends. Only the leading conductor runs it (conductor.WhileLeader), so a
// routine is fired by one conductor at a time; the run rows' unique key covers the hand-over.
func (s *Scheduler) Run(ctx context.Context) error {
	t := time.NewTicker(s.tick)
	defer t.Stop()
	for {
		s.Tick(ctx)
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}

// Tick settles what has ended, resumes what was paused for a day that is over, then fires
// what is due.
func (s *Scheduler) Tick(ctx context.Context) {
	s.settle(ctx)
	s.resumeDue(ctx)
	s.fireDue(ctx)
}

// resumeDue starts again every routine Podium paused until a moment that has passed: the day
// its spend cap was reached is over in its timezone.
func (s *Scheduler) resumeDue(ctx context.Context) {
	now := s.now().UTC()
	due, err := s.store.DueToResume(ctx, now, dueBatch)
	if err != nil {
		s.logger.WarnContext(ctx, "listing routines to resume failed", "error", err)
		return
	}
	for _, r := range due {
		sched, err := Parse(r.Schedule, r.Timezone)
		if err != nil {
			continue
		}
		if _, err := s.store.SetRoutineEnabled(ctx, r.ID, true, "", sched.NextFor(r.ID, now)); err != nil {
			s.logger.WarnContext(ctx, "resuming a routine after its capped day failed", "routine_id", r.ID, "error", err)
			continue
		}
		s.logger.InfoContext(ctx, "resumed a routine after its capped day", "routine_id", r.ID)
	}
}

func (s *Scheduler) fireDue(ctx context.Context) {
	now := s.now().UTC()
	due, err := s.store.DueRoutines(ctx, now, dueBatch)
	if err != nil {
		s.logger.WarnContext(ctx, "listing due routines failed", "error", err)
		return
	}
	for _, r := range due {
		if ctx.Err() != nil {
			return
		}
		s.fire(ctx, r, now)
	}
}

// fire runs one due routine, or records why it did not. Its next run is set first, so a
// routine that cannot be fired is not tried again every tick.
func (s *Scheduler) fire(ctx context.Context, r store.Routine, now time.Time) {
	sched, err := Parse(r.Schedule, r.Timezone)
	if err != nil {
		s.pause(ctx, r, "Its schedule can no longer be read: "+err.Error(), now)
		return
	}
	scheduledFor := r.NextRunAt
	if err := s.store.AdvanceRoutine(ctx, r.ID, sched.NextAfterFiring(r.ID, scheduledFor, now), &now); err != nil {
		s.logger.WarnContext(ctx, "advancing a routine failed; not firing it this tick", "routine_id", r.ID, "error", err)
		return
	}
	if r.Scope == store.RoutineScopePersonal {
		if _, ok, err := s.store.CanonicalWebLogin(ctx, r.OwnerLogin); err == nil && !ok {
			s.pause(ctx, r, "Its owner is no longer a Podium member.", now)
			return
		}
	}
	if capped, reason := s.overCap(ctx, r, now); capped {
		if _, _, err := s.store.ClaimRoutineRun(ctx, r.ID, scheduledFor, store.RoutineTriggerSchedule, store.RoutineRunSkippedPaused); err != nil {
			s.logger.WarnContext(ctx, "recording a capped routine run failed", "routine_id", r.ID, "error", err)
		}
		s.logger.InfoContext(ctx, "a routine did not run: over its spend cap", "routine_id", r.ID, "reason", reason)
		return
	}
	if _, err := s.start(ctx, r, scheduledFor, store.RoutineTriggerSchedule); err != nil && !errors.Is(err, errClaimed) {
		s.logger.InfoContext(ctx, "a routine did not run", "routine_id", r.ID, "reason", err)
	}
}

// overCap reports whether a routine may not run now for what has been spent. A routine at its
// own daily cap is paused until midnight in its timezone, and its owner told. A person at
// their cap across all their routines is skipped, not paused: the 24 hours roll on, and the
// next tick under the cap runs.
func (s *Scheduler) overCap(ctx context.Context, r store.Routine, now time.Time) (bool, string) {
	if r.DailyCapUSD != nil && *r.DailyCapUSD > 0 {
		midnight, next := DayBounds(r.Timezone, now)
		spent, err := s.store.RoutineSpendSince(ctx, r.ID, midnight)
		if err == nil && spent >= *r.DailyCapUSD {
			reason := fmt.Sprintf("Reached its daily spend cap of $%.2f. It runs again after midnight (%s).",
				*r.DailyCapUSD, r.Timezone)
			sched, perr := Parse(r.Schedule, r.Timezone)
			after := next
			if perr == nil {
				after = sched.NextFor(r.ID, next)
			}
			if _, err := s.store.PauseRoutineUntil(ctx, r.ID, reason, next, after); err != nil {
				s.logger.WarnContext(ctx, "pausing a capped routine failed", "routine_id", r.ID, "error", err)
			} else {
				s.notify(ctx, r, reason)
			}
			return true, reason
		}
	}
	if r.Scope == store.RoutineScopePersonal && r.OwnerLogin != "" {
		limits, err := s.store.GetRoutineLimits(ctx)
		if err != nil || limits.PersonDailyCapUSD <= 0 {
			return false, ""
		}
		spent, err := s.store.PersonRoutineSpendSince(ctx, r.OwnerLogin, now.Add(-24*time.Hour))
		if err == nil && spent >= limits.PersonDailyCapUSD {
			return true, fmt.Sprintf("its owner's routines spent $%.2f in 24 hours, the most one person's may", spent)
		}
	}
	return false, ""
}

// DayBounds is the start of the day now falls in, in tz, and the start of the next one.
func DayBounds(tz string, now time.Time) (start, next time.Time) {
	loc, err := time.LoadLocation(tz)
	if err != nil {
		loc = time.UTC
	}
	local := now.In(loc)
	start = time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
	return start, start.AddDate(0, 0, 1)
}

// notify tells a personal routine's owner, in their inbox, that Podium paused it. A system
// routine has no owner; its card says why, and the audit log has who resumes it.
func (s *Scheduler) notify(ctx context.Context, r store.Routine, reason string) {
	if r.OwnerLogin == "" {
		return
	}
	if err := s.store.NotifyRoutine(ctx, r.OwnerLogin, r.ChatID, "", reason); err != nil {
		s.logger.WarnContext(ctx, "telling a routine's owner it was paused failed", "routine_id", r.ID, "error", err)
	}
}

// errClaimed is a firing another conductor already recorded.
var errClaimed = errors.New("already claimed")

// ErrBusy is a routine whose chat is answering, or has a message waiting: a run is never put
// behind either, because the next one reads the newer record anyway.
var ErrBusy = errors.New("the routine's chat is busy: a turn is running or waiting there")

// start claims a run and hands it to the chat. A chat that is busy records the run as skipped.
func (s *Scheduler) start(ctx context.Context, r store.Routine, scheduledFor time.Time, trigger string) (store.RoutineRun, error) {
	busy, err := s.busy(ctx, r)
	if err != nil {
		return store.RoutineRun{}, err
	}
	if busy {
		if _, _, err := s.store.ClaimRoutineRun(ctx, r.ID, scheduledFor, trigger, store.RoutineRunSkippedOverlap); err != nil {
			s.logger.WarnContext(ctx, "recording a skipped routine run failed", "routine_id", r.ID, "error", err)
		}
		return store.RoutineRun{}, ErrBusy
	}
	run, ok, err := s.store.ClaimRoutineRun(ctx, r.ID, scheduledFor, trigger, store.RoutineRunRunning)
	if err != nil {
		return store.RoutineRun{}, err
	}
	if !ok {
		return store.RoutineRun{}, errClaimed
	}
	err = s.runner.RunRoutine(ctx, chat.RoutineRun{
		ChatID:      r.ChatID,
		Login:       r.OwnerLogin,
		Instruction: r.Instruction,
		RoutineID:   r.ID,
		RunID:       run.ID,
		Override:    profiles.Override{Model: r.Model, Effort: r.Effort},
		System:      r.System(),
	})
	if err != nil {
		status, summary := store.RoutineRunFailed, "The run could not start: "+err.Error()
		if errors.Is(err, chat.ErrChatBusy) {
			status, summary, err = store.RoutineRunSkippedOverlap, "", ErrBusy
		}
		if _, _, serr := s.store.SettleRoutineRun(ctx, run.ID, status, summary, nil); serr != nil {
			s.logger.WarnContext(ctx, "settling a run that did not start failed", "run_id", run.ID, "error", serr)
		}
		return run, err
	}
	s.logger.InfoContext(ctx, "a routine run started", "routine_id", r.ID, "run_id", run.ID, "trigger", trigger)
	return run, nil
}

func (s *Scheduler) busy(ctx context.Context, r store.Routine) (bool, error) {
	sess, err := s.store.GetSessionByKey(ctx, store.ChatSourceKey(r.ChatID))
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return s.store.SessionBusy(ctx, sess.ID)
}

// RunNow fires a routine at once, outside its schedule. It runs on whichever conductor the
// person reached, through that conductor's chat.
func (s *Scheduler) RunNow(ctx context.Context, routineID string) (store.RoutineRun, error) {
	r, err := s.store.GetRoutine(ctx, routineID)
	if err != nil {
		return store.RoutineRun{}, err
	}
	run, err := s.start(ctx, r, s.now().UTC().Truncate(time.Second), store.RoutineTriggerManual)
	if errors.Is(err, errClaimed) {
		return store.RoutineRun{}, fmt.Errorf("%w: it was started a moment ago", ErrBusy)
	}
	return run, err
}

// settle records how each run whose turn has ended went, and pauses a routine that keeps
// failing.
func (s *Scheduler) settle(ctx context.Context) {
	runs, err := s.store.RunningRoutineRuns(ctx)
	if err != nil {
		s.logger.WarnContext(ctx, "listing running routine runs failed", "error", err)
		return
	}
	now := s.now().UTC()
	for _, run := range runs {
		if ctx.Err() != nil {
			return
		}
		status, summary, cost, ok := s.outcome(ctx, run, now)
		if !ok {
			continue
		}
		settled, changed, err := s.store.SettleRoutineRun(ctx, run.ID, status, summary, cost)
		if err != nil || !changed {
			if err != nil {
				s.logger.WarnContext(ctx, "settling a routine run failed", "run_id", run.ID, "error", err)
			}
			continue
		}
		switch settled.Status {
		case store.RoutineRunIdle, store.RoutineRunDone:
			if err := s.store.NoteRoutineIdle(ctx, run.RoutineID, settled.Status == store.RoutineRunIdle); err != nil {
				s.logger.WarnContext(ctx, "counting an idle run failed", "routine_id", run.RoutineID, "error", err)
			}
		case store.RoutineRunFailed:
			s.checkFailures(ctx, run.RoutineID, now)
		}
	}
}

// outcome is how a running run ended, or ok=false while it has not.
func (s *Scheduler) outcome(ctx context.Context, run store.RoutineRun, now time.Time) (status, summary string, cost *float64, ok bool) {
	turn, err := s.store.TurnForRoutineRun(ctx, run.ID)
	if errors.Is(err, store.ErrNotFound) {
		if now.Sub(run.CreatedAt) > neverStarted {
			return store.RoutineRunFailed, "The run never started.", nil, true
		}
		return "", "", nil, false
	}
	if err != nil || turn.Status == store.TurnRunning {
		return "", "", nil, false
	}
	switch turn.Status {
	case store.TurnSucceeded:
		if run.Reported == store.RoutineRunIdle {
			return store.RoutineRunIdle, "", turn.CostUSD, true
		}
		return store.RoutineRunDone, "", turn.CostUSD, true
	case store.TurnCancelled:
		return store.RoutineRunDone, "Stopped before it finished.", turn.CostUSD, true
	default:
		return store.RoutineRunFailed, "The run's turn ended " + turn.Status + ".", turn.CostUSD, true
	}
}

// checkFailures pauses a routine with MaxFailures failed runs inside FailureWindow.
func (s *Scheduler) checkFailures(ctx context.Context, routineID string, now time.Time) {
	n, err := s.store.RoutineFailuresSince(ctx, routineID, now.Add(-FailureWindow))
	if err != nil || n < MaxFailures {
		return
	}
	r, err := s.store.GetRoutine(ctx, routineID)
	if err != nil || !r.Enabled {
		return
	}
	s.pause(ctx, r, fmt.Sprintf("%d runs failed within an hour. Look at the last one, then resume it.", n), now)
}

func (s *Scheduler) pause(ctx context.Context, r store.Routine, reason string, now time.Time) {
	if _, err := s.store.SetRoutineEnabled(ctx, r.ID, false, reason, r.NextRunAt); err != nil {
		s.logger.WarnContext(ctx, "pausing a routine failed", "routine_id", r.ID, "error", err)
		return
	}
	s.logger.InfoContext(ctx, "paused a routine", "routine_id", r.ID, "reason", reason, "at", now)
	s.notify(ctx, r, reason)
}
