package routines

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/agent/chat"
	"github.com/podium-ade/podium/internal/agent/store"
)

// fakeEvent is one row of routine_events.
type fakeEvent struct {
	id      int
	routine string
	run     string
	at      time.Time
}

// fakeEvents is the store's Linear events in memory, beside a fakeStore, under its lock.
type fakeEvents struct {
	st     *fakeStore
	events []*fakeEvent
	seq    int
}

func (f *fakeEvents) add(routine string, at time.Time) {
	f.st.mu.Lock()
	defer f.st.mu.Unlock()
	f.seq++
	f.events = append(f.events, &fakeEvent{id: f.seq, routine: routine, at: at})
}

func (f *fakeEvents) pending(routine string) []*fakeEvent {
	var out []*fakeEvent
	for _, e := range f.events {
		if e.routine == routine && e.run == "" {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].at.Before(out[j].at) })
	return out
}

func (f *fakeEvents) given(run string) []*fakeEvent {
	f.st.mu.Lock()
	defer f.st.mu.Unlock()
	var out []*fakeEvent
	for _, e := range f.events {
		if e.run == run {
			out = append(out, e)
		}
	}
	return out
}

func (f *fakeEvents) waiting(routine string) int {
	f.st.mu.Lock()
	defer f.st.mu.Unlock()
	return len(f.pending(routine))
}

func (f *fakeEvents) RoutinesWithPendingEvents(context.Context, int) ([]store.Routine, error) {
	f.st.mu.Lock()
	defer f.st.mu.Unlock()
	var out []store.Routine
	for _, r := range f.st.routines {
		if r.Enabled && r.OnLinear() && len(f.pending(r.ID)) > 0 {
			out = append(out, r)
		}
	}
	return out, nil
}

func (f *fakeEvents) CapPendingRoutineEvents(_ context.Context, routine string, keep int) (int, error) {
	f.st.mu.Lock()
	defer f.st.mu.Unlock()
	p := f.pending(routine)
	if len(p) <= keep {
		return 0, nil
	}
	drop := map[*fakeEvent]bool{}
	for _, e := range p[:len(p)-keep] {
		drop[e] = true
	}
	kept := f.events[:0]
	for _, e := range f.events {
		if !drop[e] {
			kept = append(kept, e)
		}
	}
	f.events = kept
	return len(drop), nil
}

func (f *fakeEvents) AttachRoutineEvents(_ context.Context, run, routine string, limit int) (int, error) {
	f.st.mu.Lock()
	defer f.st.mu.Unlock()
	p := f.pending(routine)
	if len(p) > limit {
		p = p[:limit]
	}
	for _, e := range p {
		e.run = run
	}
	return len(p), nil
}

func (f *fakeEvents) ReleaseRoutineEvents(_ context.Context, run string) (int, error) {
	f.st.mu.Lock()
	defer f.st.mu.Unlock()
	n := 0
	for _, e := range f.events {
		if e.run == run {
			e.run = ""
			n++
		}
	}
	return n, nil
}

func (f *fakeEvents) PruneRoutineEvents(_ context.Context, pendingBefore, _ time.Time) (int, error) {
	f.st.mu.Lock()
	defer f.st.mu.Unlock()
	kept, n := f.events[:0], 0
	for _, e := range f.events {
		if e.run == "" && e.at.Before(pendingBefore) {
			n++
			continue
		}
		kept = append(kept, e)
	}
	f.events = kept
	return n, nil
}

func (f *fakeEvents) CountRoutineRunsSince(_ context.Context, routine, trigger string, since time.Time) (int, error) {
	f.st.mu.Lock()
	defer f.st.mu.Unlock()
	n := 0
	for _, r := range f.st.runs {
		if r.RoutineID == routine && r.Trigger == trigger && !r.ScheduledFor.Before(since) {
			n++
		}
	}
	return n, nil
}

func (f *fakeEvents) DeleteUnstartedRoutineRun(_ context.Context, run string) error {
	f.st.mu.Lock()
	defer f.st.mu.Unlock()
	delete(f.st.runs, run)
	return nil
}

func newEventScheduler(t *testing.T, st *fakeStore, ev *fakeEvents, runner *fakeRunner, now time.Time) *Scheduler {
	t.Helper()
	s, err := New(Options{Store: st, Events: ev, Runner: runner, Now: func() time.Time { return now },
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	require.NoError(t, err)
	return s
}

func triage() store.Routine {
	return store.Routine{
		ID: "rtn_triage", Scope: store.RoutineScopeSystem, Name: "Triage", Kind: store.RoutineKindLinear,
		Events: []string{"Issue.create"}, Timezone: "UTC", Instruction: "Label each new issue.",
		Enabled: true, ChatID: "chat_triage", NextRunAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
}

func TestALinearRoutineRunsWithTheOldestEventsWaitingTwentyAtATime(t *testing.T) {
	now := time.Date(2026, 11, 9, 9, 0, 0, 0, time.UTC)
	st, runner := newFakeStore(), &fakeRunner{}
	ev := &fakeEvents{st: st}
	st.routines["rtn_triage"] = triage()
	for i := range MaxEventsPerRun + 5 {
		ev.add("rtn_triage", now.Add(-time.Hour+time.Duration(i)*time.Second))
	}
	newEventScheduler(t, st, ev, runner, now).Tick(context.Background())

	require.Len(t, runner.runs, 1)
	got := runner.runs[0]
	assert.Equal(t, "chat_triage", got.ChatID)
	assert.True(t, got.System)
	assert.Equal(t, store.RoutineTriggerEvent, st.runs[got.RunID].Trigger)
	given := ev.given(got.RunID)
	require.Len(t, given, MaxEventsPerRun)
	assert.Equal(t, 1, given[0].id, "oldest first")
	assert.Equal(t, 5, ev.waiting("rtn_triage"), "the rest wait for the next run")
	// It is never fired as a schedule.
	assert.Equal(t, triage().NextRunAt, st.routines["rtn_triage"].NextRunAt)
}

func TestALinearRoutineWithNothingWaitingDoesNotRun(t *testing.T) {
	now := time.Date(2026, 11, 9, 9, 0, 0, 0, time.UTC)
	st, runner := newFakeStore(), &fakeRunner{}
	st.routines["rtn_triage"] = triage()
	newEventScheduler(t, st, &fakeEvents{st: st}, runner, now).Tick(context.Background())
	assert.Empty(t, runner.runs)
	assert.Empty(t, st.runs, "a linear routine is never due, however old its next_run_at")
}

func TestABusyChatLeavesTheEventsWaitingRatherThanSkippingThem(t *testing.T) {
	now := time.Date(2026, 11, 9, 9, 0, 0, 0, time.UTC)
	st, runner := newFakeStore(), &fakeRunner{}
	ev := &fakeEvents{st: st}
	st.routines["rtn_triage"] = triage()
	ev.add("rtn_triage", now.Add(-time.Minute))
	st.busy["sess:"+store.ChatSourceKey("chat_triage")] = true
	newEventScheduler(t, st, ev, runner, now).Tick(context.Background())

	assert.Empty(t, runner.runs)
	assert.Empty(t, st.runs, "nothing is recorded as skipped: the event is still to do")
	assert.Equal(t, 1, ev.waiting("rtn_triage"))

	st.busy["sess:"+store.ChatSourceKey("chat_triage")] = false
	newEventScheduler(t, st, ev, runner, now.Add(15*time.Second)).Tick(context.Background())
	require.Len(t, runner.runs, 1)
	assert.Zero(t, ev.waiting("rtn_triage"))
}

func TestARunThatCannotStartGivesItsEventsBack(t *testing.T) {
	now := time.Date(2026, 11, 9, 9, 0, 0, 0, time.UTC)
	for name, startErr := range map[string]error{
		"busy here": chat.ErrChatBusy,
		"refused":   errors.New("chat chat_triage is gone"),
	} {
		t.Run(name, func(t *testing.T) {
			st, runner := newFakeStore(), &fakeRunner{err: startErr}
			ev := &fakeEvents{st: st}
			st.routines["rtn_triage"] = triage()
			ev.add("rtn_triage", now.Add(-time.Minute))
			ev.add("rtn_triage", now.Add(-time.Second))
			newEventScheduler(t, st, ev, runner, now).Tick(context.Background())

			assert.Equal(t, 2, ev.waiting("rtn_triage"), "no event is lost to a start that failed")
			require.Len(t, st.runs, 1)
			for _, run := range st.runs {
				assert.NotEqual(t, store.RoutineRunRunning, run.Status)
			}
		})
	}
}

func TestARunWhoseTurnNeverStartedGivesItsEventsBack(t *testing.T) {
	now := time.Date(2026, 11, 9, 9, 30, 0, 0, time.UTC)
	st := newFakeStore()
	ev := &fakeEvents{st: st}
	r := triage()
	st.routines[r.ID] = r
	st.runs["rrun_1"] = store.RoutineRun{ID: "rrun_1", RoutineID: r.ID, Trigger: store.RoutineTriggerEvent,
		Status: store.RoutineRunRunning, CreatedAt: now.Add(-neverStarted - time.Minute), ScheduledFor: now.Add(-neverStarted - time.Minute)}
	ev.add(r.ID, now.Add(-time.Hour))
	ev.events[0].run = "rrun_1"
	// The routine's chat is busy, so the events are only given back this tick, not run again.
	st.busy["sess:"+store.ChatSourceKey(r.ChatID)] = true
	newEventScheduler(t, st, ev, &fakeRunner{}, now).Tick(context.Background())

	assert.Equal(t, store.RoutineRunFailed, st.runs["rrun_1"].Status)
	assert.Equal(t, 1, ev.waiting(r.ID))
}

func TestEventsWaitAtMostADayAndTwoHundredDeep(t *testing.T) {
	now := time.Date(2026, 11, 9, 9, 0, 0, 0, time.UTC)
	st, runner := newFakeStore(), &fakeRunner{}
	ev := &fakeEvents{st: st}
	st.routines["rtn_triage"] = triage()
	ev.add("rtn_triage", now.Add(-PendingEventTTL-time.Minute))
	for i := range MaxPendingEvents + 10 {
		ev.add("rtn_triage", now.Add(-time.Hour+time.Duration(i)*time.Second))
	}
	newEventScheduler(t, st, ev, runner, now).Tick(context.Background())

	require.Len(t, runner.runs, 1)
	given := ev.given(runner.runs[0].RunID)
	require.Len(t, given, MaxEventsPerRun)
	// The day-old one went, then the ten oldest beyond the cap.
	assert.Equal(t, 12, given[0].id)
	assert.Equal(t, MaxPendingEvents-MaxEventsPerRun, ev.waiting("rtn_triage"))
}

func TestALinearRoutineThatRunsTooOftenPausesItself(t *testing.T) {
	now := time.Date(2026, 11, 9, 9, 0, 0, 0, time.UTC)
	st, runner := newFakeStore(), &fakeRunner{}
	ev := &fakeEvents{st: st}
	r := triage()
	r.Scope, r.OwnerLogin = store.RoutineScopePersonal, "ana@acme.com"
	st.routines[r.ID] = r
	for i := range MaxEventRunsPerHour {
		id := fmt.Sprintf("rrun_%d", i)
		st.runs[id] = store.RoutineRun{ID: id, RoutineID: r.ID, Trigger: store.RoutineTriggerEvent,
			Status: store.RoutineRunDone, ScheduledFor: now.Add(-time.Duration(i) * time.Second)}
	}
	ev.add(r.ID, now.Add(-time.Second))
	newEventScheduler(t, st, ev, runner, now).Tick(context.Background())

	assert.Empty(t, runner.runs)
	got := st.routines[r.ID]
	assert.False(t, got.Enabled)
	assert.Contains(t, got.PausedReason, "60 runs from Linear events within an hour")
	require.Len(t, st.notices, 1)
	assert.Contains(t, st.notices[0], "ana@acme.com|60 runs from Linear events")
	assert.Equal(t, 1, ev.waiting(r.ID), "a paused routine's events wait, and a day later are dropped")
}

func TestALinearRoutineOverItsCapLeavesItsEventsWaiting(t *testing.T) {
	now := time.Date(2026, 11, 9, 9, 0, 0, 0, time.UTC)
	st, runner := newFakeStore(), &fakeRunner{}
	ev := &fakeEvents{st: st}
	r := triage()
	capUSD := 1.0
	r.DailyCapUSD = &capUSD
	st.routines[r.ID] = r
	st.spend[r.ID] = 2
	ev.add(r.ID, now.Add(-time.Minute))
	newEventScheduler(t, st, ev, runner, now).Tick(context.Background())

	assert.Empty(t, runner.runs)
	assert.Empty(t, st.runs)
	assert.Equal(t, 1, ev.waiting(r.ID))
	got := st.routines[r.ID]
	assert.False(t, got.Enabled)
	require.NotNil(t, got.PausedUntil)

	// After midnight it resumes without a schedule to read, and the events run.
	st.spend[r.ID] = 0
	newEventScheduler(t, st, ev, runner, got.PausedUntil.Add(time.Minute)).Tick(context.Background())
	assert.True(t, st.routines[r.ID].Enabled)
	require.Len(t, runner.runs, 1)
}

func TestRunNowOnALinearRoutineRunsWithNoEvents(t *testing.T) {
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	st, runner := newFakeStore(), &fakeRunner{}
	ev := &fakeEvents{st: st}
	st.routines["rtn_triage"] = triage()
	ev.add("rtn_triage", now.Add(-time.Minute))
	run, err := newEventScheduler(t, st, ev, runner, now).RunNow(context.Background(), "rtn_triage")
	require.NoError(t, err)
	assert.Equal(t, store.RoutineTriggerManual, run.Trigger)
	assert.Empty(t, ev.given(run.ID))
	assert.Equal(t, 1, ev.waiting("rtn_triage"))
}
