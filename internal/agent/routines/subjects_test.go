package routines

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/agent/store"
)

// fakeSubjects is the store's Linear conversations in memory, beside a fakeEvents, under the
// fakeStore's lock. about is what each event is about, by its id; an event missing from it is
// about nothing in particular.
type fakeSubjects struct {
	ev         *fakeEvents
	about      map[int]string
	convs      map[string]string   // conversation key -> chat
	readers    map[string][]string // conversation key -> owners recorded as reading it
	runChats   map[string]string   // run -> chat
	authorRuns []string            // run@chat a pull request's authors were recorded for
}

func newFakeSubjects(ev *fakeEvents) *fakeSubjects {
	return &fakeSubjects{
		ev: ev, about: map[int]string{}, convs: map[string]string{}, readers: map[string][]string{},
		runChats: map[string]string{},
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
			sub = &store.RoutineSubject{Key: key, Title: key, Oldest: e.at}
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

func (f *fakeSubjects) EnsureConversation(_ context.Context, key, title string) (store.Chat, bool, error) {
	f.ev.st.mu.Lock()
	defer f.ev.st.mu.Unlock()
	origin := store.OriginLinear
	if store.IsGitHubConversation(key) {
		origin = store.OriginGitHub
	}
	if id, ok := f.convs[key]; ok {
		return store.Chat{ID: id, SourceKey: key, Origin: origin}, false, nil
	}
	id := "chat_" + key
	f.convs[key] = id
	return store.Chat{ID: id, SourceKey: key, Origin: origin, Title: title}, true, nil
}

func (f *fakeSubjects) NoteRoutineReader(_ context.Context, key, _, login string) error {
	f.ev.st.mu.Lock()
	defer f.ev.st.mu.Unlock()
	f.readers[key] = append(f.readers[key], login)
	return nil
}

func (f *fakeSubjects) NotePullRequestAuthors(_ context.Context, run, chatID string) error {
	f.ev.st.mu.Lock()
	defer f.ev.st.mu.Unlock()
	f.authorRuns = append(f.authorRuns, run+"@"+chatID)
	return nil
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

// perTicket is the triage routine that runs in each ticket's conversation. It has no chat of its
// own: one is opened only if an event about no ticket needs it.
func perTicket() store.Routine {
	r := triage()
	r.ChatMode = store.RoutineChatModeSubject
	r.ChatID = ""
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

func TestEachTicketsEventsRunInItsConversationOneRunAtATime(t *testing.T) {
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
	assert.Equal(t, "chat_linear:a", got.ChatID, "the ticket that waited longest, in the issue's conversation")
	assert.True(t, got.Conversation, "a turn of the issue's conversation, not the routine's chat")
	assert.True(t, strings.HasPrefix(got.Instruction, ConversationPreamble), "told it shares the issue's conversation")
	assert.True(t, strings.HasSuffix(got.Instruction, "Label each new issue."))
	assert.Equal(t, "chat_linear:a", sub.runChats[got.RunID])
	given := sub.ev.given(got.RunID)
	require.Len(t, given, 2, "every waiting event about that ticket, and nothing else")
	assert.Equal(t, []int{first, third}, []int{given[0].id, given[1].id})
	assert.Equal(t, 1, sub.ev.waiting("rtn_triage"))
	assert.Empty(t, st.routines["rtn_triage"].ChatID, "the routine opened no chat of its own")
	assert.Empty(t, sub.readers["linear:a"], "a system routine has no owner to record")
	assert.Empty(t, sub.authorRuns, "a Linear ticket has no pull request author")

	// While that run goes, the next ticket waits.
	newSubjectScheduler(t, sub, runner, now.Add(15*time.Second)).Tick(context.Background())
	require.Len(t, runner.runs, 1)

	finish(st, got.RunID)
	newSubjectScheduler(t, sub, runner, now.Add(30*time.Second)).Tick(context.Background())
	require.Len(t, runner.runs, 2)
	assert.Equal(t, "chat_linear:b", runner.runs[1].ChatID)
	assert.Zero(t, sub.ev.waiting("rtn_triage"))

	// The next event about the first ticket goes to its conversation again.
	finish(st, runner.runs[1].RunID)
	sub.add("rtn_triage", "Issue:a", now.Add(40*time.Second))
	newSubjectScheduler(t, sub, runner, now.Add(45*time.Second)).Tick(context.Background())
	require.Len(t, runner.runs, 3)
	assert.Equal(t, "chat_linear:a", runner.runs[2].ChatID)
	assert.Len(t, sub.convs, 2)
}

// A project's events are its conversation's, and a personal routine's owner is recorded as
// having touched what it ran on, which is what lets them read it there.
func TestAProjectIsAConversationAndTheOwnerIsRecordedOnIt(t *testing.T) {
	now := time.Date(2026, 11, 9, 9, 0, 0, 0, time.UTC)
	st, runner := newFakeStore(), &fakeRunner{}
	sub := newFakeSubjects(&fakeEvents{st: st})
	r := perTicket()
	r.Scope, r.OwnerLogin, r.PersonalityID = store.RoutineScopePersonal, "ana@acme.com", "pers_ana"
	st.routines[r.ID] = r
	sub.add(r.ID, "Project:p", now.Add(-time.Minute))

	newSubjectScheduler(t, sub, runner, now).Tick(context.Background())
	require.Len(t, runner.runs, 1)
	assert.Equal(t, "chat_linear:project:p", runner.runs[0].ChatID)
	assert.Equal(t, "ana@acme.com", runner.runs[0].Login, "the run is its owner's, in whoever's conversation")
	assert.Equal(t, "pers_ana", runner.runs[0].PersonalityID, "with the routine's assistant")
	assert.Equal(t, []string{"ana@acme.com"}, sub.readers["linear:project:p"])
}

// A conversation with a turn going — a delegation, somebody's mention, another routine — keeps
// the routine's events about it waiting, as a second prompt would; the next ticket runs.
func TestABusyConversationIsPassedOverForTheNextTicket(t *testing.T) {
	now := time.Date(2026, 11, 9, 9, 0, 0, 0, time.UTC)
	st, runner := newFakeStore(), &fakeRunner{}
	sub := newFakeSubjects(&fakeEvents{st: st})
	st.routines["rtn_triage"] = perTicket()
	st.busy["sess:linear:a"] = true
	sub.add("rtn_triage", "Issue:a", now.Add(-2*time.Minute))
	sub.add("rtn_triage", "Issue:b", now.Add(-time.Minute))

	newSubjectScheduler(t, sub, runner, now).Tick(context.Background())
	require.Len(t, runner.runs, 1)
	assert.Equal(t, "chat_linear:b", runner.runs[0].ChatID)
	assert.Equal(t, 1, sub.ev.waiting("rtn_triage"), "the busy ticket's event waits for its conversation")
}

// An event about no conversation — a cycle, a label, nothing — runs in the routine's own chat,
// which is opened then, the first time one needs it.
func TestAnEventAboutNoTicketOpensTheRoutinesOwnChatThen(t *testing.T) {
	now := time.Date(2026, 11, 9, 9, 0, 0, 0, time.UTC)
	st, runner := newFakeStore(), &fakeRunner{}
	sub := newFakeSubjects(&fakeEvents{st: st})
	st.routines["rtn_triage"] = perTicket()
	sub.add("rtn_triage", "Cycle:c", now.Add(-time.Minute))

	newSubjectScheduler(t, sub, runner, now).Tick(context.Background())
	require.Len(t, runner.runs, 1)
	assert.Equal(t, "chat_rtn_triage", runner.runs[0].ChatID)
	assert.False(t, runner.runs[0].Conversation)
	assert.Equal(t, "Label each new issue.", runner.runs[0].Instruction)
	assert.Equal(t, "chat_rtn_triage", st.routines["rtn_triage"].ChatID)
	assert.Empty(t, sub.convs, "a cycle is no conversation")
}

func TestAPerTicketRoutineCannotBeRunByHand(t *testing.T) {
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	st, runner := newFakeStore(), &fakeRunner{}
	sub := newFakeSubjects(&fakeEvents{st: st})
	st.routines["rtn_triage"] = perTicket()
	_, err := newSubjectScheduler(t, sub, runner, now).RunNow(context.Background(), "rtn_triage")
	require.ErrorIs(t, err, ErrNotByHand)
	assert.Empty(t, runner.runs)
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
	assert.Empty(t, sub.convs)
}

// A routine on Linear events that runs in its own chat opens it on its first run: none was
// opened when it was made.
func TestARoutineInItsOwnChatOpensItOnItsFirstRun(t *testing.T) {
	now := time.Date(2026, 11, 9, 9, 0, 0, 0, time.UTC)
	st, runner := newFakeStore(), &fakeRunner{}
	sub := newFakeSubjects(&fakeEvents{st: st})
	r := triage()
	r.ChatID = ""
	st.routines[r.ID] = r
	sub.add(r.ID, "Issue:a", now.Add(-time.Minute))

	newSubjectScheduler(t, sub, runner, now).Tick(context.Background())
	require.Len(t, runner.runs, 1)
	assert.Equal(t, "chat_rtn_triage", runner.runs[0].ChatID)
	assert.Equal(t, "chat_rtn_triage", st.routines[r.ID].ChatID)
}

// A routine on GitHub events in each pull request's conversation runs an event about a pull
// request as a turn of that pull request's conversation — the chat its reviews are in — and an
// event about none (a push to a branch) in its own chat.
func TestAPullRequestsEventsRunInItsConversation(t *testing.T) {
	now := time.Date(2026, 11, 9, 9, 0, 0, 0, time.UTC)
	st, runner := newFakeStore(), &fakeRunner{}
	sub := newFakeSubjects(&fakeEvents{st: st})
	r := perTicket()
	r.Kind, r.Events, r.Teams = store.RoutineKindGitHub, []string{"workflow_run.failed"}, nil
	r.Scope, r.OwnerLogin = store.RoutineScopePersonal, "ana@acme.com"
	st.routines[r.ID] = r
	sub.add(r.ID, "PullRequest:Acme/api#42", now.Add(-2*time.Minute))
	sub.add(r.ID, "", now.Add(-time.Minute))

	newSubjectScheduler(t, sub, runner, now).Tick(context.Background())
	require.Len(t, runner.runs, 1)
	got := runner.runs[0]
	assert.Equal(t, "chat_github:Acme/api#42", got.ChatID, "the pull request's conversation, keyed as its reviews are")
	assert.True(t, got.Conversation)
	assert.True(t, strings.HasPrefix(got.Instruction, PullRequestPreamble), "told it is a turn of the pull request's conversation")
	assert.Equal(t, []string{"ana@acme.com"}, sub.readers["github:Acme/api#42"], "its owner may read it")
	assert.Equal(t, []string{got.RunID + "@chat_github:Acme/api#42"}, sub.authorRuns, "and its author is made an editor")

	finish(st, got.RunID)
	newSubjectScheduler(t, sub, runner, now.Add(15*time.Second)).Tick(context.Background())
	require.Len(t, runner.runs, 2)
	assert.False(t, runner.runs[1].Conversation, "a push to a branch is no pull request's: the routine's own chat")
	assert.NotEmpty(t, st.routines[r.ID].ChatID, "opened for it")
}
