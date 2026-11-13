package routines

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/agent/store"
)

// fakeSubjects is the store's chats per ticket in memory, beside a fakeEvents, under the
// fakeStore's lock. about is what each event is about, by its id; an event missing from it is
// about nothing in particular.
type fakeSubjects struct {
	ev     *fakeEvents
	about  map[int]string
	chats  map[string]string    // routine|subject -> chat
	opened map[string]time.Time // chat -> when it was opened
	// openedBefore is how many chats each routine opened earlier in the last 24 hours.
	openedBefore map[string]int
	runChats     map[string]string // run -> chat
}

func newFakeSubjects(ev *fakeEvents) *fakeSubjects {
	return &fakeSubjects{
		ev: ev, about: map[int]string{}, chats: map[string]string{}, opened: map[string]time.Time{},
		openedBefore: map[string]int{}, runChats: map[string]string{},
	}
}

// add is an event for routine about subject ("" for nothing in particular).
func (f *fakeSubjects) add(routine, subject string, at time.Time) int {
	f.ev.add(routine, at)
	f.ev.st.mu.Lock()
	defer f.ev.st.mu.Unlock()
	if subject != "" {
		f.about[f.ev.seq] = subject
	}
	return f.ev.seq
}

func (f *fakeSubjects) PendingRoutineSubjects(_ context.Context, routine string) ([]store.RoutineSubject, error) {
	f.ev.st.mu.Lock()
	defer f.ev.st.mu.Unlock()
	by := map[string]*store.RoutineSubject{}
	for _, e := range f.ev.pending(routine) {
		key := f.about[e.id]
		sub, ok := by[key]
		if !ok {
			sub = &store.RoutineSubject{Key: key, Title: key, Oldest: e.at, ChatID: f.chats[routine+"|"+key]}
			by[key] = sub
		}
		sub.Pending++
	}
	out := make([]store.RoutineSubject, 0, len(by))
	for _, sub := range by {
		out = append(out, *sub)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Oldest.Before(out[j].Oldest) })
	return out, nil
}

func (f *fakeSubjects) AttachRoutineSubjectEvents(_ context.Context, run, routine, subject string, limit int) (int, error) {
	f.ev.st.mu.Lock()
	defer f.ev.st.mu.Unlock()
	n := 0
	for _, e := range f.ev.pending(routine) {
		if f.about[e.id] != subject || n == limit {
			continue
		}
		e.run = run
		n++
	}
	return n, nil
}

func (f *fakeSubjects) EnsureRoutineSubjectChat(_ context.Context, r store.Routine, subject, _ string) (string, bool, error) {
	f.ev.st.mu.Lock()
	defer f.ev.st.mu.Unlock()
	if subject == "" {
		return r.ChatID, false, nil
	}
	if id, ok := f.chats[r.ID+"|"+subject]; ok {
		return id, false, nil
	}
	id := "chat_" + subject
	f.chats[r.ID+"|"+subject] = id
	f.opened[id] = time.Now()
	return id, true, nil
}

func (f *fakeSubjects) CountRoutineSubjectChatsSince(_ context.Context, routine string, _ time.Time) (int, error) {
	f.ev.st.mu.Lock()
	defer f.ev.st.mu.Unlock()
	n := f.openedBefore[routine]
	for key := range f.chats {
		if len(key) > len(routine) && key[:len(routine)+1] == routine+"|" {
			n++
		}
	}
	return n, nil
}

func (f *fakeSubjects) SetRoutineRunChat(_ context.Context, run, chatID string) error {
	f.ev.st.mu.Lock()
	defer f.ev.st.mu.Unlock()
	f.runChats[run] = chatID
	return nil
}

func (f *fakeSubjects) RunningRoutineRun(_ context.Context, routine string) (store.RoutineRun, error) {
	f.ev.st.mu.Lock()
	defer f.ev.st.mu.Unlock()
	for _, run := range f.ev.st.runs {
		if run.RoutineID == routine && run.Status == store.RoutineRunRunning {
			return run, nil
		}
	}
	return store.RoutineRun{}, fmt.Errorf("%w: a running run of routine %s", store.ErrNotFound, routine)
}

func newSubjectScheduler(t *testing.T, sub *fakeSubjects, runner *fakeRunner, now time.Time) *Scheduler {
	t.Helper()
	s, err := New(Options{Store: sub.ev.st, Events: sub.ev, Subjects: sub, Runner: runner,
		Now: func() time.Time { return now }, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	require.NoError(t, err)
	return s
}

// perTicket is the triage routine with a chat per ticket.
func perTicket() store.Routine {
	r := triage()
	r.ChatMode = store.RoutineChatModeSubject
	return r
}

// finish settles a run as done, as the tick does once its turn has ended.
func finish(st *fakeStore, runID string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	run := st.runs[runID]
	run.Status = store.RoutineRunDone
	st.runs[runID] = run
}

func TestAChatPerTicketRunsEachTicketInItsOwnChatOneRunAtATime(t *testing.T) {
	now := time.Date(2026, 11, 9, 9, 0, 0, 0, time.UTC)
	st, runner := newFakeStore(), &fakeRunner{}
	sub := newFakeSubjects(&fakeEvents{st: st})
	st.routines["rtn_triage"] = perTicket()
	first := sub.add("rtn_triage", "Issue:a", now.Add(-3*time.Minute))
	sub.add("rtn_triage", "Issue:b", now.Add(-2*time.Minute))
	third := sub.add("rtn_triage", "Issue:a", now.Add(-time.Minute))

	newSubjectScheduler(t, sub, runner, now).Tick(context.Background())
	require.Len(t, runner.runs, 1, "one run of the routine at a time")
	got := runner.runs[0]
	assert.Equal(t, "chat_Issue:a", got.ChatID, "the ticket that waited longest, in its own chat")
	assert.Equal(t, "chat_Issue:a", sub.runChats[got.RunID])
	given := sub.ev.given(got.RunID)
	require.Len(t, given, 2, "every waiting event about that ticket, and nothing else")
	assert.Equal(t, []int{first, third}, []int{given[0].id, given[1].id})
	assert.Equal(t, 1, sub.ev.waiting("rtn_triage"))

	// While that run goes, the next ticket waits.
	newSubjectScheduler(t, sub, runner, now.Add(15*time.Second)).Tick(context.Background())
	require.Len(t, runner.runs, 1)

	finish(st, got.RunID)
	newSubjectScheduler(t, sub, runner, now.Add(30*time.Second)).Tick(context.Background())
	require.Len(t, runner.runs, 2)
	assert.Equal(t, "chat_Issue:b", runner.runs[1].ChatID)
	assert.Zero(t, sub.ev.waiting("rtn_triage"))

	// The next event about the first ticket goes to the chat it already has.
	finish(st, runner.runs[1].RunID)
	sub.add("rtn_triage", "Issue:a", now.Add(40*time.Second))
	newSubjectScheduler(t, sub, runner, now.Add(45*time.Second)).Tick(context.Background())
	require.Len(t, runner.runs, 3)
	assert.Equal(t, "chat_Issue:a", runner.runs[2].ChatID)
	assert.Len(t, sub.chats, 2)
}

func TestABusyTicketChatIsPassedOverForTheNextTicket(t *testing.T) {
	now := time.Date(2026, 11, 9, 9, 0, 0, 0, time.UTC)
	st, runner := newFakeStore(), &fakeRunner{}
	sub := newFakeSubjects(&fakeEvents{st: st})
	st.routines["rtn_triage"] = perTicket()
	sub.chats["rtn_triage|Issue:a"] = "chat_a"
	st.busy["sess:"+store.ChatSourceKey("chat_a")] = true
	sub.add("rtn_triage", "Issue:a", now.Add(-2*time.Minute))
	sub.add("rtn_triage", "Issue:b", now.Add(-time.Minute))

	newSubjectScheduler(t, sub, runner, now).Tick(context.Background())
	require.Len(t, runner.runs, 1)
	assert.Equal(t, "chat_Issue:b", runner.runs[0].ChatID)
	assert.Equal(t, 1, sub.ev.waiting("rtn_triage"), "the busy ticket's event waits for its chat")
}

func TestAnEventAboutNoTicketRunsInTheRoutinesOwnChat(t *testing.T) {
	now := time.Date(2026, 11, 9, 9, 0, 0, 0, time.UTC)
	st, runner := newFakeStore(), &fakeRunner{}
	sub := newFakeSubjects(&fakeEvents{st: st})
	st.routines["rtn_triage"] = perTicket()
	sub.add("rtn_triage", "", now.Add(-time.Minute))

	newSubjectScheduler(t, sub, runner, now).Tick(context.Background())
	require.Len(t, runner.runs, 1)
	assert.Equal(t, "chat_triage", runner.runs[0].ChatID)
	assert.Empty(t, sub.chats, "no chat is opened for it")
	assert.Empty(t, sub.runChats)
}

func TestARoutineThatOpensTooManyTicketChatsPausesItself(t *testing.T) {
	now := time.Date(2026, 11, 9, 9, 0, 0, 0, time.UTC)
	st, runner := newFakeStore(), &fakeRunner{}
	sub := newFakeSubjects(&fakeEvents{st: st})
	r := perTicket()
	r.Scope, r.OwnerLogin = store.RoutineScopePersonal, "ana@acme.com"
	st.routines[r.ID] = r
	sub.openedBefore[r.ID] = MaxSubjectChatsPerDay
	sub.add(r.ID, "Issue:new", now.Add(-time.Minute))

	newSubjectScheduler(t, sub, runner, now).Tick(context.Background())
	assert.Empty(t, runner.runs)
	got := st.routines[r.ID]
	assert.False(t, got.Enabled)
	assert.Contains(t, got.PausedReason, "50 chats, one per ticket, within 24 hours")
	require.Len(t, st.notices, 1)
	assert.Equal(t, 1, sub.ev.waiting(r.ID))
}

func TestATicketWithAChatAlreadyRunsPastTheChatCap(t *testing.T) {
	now := time.Date(2026, 11, 9, 9, 0, 0, 0, time.UTC)
	st, runner := newFakeStore(), &fakeRunner{}
	sub := newFakeSubjects(&fakeEvents{st: st})
	st.routines["rtn_triage"] = perTicket()
	sub.openedBefore["rtn_triage"] = MaxSubjectChatsPerDay
	sub.chats["rtn_triage|Issue:a"] = "chat_a"
	sub.add("rtn_triage", "Issue:a", now.Add(-time.Minute))

	newSubjectScheduler(t, sub, runner, now).Tick(context.Background())
	require.Len(t, runner.runs, 1, "the cap is on new chats; a ticket that has one is answered there")
	assert.Equal(t, "chat_a", runner.runs[0].ChatID)
	assert.True(t, st.routines["rtn_triage"].Enabled)
}

func TestRunNowOnAPerTicketRoutineRunsInItsOwnChatOnlyWhenNoRunIsGoing(t *testing.T) {
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	st, runner := newFakeStore(), &fakeRunner{}
	sub := newFakeSubjects(&fakeEvents{st: st})
	st.routines["rtn_triage"] = perTicket()
	sub.add("rtn_triage", "Issue:a", now.Add(-time.Minute))

	s := newSubjectScheduler(t, sub, runner, now)
	run, err := s.RunNow(context.Background(), "rtn_triage")
	require.NoError(t, err)
	assert.Equal(t, store.RoutineTriggerManual, run.Trigger)
	assert.Equal(t, "chat_triage", runner.runs[0].ChatID)
	assert.Empty(t, sub.ev.given(run.ID))

	// A run by hand is a run of the routine: the ticket's events wait for it to end.
	s.Tick(context.Background())
	require.Len(t, runner.runs, 1)

	// And a run going in a ticket's chat refuses another by hand.
	finish(st, run.ID)
	newSubjectScheduler(t, sub, runner, now.Add(15*time.Second)).Tick(context.Background())
	require.Len(t, runner.runs, 2)
	_, err = newSubjectScheduler(t, sub, runner, now.Add(time.Minute)).RunNow(context.Background(), "rtn_triage")
	require.ErrorIs(t, err, ErrBusy)
}

func TestARoutineInItsOwnChatRunsEveryTicketTogetherAsBefore(t *testing.T) {
	now := time.Date(2026, 11, 9, 9, 0, 0, 0, time.UTC)
	st, runner := newFakeStore(), &fakeRunner{}
	sub := newFakeSubjects(&fakeEvents{st: st})
	st.routines["rtn_triage"] = triage()
	sub.add("rtn_triage", "Issue:a", now.Add(-2*time.Minute))
	sub.add("rtn_triage", "Issue:b", now.Add(-time.Minute))

	newSubjectScheduler(t, sub, runner, now).Tick(context.Background())
	require.Len(t, runner.runs, 1)
	assert.Equal(t, "chat_triage", runner.runs[0].ChatID)
	assert.Len(t, sub.ev.given(runner.runs[0].RunID), 2)
	assert.Empty(t, sub.chats)
}
