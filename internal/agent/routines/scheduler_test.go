package routines

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/agent/chat"
	"github.com/podium-ade/podium/internal/agent/store"
)

// fakeStore is the scheduler's database in memory, keyed the way the real one is.
type fakeStore struct {
	mu       sync.Mutex
	routines map[string]store.Routine
	runs     map[string]store.RoutineRun
	claimed  map[string]bool // routine|scheduled_for|trigger
	turns    map[string]store.Turn
	busy     map[string]bool // session id
	members  map[string]bool
	idle     map[string]int
	seq      int
	// spend is each routine's spend since any moment; personSpend each owner's.
	spend       map[string]float64
	personSpend map[string]float64
	limits      store.RoutineLimits
	notices     []string // login|detail
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		routines: map[string]store.Routine{}, runs: map[string]store.RoutineRun{}, claimed: map[string]bool{},
		turns: map[string]store.Turn{}, busy: map[string]bool{}, members: map[string]bool{"ana@acme.com": true},
		idle: map[string]int{}, spend: map[string]float64{}, personSpend: map[string]float64{},
		limits: store.DefaultRoutineLimits,
	}
}

func (f *fakeStore) RoutineSpendSince(_ context.Context, id string, _ time.Time) (float64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.spend[id], nil
}

func (f *fakeStore) PersonRoutineSpendSince(_ context.Context, owner string, _ time.Time) (float64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.personSpend[owner], nil
}

func (f *fakeStore) GetRoutineLimits(context.Context) (store.RoutineLimits, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.limits, nil
}

func (f *fakeStore) PauseRoutineUntil(_ context.Context, id, reason string, until, next time.Time) (store.Routine, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r := f.routines[id]
	r.Enabled, r.PausedReason, r.PausedUntil, r.NextRunAt = false, reason, &until, next
	f.routines[id] = r
	return r, nil
}

func (f *fakeStore) DueToResume(_ context.Context, now time.Time, _ int) ([]store.Routine, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []store.Routine
	for _, r := range f.routines {
		if !r.Enabled && r.PausedUntil != nil && !r.PausedUntil.After(now) {
			out = append(out, r)
		}
	}
	return out, nil
}

func (f *fakeStore) NotifyRoutine(_ context.Context, login, _, _, detail string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.notices = append(f.notices, login+"|"+detail)
	return nil
}

func (f *fakeStore) DueRoutines(_ context.Context, now time.Time, _ int) ([]store.Routine, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []store.Routine
	for _, r := range f.routines {
		if r.Enabled && !r.NextRunAt.After(now) {
			out = append(out, r)
		}
	}
	return out, nil
}

func (f *fakeStore) GetRoutine(_ context.Context, id string) (store.Routine, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.routines[id]
	if !ok {
		return store.Routine{}, store.ErrNotFound
	}
	return r, nil
}

func (f *fakeStore) AdvanceRoutine(_ context.Context, id string, next time.Time, last *time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	r := f.routines[id]
	r.NextRunAt, r.LastRunAt = next, last
	f.routines[id] = r
	return nil
}

func (f *fakeStore) SetRoutineEnabled(_ context.Context, id string, enabled bool, reason string, next time.Time) (store.Routine, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r := f.routines[id]
	r.Enabled, r.PausedReason, r.NextRunAt = enabled, reason, next
	if enabled {
		r.PausedUntil = nil
	}
	f.routines[id] = r
	return r, nil
}

func (f *fakeStore) ClaimRoutineRun(_ context.Context, routineID string, at time.Time, trigger, status string) (store.RoutineRun, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := fmt.Sprintf("%s|%s|%s", routineID, at.Format(time.RFC3339Nano), trigger)
	if f.claimed[key] {
		return store.RoutineRun{}, false, nil
	}
	f.claimed[key] = true
	f.seq++
	run := store.RoutineRun{ID: fmt.Sprintf("rrun_%d", f.seq), RoutineID: routineID, ScheduledFor: at,
		Trigger: trigger, Status: status, CreatedAt: time.Now()}
	f.runs[run.ID] = run
	return run, true, nil
}

func (f *fakeStore) SettleRoutineRun(_ context.Context, id, status, summary string, cost *float64) (store.RoutineRun, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	run, ok := f.runs[id]
	if !ok || run.Status != store.RoutineRunRunning {
		return store.RoutineRun{}, false, nil
	}
	run.Status, run.CostUSD = status, cost
	if summary != "" {
		run.Summary = summary
	}
	f.runs[id] = run
	return run, true, nil
}

func (f *fakeStore) RunningRoutineRuns(context.Context) ([]store.RoutineRun, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []store.RoutineRun
	for _, r := range f.runs {
		if r.Status == store.RoutineRunRunning {
			out = append(out, r)
		}
	}
	return out, nil
}

func (f *fakeStore) TurnForRoutineRun(_ context.Context, runID string) (store.Turn, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.turns[runID]
	if !ok {
		return store.Turn{}, store.ErrNotFound
	}
	return t, nil
}

func (f *fakeStore) NoteRoutineIdle(_ context.Context, id string, idle bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if idle {
		f.idle[id]++
	} else {
		f.idle[id] = 0
	}
	return nil
}

func (f *fakeStore) RoutineFailuresSince(_ context.Context, routineID string, since time.Time) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, r := range f.runs {
		if r.RoutineID == routineID && r.Status == store.RoutineRunFailed && !r.CreatedAt.Before(since) {
			n++
		}
	}
	return n, nil
}

func (f *fakeStore) GetSessionByKey(_ context.Context, key string) (store.Session, error) {
	return store.Session{ID: "sess:" + key}, nil
}

func (f *fakeStore) SessionBusy(_ context.Context, id string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.busy[id], nil
}

func (f *fakeStore) CanonicalWebLogin(_ context.Context, login string) (string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return login, f.members[login], nil
}

type fakeRunner struct {
	mu   sync.Mutex
	runs []chat.RoutineRun
	err  error
}

func (r *fakeRunner) RunRoutine(_ context.Context, run chat.RoutineRun) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return r.err
	}
	r.runs = append(r.runs, run)
	return nil
}

func newScheduler(t *testing.T, st *fakeStore, runner *fakeRunner, now time.Time) *Scheduler {
	t.Helper()
	s, err := New(Options{Store: st, Runner: runner, Now: func() time.Time { return now },
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	require.NoError(t, err)
	return s
}

func digest(next time.Time) store.Routine {
	return store.Routine{
		ID: "rtn_digest", Scope: store.RoutineScopePersonal, OwnerLogin: "ana@acme.com", Name: "Digest",
		Schedule: "0 9 * * *", Timezone: "UTC", Instruction: "Summarise yesterday's merged PRs.",
		Model: "claude-sonnet-5-5", Enabled: true, ChatID: "chat_digest", NextRunAt: next,
	}
}

func TestADueRoutineRunsInItsChatAndMovesToItsNextTime(t *testing.T) {
	now := time.Date(2026, 11, 9, 9, 0, 30, 0, time.UTC)
	st, runner := newFakeStore(), &fakeRunner{}
	st.routines["rtn_digest"] = digest(now.Add(-30 * time.Second))
	newScheduler(t, st, runner, now).Tick(context.Background())

	require.Len(t, runner.runs, 1)
	got := runner.runs[0]
	assert.Equal(t, "chat_digest", got.ChatID)
	assert.Equal(t, "ana@acme.com", got.Login)
	assert.Equal(t, "Summarise yesterday's merged PRs.", got.Instruction)
	assert.Equal(t, "claude-sonnet-5-5", got.Override.Model)
	assert.Equal(t, store.RoutineRunRunning, st.runs[got.RunID].Status)
	next := st.routines["rtn_digest"].NextRunAt
	assert.Equal(t, time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC).Add(Jitter("rtn_digest")), next)

	// The next tick does not fire it again: it is not due.
	newScheduler(t, st, runner, now.Add(time.Minute)).Tick(context.Background())
	assert.Len(t, runner.runs, 1)
}

func TestARoutineMissedForHoursRunsOnceNotOncePerMissedTime(t *testing.T) {
	now := time.Date(2026, 11, 9, 15, 0, 0, 0, time.UTC)
	st, runner := newFakeStore(), &fakeRunner{}
	r := digest(now.Add(-6 * time.Hour))
	r.Schedule = "0 * * * *"
	st.routines[r.ID] = r
	s := newScheduler(t, st, runner, now)
	s.Tick(context.Background())
	s.Tick(context.Background())
	assert.Len(t, runner.runs, 1)
	assert.True(t, st.routines[r.ID].NextRunAt.After(now))
}

func TestABusyChatSkipsTheRunInsteadOfQueueingIt(t *testing.T) {
	now := time.Date(2026, 11, 9, 9, 0, 30, 0, time.UTC)
	st, runner := newFakeStore(), &fakeRunner{}
	st.routines["rtn_digest"] = digest(now.Add(-30 * time.Second))
	st.busy["sess:"+store.ChatSourceKey("chat_digest")] = true
	newScheduler(t, st, runner, now).Tick(context.Background())

	assert.Empty(t, runner.runs)
	require.Len(t, st.runs, 1)
	for _, run := range st.runs {
		assert.Equal(t, store.RoutineRunSkippedOverlap, run.Status)
	}
}

func TestRunsSettleFromTheirTurnAndWhatTheAgentReported(t *testing.T) {
	now := time.Date(2026, 11, 9, 9, 0, 30, 0, time.UTC)
	cost := 0.02
	for _, tc := range []struct {
		name, turn, reported, want string
	}{
		{"idle", store.TurnSucceeded, store.RoutineRunIdle, store.RoutineRunIdle},
		{"done", store.TurnSucceeded, store.RoutineRunDone, store.RoutineRunDone},
		{"unreported", store.TurnSucceeded, "", store.RoutineRunDone},
		{"stopped", store.TurnCancelled, "", store.RoutineRunDone},
		{"failed", store.TurnFailed, "", store.RoutineRunFailed},
		{"running", store.TurnRunning, "", store.RoutineRunRunning},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := newFakeStore()
			st.routines["rtn_digest"] = digest(now.Add(time.Hour))
			st.runs["rrun_1"] = store.RoutineRun{ID: "rrun_1", RoutineID: "rtn_digest", Status: store.RoutineRunRunning,
				Reported: tc.reported, CreatedAt: now.Add(-time.Minute)}
			st.turns["rrun_1"] = store.Turn{ID: "turn_1", Status: tc.turn, CostUSD: &cost}
			newScheduler(t, st, &fakeRunner{}, now).Tick(context.Background())
			assert.Equal(t, tc.want, st.runs["rrun_1"].Status)
			if tc.want == store.RoutineRunIdle {
				assert.Equal(t, 1, st.idle["rtn_digest"])
			}
		})
	}
}

func TestARunWhoseTurnNeverStartedFailsAfterAWhile(t *testing.T) {
	now := time.Date(2026, 11, 9, 9, 30, 0, 0, time.UTC)
	st := newFakeStore()
	st.routines["rtn_digest"] = digest(now.Add(time.Hour))
	st.runs["rrun_1"] = store.RoutineRun{ID: "rrun_1", RoutineID: "rtn_digest", Status: store.RoutineRunRunning,
		CreatedAt: now.Add(-2 * time.Minute)}
	s := newScheduler(t, st, &fakeRunner{}, now)
	s.Tick(context.Background())
	assert.Equal(t, store.RoutineRunRunning, st.runs["rrun_1"].Status, "two minutes is not long enough to give up")

	newScheduler(t, st, &fakeRunner{}, now.Add(neverStarted)).Tick(context.Background())
	assert.Equal(t, store.RoutineRunFailed, st.runs["rrun_1"].Status)
	assert.Equal(t, "The run never started.", st.runs["rrun_1"].Summary)
}

func TestARoutineThatKeepsFailingPausesItself(t *testing.T) {
	now := time.Now().UTC()
	st := newFakeStore()
	st.routines["rtn_digest"] = digest(now.Add(time.Hour))
	for i := range MaxFailures {
		id := fmt.Sprintf("rrun_%d", i)
		st.runs[id] = store.RoutineRun{ID: id, RoutineID: "rtn_digest", Status: store.RoutineRunRunning, CreatedAt: now.Add(-time.Minute)}
		st.turns[id] = store.Turn{Status: store.TurnFailed}
	}
	newScheduler(t, st, &fakeRunner{}, now).Tick(context.Background())
	r := st.routines["rtn_digest"]
	assert.False(t, r.Enabled)
	assert.Contains(t, r.PausedReason, "runs failed within an hour")
}

func TestARoutineWhoseOwnerLeftPausesInsteadOfRunning(t *testing.T) {
	now := time.Date(2026, 11, 9, 9, 0, 30, 0, time.UTC)
	st, runner := newFakeStore(), &fakeRunner{}
	st.routines["rtn_digest"] = digest(now.Add(-30 * time.Second))
	delete(st.members, "ana@acme.com")
	newScheduler(t, st, runner, now).Tick(context.Background())
	assert.Empty(t, runner.runs)
	assert.False(t, st.routines["rtn_digest"].Enabled)
	assert.Contains(t, st.routines["rtn_digest"].PausedReason, "no longer a Podium member")
}

func TestRunNowStartsAManualRunAndRefusesABusyChat(t *testing.T) {
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	st, runner := newFakeStore(), &fakeRunner{}
	st.routines["rtn_digest"] = digest(now.Add(time.Hour))
	s := newScheduler(t, st, runner, now)

	run, err := s.RunNow(context.Background(), "rtn_digest")
	require.NoError(t, err)
	assert.Equal(t, store.RoutineTriggerManual, run.Trigger)
	require.Len(t, runner.runs, 1)

	runner.err = chat.ErrChatBusy
	_, err = newScheduler(t, st, runner, now.Add(time.Second)).RunNow(context.Background(), "rtn_digest")
	assert.ErrorIs(t, err, ErrBusy)
}

func TestASystemRoutineRunsAsNobody(t *testing.T) {
	now := time.Date(2026, 11, 9, 9, 0, 30, 0, time.UTC)
	st, runner := newFakeStore(), &fakeRunner{}
	r := digest(now.Add(-30 * time.Second))
	r.Scope, r.OwnerLogin = store.RoutineScopeSystem, ""
	st.routines[r.ID] = r
	newScheduler(t, st, runner, now).Tick(context.Background())

	require.Len(t, runner.runs, 1)
	assert.True(t, runner.runs[0].System)
	assert.Empty(t, runner.runs[0].Login)
}

func TestARoutineAtItsDailyCapPausesUntilMidnightInItsTimezoneAndTellsItsOwner(t *testing.T) {
	// 22:00 in Toronto is 03:00 UTC the next day: the day that ends is Toronto's.
	now := time.Date(2026, 11, 10, 3, 0, 30, 0, time.UTC)
	st, runner := newFakeStore(), &fakeRunner{}
	r := digest(now.Add(-30 * time.Second))
	r.Schedule, r.Timezone = "0 * * * *", "America/Toronto"
	capUSD := 1.0
	r.DailyCapUSD = &capUSD
	st.routines[r.ID] = r
	st.spend[r.ID] = 1.25
	newScheduler(t, st, runner, now).Tick(context.Background())

	assert.Empty(t, runner.runs)
	got := st.routines[r.ID]
	assert.False(t, got.Enabled)
	assert.Contains(t, got.PausedReason, "daily spend cap of $1.00")
	loc, _ := time.LoadLocation("America/Toronto")
	require.NotNil(t, got.PausedUntil)
	assert.True(t, got.PausedUntil.Equal(time.Date(2026, 11, 10, 0, 0, 0, 0, loc)), "%s", got.PausedUntil)
	require.Len(t, st.notices, 1)
	assert.Contains(t, st.notices[0], "ana@acme.com|Reached its daily spend cap")
	for _, run := range st.runs {
		assert.Equal(t, store.RoutineRunSkippedPaused, run.Status)
	}

	// After midnight there it resumes by itself, and runs at its next time.
	st.spend[r.ID] = 0
	later := got.PausedUntil.Add(time.Minute)
	newScheduler(t, st, runner, later).Tick(context.Background())
	assert.True(t, st.routines[r.ID].Enabled)
	assert.Nil(t, st.routines[r.ID].PausedUntil)
	assert.True(t, st.routines[r.ID].NextRunAt.After(later))
}

func TestAPersonOverTheirDailyCapHasTheirRoutinesSkippedNotPaused(t *testing.T) {
	now := time.Date(2026, 11, 9, 9, 0, 30, 0, time.UTC)
	st, runner := newFakeStore(), &fakeRunner{}
	st.routines["rtn_digest"] = digest(now.Add(-30 * time.Second))
	st.limits.PersonDailyCapUSD = 5
	st.personSpend["ana@acme.com"] = 5.5
	newScheduler(t, st, runner, now).Tick(context.Background())

	assert.Empty(t, runner.runs)
	assert.True(t, st.routines["rtn_digest"].Enabled, "a person's cap rolls on; it does not pause the routine")
	assert.Empty(t, st.notices)
}

func TestAutoPauseTellsTheOwner(t *testing.T) {
	now := time.Date(2026, 11, 9, 9, 0, 30, 0, time.UTC)
	st, runner := newFakeStore(), &fakeRunner{}
	r := digest(now.Add(-30 * time.Second))
	st.members = map[string]bool{}
	st.routines[r.ID] = r
	newScheduler(t, st, runner, now).Tick(context.Background())
	assert.False(t, st.routines[r.ID].Enabled)
	require.Len(t, st.notices, 1)
	assert.Contains(t, st.notices[0], "no longer a Podium member")
}
