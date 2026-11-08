//go:build integration

package conductor_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/agent/chat"
	"github.com/podium-ade/podium/internal/agent/conductor"
	"github.com/podium-ade/podium/internal/agent/conductor/fakesource"
	"github.com/podium-ade/podium/internal/agent/store"
)

// routineFixture is a conductor with the web chat behind it and one of testSpeaker's
// routines, with a scheduled run of it started in its chat.
type routineFixture struct {
	st      *store.Store
	cond    *conductor.Conductor
	web     *chat.Source
	routine store.Routine
	run     store.RoutineRun
	sessID  string
	turnID  string
	token   string
}

func newRoutineFixture(t *testing.T) routineFixture {
	t.Helper()
	ctx := context.Background()
	st := newStore(t)
	fake := newFakePodium(t)
	web, err := chat.New(chat.Options{Store: st, DisplayName: "Podium", UIURL: "https://podium.example/"})
	require.NoError(t, err)
	src := fakesource.New(conductor.KindDev)
	t.Cleanup(src.Close)
	r := startWith(t, st, fake, src, func(o *conductor.Options) { o.Mirror = web })

	routine, err := st.CreateRoutine(ctx, testSpeaker, store.RoutineDraft{
		Name: "PR digest", Schedule: "0 9 * * 1-5", Timezone: "UTC",
		Instruction: "Report pull requests whose CI failed.", Notes: "- skip drafts",
		NextRunAt: time.Now().Add(time.Hour),
	}, "")
	require.NoError(t, err)
	run, ok, err := st.ClaimRoutineRun(ctx, routine.ID, time.Now().Truncate(time.Second), store.RoutineTriggerSchedule, store.RoutineRunRunning)
	require.NoError(t, err)
	require.True(t, ok)
	sess, err := st.UpsertSession(ctx, store.Session{
		SourceKind: conductor.SourceChat, SourceKey: store.ChatSourceKey(routine.ChatID), Profile: "podium",
	})
	require.NoError(t, err)
	turn, err := st.CreateTurnFor(ctx, sess.ID, routine.ChatID, testSpeaker, store.Backend{})
	require.NoError(t, err)

	// The run as the scheduler starts it: the chat holds its output, and the conductor links
	// the turn it runs as.
	require.NoError(t, web.RunRoutine(ctx, chat.RoutineRun{
		ChatID: routine.ChatID, Login: testSpeaker, Instruction: routine.Instruction,
		RoutineID: routine.ID, RunID: run.ID,
	}))
	ev := <-web.Events()
	r.cond.NoteRoutineTurnForTest(ctx, turn.ID, ev)
	token, err := r.cond.MintChatTurnTokenForTest(web, sess.ID, turn.ID, routine.ChatID, testSpeaker)
	require.NoError(t, err)
	return routineFixture{st: st, cond: r.cond, web: web, routine: routine, run: run, sessID: sess.ID, turnID: turn.ID, token: token}
}

func TestAnIdleRoutineRunLeavesNothingInItsChat(t *testing.T) {
	ctx := context.Background()
	f := newRoutineFixture(t)

	linked, err := f.st.TurnForRoutineRun(ctx, f.run.ID)
	require.NoError(t, err)
	assert.Equal(t, f.turnID, linked.ID, "the run learns its turn")

	require.NoError(t, f.web.PostActivity(ctx, f.routine.ChatID, conductor.Outbound{Text: `{"tool":"list_pull_requests"}`}))
	msg, err := f.cond.FinishRoutine(ctx, f.token, conductor.RoutineFinishRequest{
		Status: "idle", Summary: "No failing PRs.", State: []byte(`{"checked":["acme/api#7@abc"]}`),
	})
	require.NoError(t, err)
	assert.Contains(t, msg, "Recorded as idle")

	// The answer, the thinking and the tool calls all go nowhere.
	assert.Empty(t, f.cond.PostForTest(ctx, f.web, f.routine.ChatID, conductor.Outbound{Type: conductor.OutFinal, Text: "Nothing to report."}))
	f.cond.FinishForTest(ctx, f.web, f.routine.ChatID, conductor.ReactionDone)
	msgs, err := f.st.ListChatMessages(ctx, f.routine.ChatID, 0)
	require.NoError(t, err)
	assert.Empty(t, msgs, "an idle run leaves nothing in the chat")

	run, err := f.st.GetRoutineRun(ctx, f.run.ID)
	require.NoError(t, err)
	assert.Equal(t, store.RoutineRunIdle, run.Reported)
	assert.Equal(t, "No failing PRs.", run.Summary)
	r, err := f.st.GetRoutine(ctx, f.routine.ID)
	require.NoError(t, err)
	assert.JSONEq(t, `{"checked":["acme/api#7@abc"]}`, string(r.State), "the state is kept even when the run was idle")
}

func TestARoutineRunThatDidSomethingIsKeptUnderADivider(t *testing.T) {
	ctx := context.Background()
	f := newRoutineFixture(t)
	require.NoError(t, f.web.PostActivity(ctx, f.routine.ChatID, conductor.Outbound{Text: `{"tool":"list_check_runs"}`}))
	_, err := f.cond.FinishRoutine(ctx, f.token, conductor.RoutineFinishRequest{
		Status: "done", Summary: "1 PR failing", ProposeNote: "leave #42 alone",
	})
	require.NoError(t, err)
	f.cond.PostForTest(ctx, f.web, f.routine.ChatID, conductor.Outbound{Type: conductor.OutFinal, Text: "acme/api#9 fails `go test`."})

	msgs, err := f.st.ListChatMessages(ctx, f.routine.ChatID, 0)
	require.NoError(t, err)
	require.Len(t, msgs, 3)
	assert.Equal(t, store.RoleNotice, msgs[0].Role)
	assert.Contains(t, msgs[0].Text, "Routine run")
	assert.Equal(t, store.RoleActivity, msgs[1].Role)
	assert.Equal(t, store.RoleAssistant, msgs[2].Role)
	assert.Equal(t, "acme/api#9 fails `go test`.", msgs[2].Text)

	r, err := f.st.GetRoutine(ctx, f.routine.ID)
	require.NoError(t, err)
	assert.Equal(t, "leave #42 alone", r.ProposedNote)
}

func TestRoutineFinishRefusesWhatIsNotARunsToSay(t *testing.T) {
	ctx := context.Background()
	f := newRoutineFixture(t)
	_, err := f.cond.FinishRoutine(ctx, f.token, conductor.RoutineFinishRequest{Status: "maybe", Summary: "x"})
	assert.ErrorIs(t, err, conductor.ErrInvalidRoutineFinish)
	_, err = f.cond.FinishRoutine(ctx, f.token, conductor.RoutineFinishRequest{Status: "done", Summary: "x", State: []byte(`[1]`)})
	assert.ErrorIs(t, err, conductor.ErrInvalidRoutineFinish)

	// A chat that is not a routine's has nothing to finish.
	other, err := f.st.CreateChat(ctx, testSpeaker, "Just a chat")
	require.NoError(t, err)
	token, err := f.cond.MintChatTurnTokenForTest(f.web, f.sessID, "turn_other", other.ID, testSpeaker)
	require.NoError(t, err)
	_, err = f.cond.FinishRoutine(ctx, token, conductor.RoutineFinishRequest{Status: "done", Summary: "x"})
	assert.ErrorIs(t, err, conductor.ErrNotARoutine)
}

func TestARoutineOpensAtMostThreeChatsADay(t *testing.T) {
	ctx := context.Background()
	f := newRoutineFixture(t)
	for i := range conductor.MaxChatsStartedPerRoutineDay {
		_, err := f.cond.StartChat(ctx, f.token, conductor.StartChatRequest{Title: "Triage", Opening: "CI is red."})
		if i < conductor.MaxChatsStartedPerTurn {
			require.NoError(t, err)
		}
	}
	_, err := f.cond.StartChat(ctx, f.token, conductor.StartChatRequest{Title: "Triage", Opening: "CI is red."})
	assert.Error(t, err)
	// A later run of the same routine is still capped for the day.
	run2, ok, err := f.st.ClaimRoutineRun(ctx, f.routine.ID, time.Now().Add(time.Minute).Truncate(time.Second),
		store.RoutineTriggerManual, store.RoutineRunRunning)
	require.NoError(t, err)
	require.True(t, ok)
	turn2, err := f.st.CreateTurnFor(ctx, f.sessID, f.routine.ChatID, testSpeaker, store.Backend{})
	require.NoError(t, err)
	f.cond.NoteRoutineTurnForTest(ctx, turn2.ID, conductor.InboundEvent{
		Ref: f.routine.ChatID, Routine: &conductor.RoutineRef{RoutineID: f.routine.ID, RunID: run2.ID},
	})
	token2, err := f.cond.MintChatTurnTokenForTest(f.web, f.sessID, turn2.ID, f.routine.ChatID, testSpeaker)
	require.NoError(t, err)
	_, err = f.cond.StartChat(ctx, token2, conductor.StartChatRequest{Title: "Triage", Opening: "CI is red."})
	assert.ErrorIs(t, err, conductor.ErrRoutineChatCap)
}

// fakeCreator records what create_routine asked the API for.
type fakeCreator struct {
	owner string
	draft store.RoutineDraft
	st    *store.Store
}

func (f *fakeCreator) CreateRoutineFor(ctx context.Context, owner string, d store.RoutineDraft) (store.Routine, error) {
	f.owner, f.draft = owner, d
	d.NextRunAt = time.Now().Add(time.Hour)
	if d.Timezone == "" {
		d.Timezone = "UTC"
	}
	return f.st.CreateRoutine(ctx, owner, d, "")
}

// A person asks for a routine in their own chat and the assistant sets it up for them: theirs,
// with its own chat. Not from a routine's chat, and not from a turn nobody started.
func TestCreateRoutineSetsOneUpForThePersonWhoAsked(t *testing.T) {
	ctx := context.Background()
	f := newRoutineFixture(t)

	_, err := f.cond.CreateRoutine(ctx, f.token, conductor.CreateRoutineRequest{Name: "x", Instruction: "y", Schedule: "0 9 * * *"})
	assert.ErrorIs(t, err, conductor.ErrNoRoutineHere, "no creator yet: the conductor runs no routines")

	creator := &fakeCreator{st: f.st}
	f.cond.SetRoutineCreator(creator)

	_, err = f.cond.CreateRoutine(ctx, f.token, conductor.CreateRoutineRequest{Name: "x", Instruction: "y", Schedule: "0 9 * * *"})
	assert.ErrorIs(t, err, conductor.ErrNoRoutineHere, "a routine's own chat does not make routines")

	plain, err := f.st.CreateChat(ctx, testSpeaker, "Morning chat")
	require.NoError(t, err)
	token, err := f.cond.MintChatTurnTokenForTest(f.web, f.sessID, "turn_plain", plain.ID, testSpeaker)
	require.NoError(t, err)
	_, err = f.cond.CreateRoutine(ctx, token, conductor.CreateRoutineRequest{Name: "Digest", Schedule: "0 9 * * *"})
	assert.ErrorIs(t, err, conductor.ErrInvalidCreateRoutine, "an instruction is required")

	// With nothing to delegate to, create_routine is all the podium server offers there; the
	// routine's own chat gets routine_finish instead.
	assert.Equal(t, []string{"create_routine"}, f.cond.ChatToolNamesForTest(ctx, token))
	assert.Equal(t, []string{"routine_finish"}, f.cond.ChatToolNamesForTest(ctx, f.token))

	msg, err := f.cond.CreateRoutine(ctx, token, conductor.CreateRoutineRequest{
		Name: "Morning digest", Instruction: "List yesterday's merged PRs.", Schedule: "0 8 * * 1-5",
		Timezone: "America/Toronto", Notes: "- only acme/api",
	})
	require.NoError(t, err)
	assert.Equal(t, testSpeaker, creator.owner)
	assert.Equal(t, "0 8 * * 1-5", creator.draft.Schedule)
	assert.Equal(t, "America/Toronto", creator.draft.Timezone)
	assert.Equal(t, "- only acme/api", creator.draft.Notes)
	assert.Contains(t, msg, `Created the routine "Morning digest"`)
	assert.Contains(t, msg, "https://podium.example/agent/chat/")
	mine, err := f.st.ListRoutines(ctx, testSpeaker)
	require.NoError(t, err)
	assert.Len(t, mine, 2)

	nobody, err := f.cond.MintChatTurnTokenForTest(f.web, f.sessID, "turn_nobody", plain.ID, "")
	require.NoError(t, err)
	_, err = f.cond.CreateRoutine(ctx, nobody, conductor.CreateRoutineRequest{Name: "x", Instruction: "y", Schedule: "0 9 * * *"})
	assert.ErrorIs(t, err, conductor.ErrNoRoutineHere, "a turn nobody started has nobody to own it")
}

// Nobody watches a routine run, so it starts at most three tasks, and a routine has at most
// three running at once across its runs.
func TestARoutineRunStartsAtMostThreeTasks(t *testing.T) {
	ctx := context.Background()
	f := newRoutineFixture(t)
	for range conductor.MaxRoutineDelegationsPerRun {
		require.NoError(t, f.cond.CheckRoutineDelegationsForTest(ctx, f.token))
		_, err := f.st.CreateDelegation(ctx, store.NewDelegation{
			SessionID: f.sessID, TurnID: f.turnID, TriggerRef: f.routine.ChatID, Playbook: "fix", Instruction: "fix it",
		})
		require.NoError(t, err)
	}
	err := f.cond.CheckRoutineDelegationsForTest(ctx, f.token)
	require.ErrorIs(t, err, conductor.ErrRoutineDelegationCap)
	assert.Contains(t, err.Error(), "at most 3")

	// The next run is capped by the three still running, not by its own count.
	run2, ok, err := f.st.ClaimRoutineRun(ctx, f.routine.ID, time.Now().Add(time.Minute).Truncate(time.Second),
		store.RoutineTriggerManual, store.RoutineRunRunning)
	require.NoError(t, err)
	require.True(t, ok)
	turn2, err := f.st.CreateTurnFor(ctx, f.sessID, f.routine.ChatID, testSpeaker, store.Backend{})
	require.NoError(t, err)
	f.cond.NoteRoutineTurnForTest(ctx, turn2.ID, conductor.InboundEvent{
		Ref: f.routine.ChatID, Routine: &conductor.RoutineRef{RoutineID: f.routine.ID, RunID: run2.ID},
	})
	token2, err := f.cond.MintChatTurnTokenForTest(f.web, f.sessID, turn2.ID, f.routine.ChatID, testSpeaker)
	require.NoError(t, err)
	err = f.cond.CheckRoutineDelegationsForTest(ctx, token2)
	require.ErrorIs(t, err, conductor.ErrRoutineDelegationCap)
	assert.Contains(t, err.Error(), "still running")

	// A person's own message in the chat is not a run, and is not capped here.
	turn3, err := f.st.CreateTurnFor(ctx, f.sessID, f.routine.ChatID, testSpeaker, store.Backend{})
	require.NoError(t, err)
	token3, err := f.cond.MintChatTurnTokenForTest(f.web, f.sessID, turn3.ID, f.routine.ChatID, testSpeaker)
	require.NoError(t, err)
	assert.NoError(t, f.cond.CheckRoutineDelegationsForTest(ctx, token3))
}

// A system routine's run goes into a chat nobody owns, as nobody; a person's routine is not
// run as a system one, nor the other way round.
func TestASystemRoutineRunsInItsUnownedChatAsNobody(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	web, err := chat.New(chat.Options{Store: st, DisplayName: "Podium", UIURL: "https://podium.example/"})
	require.NoError(t, err)

	sys, err := st.CreateSystemRoutine(ctx, "admin@acme.com", store.RoutineDraft{
		Name: "Fix failing CI", Schedule: "*/5 * * * *", Timezone: "UTC",
		Instruction: "Report failing checks.", Repos: []string{"acme/api"}, NextRunAt: time.Now().Add(time.Hour),
	})
	require.NoError(t, err)
	c, err := st.GetChat(ctx, sys.ChatID)
	require.NoError(t, err)
	assert.Empty(t, c.Login, "a system routine's chat is nobody's")
	assert.Equal(t, sys.ID, c.RoutineID)

	require.Error(t, web.RunRoutine(ctx, chat.RoutineRun{
		ChatID: sys.ChatID, Login: testSpeaker, Instruction: sys.Instruction, RoutineID: sys.ID, RunID: "rrun_1",
	}), "a system routine is never run as a person")
	require.NoError(t, web.RunRoutine(ctx, chat.RoutineRun{
		ChatID: sys.ChatID, Instruction: sys.Instruction, RoutineID: sys.ID, RunID: "rrun_1", System: true,
	}))
	ev := <-web.Events()
	assert.Empty(t, ev.Login)
	require.NotNil(t, ev.Routine)
	assert.Equal(t, sys.ID, ev.Routine.RoutineID)

	personal, err := st.CreateRoutine(ctx, testSpeaker, store.RoutineDraft{
		Name: "Digest", Schedule: "0 9 * * *", Timezone: "UTC", Instruction: "Summarise.",
		NextRunAt: time.Now().Add(time.Hour),
	}, "")
	require.NoError(t, err)
	assert.Error(t, web.RunRoutine(ctx, chat.RoutineRun{
		ChatID: personal.ChatID, Instruction: personal.Instruction, RoutineID: personal.ID, RunID: "rrun_2", System: true,
	}))
}
