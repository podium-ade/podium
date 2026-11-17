//go:build integration

package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func routineDraft(next time.Time) RoutineDraft {
	return RoutineDraft{
		Name: "Morning digest", Schedule: "0 9 * * 1-5", Timezone: "America/Toronto",
		Instruction: "Summarise yesterday's merged pull requests.", Notes: "- skip drafts", NextRunAt: next,
	}
}

func TestARoutineOpensItsOwnChatAndKeepsIt(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	next := time.Date(2026, 11, 9, 14, 0, 0, 0, time.UTC)

	r, err := s.CreateRoutine(ctx, "ana@acme.com", routineDraft(next), "")
	require.NoError(t, err)
	assert.Equal(t, RoutineScopePersonal, r.Scope)
	assert.Equal(t, "ana@acme.com", r.OwnerLogin)
	assert.True(t, r.Enabled)
	assert.JSONEq(t, `{}`, string(r.State))
	assert.Equal(t, next, r.NextRunAt)

	chat, err := s.GetChat(ctx, r.ChatID)
	require.NoError(t, err)
	assert.Equal(t, r.ID, chat.RoutineID)
	assert.Equal(t, "ana@acme.com", chat.Login)
	assert.Equal(t, OriginWeb, chat.Origin)

	byChat, err := s.RoutineByChat(ctx, r.ChatID)
	require.NoError(t, err)
	assert.Equal(t, r.ID, byChat.ID)

	mine, err := s.ListRoutines(ctx, "ana@acme.com")
	require.NoError(t, err)
	assert.Len(t, mine, 1)
	theirs, err := s.ListRoutines(ctx, "bo@acme.com")
	require.NoError(t, err)
	assert.Empty(t, theirs)

	// Deleting the routine leaves its chat, as an ordinary chat.
	require.NoError(t, s.DeleteRoutine(ctx, r.ID))
	chat, err = s.GetChat(ctx, r.ChatID)
	require.NoError(t, err)
	assert.Empty(t, chat.RoutineID)
}

func TestARoutineRefusesWhatItWillNotHold(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	d := routineDraft(time.Now())
	d.Instruction = "  "
	_, err := s.CreateRoutine(ctx, "ana@acme.com", d, "")
	assert.ErrorIs(t, err, ErrInvalidRoutine)

	r, err := s.CreateRoutine(ctx, "ana@acme.com", routineDraft(time.Now()), "")
	require.NoError(t, err)
	_, err = s.SetRoutineState(ctx, r.ID, json.RawMessage(`[1,2]`))
	assert.ErrorIs(t, err, ErrInvalidRoutine)
	updated, err := s.SetRoutineState(ctx, r.ID, json.RawMessage(`{"seen":["acme/api#42@abc"]}`))
	require.NoError(t, err)
	assert.JSONEq(t, `{"seen":["acme/api#42@abc"]}`, string(updated.State))
}

func TestARoutineFiringIsClaimedOnceAndSettledOnce(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	r, err := s.CreateRoutine(ctx, "ana@acme.com", routineDraft(time.Now()), "")
	require.NoError(t, err)
	at := time.Date(2026, 11, 9, 14, 0, 7, 0, time.UTC)

	run, ok, err := s.ClaimRoutineRun(ctx, r.ID, at, RoutineTriggerSchedule, RoutineRunRunning)
	require.NoError(t, err)
	require.True(t, ok)
	// A second conductor claiming the same firing gets nothing.
	_, ok, err = s.ClaimRoutineRun(ctx, r.ID, at, RoutineTriggerSchedule, RoutineRunRunning)
	require.NoError(t, err)
	assert.False(t, ok)

	running, err := s.RunningRoutineRuns(ctx)
	require.NoError(t, err)
	require.Len(t, running, 1)

	// The run learns its turn and the turn its run.
	sess, err := s.UpsertSession(ctx, Session{SourceKind: "chat", SourceKey: ChatSourceKey(r.ChatID), Profile: "podium"})
	require.NoError(t, err)
	turn, err := s.CreateTurnFor(ctx, sess.ID, r.ChatID, "ana@acme.com", Backend{})
	require.NoError(t, err)
	require.NoError(t, s.LinkRoutineRunTurn(ctx, run.ID, turn.ID))
	got, err := s.TurnForRoutineRun(ctx, run.ID)
	require.NoError(t, err)
	assert.Equal(t, turn.ID, got.ID)
	assert.Equal(t, run.ID, got.RoutineRunID)

	busy, err := s.SessionBusy(ctx, sess.ID)
	require.NoError(t, err)
	assert.True(t, busy, "a running turn makes the session busy")

	require.NoError(t, s.ReportRoutineRun(ctx, run.ID, RoutineRunIdle, "nothing new\nsecond line"))
	reported, err := s.GetRoutineRun(ctx, run.ID)
	require.NoError(t, err)
	assert.Equal(t, RoutineRunIdle, reported.Reported)
	assert.Equal(t, "nothing new", reported.Summary)

	cost := 0.012
	settled, ok, err := s.SettleRoutineRun(ctx, run.ID, RoutineRunIdle, "", &cost)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, RoutineRunIdle, settled.Status)
	assert.Equal(t, "nothing new", settled.Summary, "an empty summary keeps what the run reported")
	_, ok, err = s.SettleRoutineRun(ctx, run.ID, RoutineRunFailed, "", nil)
	require.NoError(t, err)
	assert.False(t, ok, "a settled run is not settled again")

	// Idle runs are left out of the list unless asked for.
	runs, _, err := s.ListRoutineRuns(ctx, r.ID, false, "", 10)
	require.NoError(t, err)
	assert.Empty(t, runs)
	runs, _, err = s.ListRoutineRuns(ctx, r.ID, true, "", 10)
	require.NoError(t, err)
	assert.Len(t, runs, 1)

	require.NoError(t, s.NoteRoutineIdle(ctx, r.ID, true))
	require.NoError(t, s.NoteRoutineIdle(ctx, r.ID, true))
	again, err := s.GetRoutine(ctx, r.ID)
	require.NoError(t, err)
	assert.Equal(t, 2, again.IdleCount)
	require.NoError(t, s.NoteRoutineIdle(ctx, r.ID, false))
	again, err = s.GetRoutine(ctx, r.ID)
	require.NoError(t, err)
	assert.Equal(t, 0, again.IdleCount)
}

func TestDueRoutinesAreEnabledOnesWhoseTimeHasCome(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	due, err := s.CreateRoutine(ctx, "ana@acme.com", routineDraft(now.Add(-time.Minute)), "")
	require.NoError(t, err)
	_, err = s.CreateRoutine(ctx, "ana@acme.com", routineDraft(now.Add(time.Hour)), "")
	require.NoError(t, err)
	paused, err := s.CreateRoutine(ctx, "bo@acme.com", routineDraft(now.Add(-time.Minute)), "")
	require.NoError(t, err)
	_, err = s.SetRoutineEnabled(ctx, paused.ID, false, "", paused.NextRunAt)
	require.NoError(t, err)

	got, err := s.DueRoutines(ctx, now, 10)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, due.ID, got[0].ID)
}

func TestNotesAndProposedNotes(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	r, err := s.CreateRoutine(ctx, "ana@acme.com", routineDraft(time.Now()), "")
	require.NoError(t, err)
	require.NoError(t, s.ProposeRoutineNote(ctx, r.ID, "leave #42 alone"))
	got, err := s.GetRoutine(ctx, r.ID)
	require.NoError(t, err)
	assert.Equal(t, "leave #42 alone", got.ProposedNote)
	got, err = s.SetRoutineNotes(ctx, r.ID, "ana@acme.com", got.Notes+"\n- leave #42 alone", "")
	require.NoError(t, err)
	assert.Equal(t, "- skip drafts\n- leave #42 alone", got.Notes)
	assert.Empty(t, got.ProposedNote)
}
