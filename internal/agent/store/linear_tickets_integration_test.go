//go:build integration

package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFindLinearIssueByIdentifierOrIDAndItsTeams(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	require.NoError(t, s.UpsertLinearIssue(ctx, LinearIssue{
		SourceKey: LinearIssueKey("issue-6"), Identifier: "EME-6", Title: "Fix login", TeamKey: "eme", StateName: "Todo",
	}))
	require.NoError(t, s.UpsertLinearIssue(ctx, LinearIssue{SourceKey: LinearProjectKey("proj-1"), Title: "A project"}))

	got, err := s.FindLinearIssue(ctx, "eme-6", "")
	require.NoError(t, err)
	assert.Equal(t, "issue-6", got.LinearID)
	got, err = s.FindLinearIssue(ctx, "", "issue-6")
	require.NoError(t, err)
	assert.Equal(t, "EME-6", got.Identifier)
	_, err = s.FindLinearIssue(ctx, "", "proj-1")
	require.ErrorIs(t, err, ErrNotFound, "a project is no issue")
	_, err = s.FindLinearIssue(ctx, "EME-7", "")
	require.ErrorIs(t, err, ErrNotFound)

	keys, err := s.LinearIssueTeamKeys(ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{"EME"}, keys)
}

func TestAnIssueConversationsTasksAndRoutineRuns(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	key := LinearIssueKey("issue-6")
	chat, _, err := s.EnsureLinearConversation(ctx, key, "EME-6 Fix login")
	require.NoError(t, err)
	sess, err := s.GetSessionByKey(ctx, key)
	require.NoError(t, err)
	turn, err := s.CreateTurnFor(ctx, sess.ID, "ref", "ana@acme.com", Backend{})
	require.NoError(t, err)
	for _, pb := range []string{"coder", "reviewer"} {
		_, err := s.CreateDelegation(ctx, NewDelegation{SessionID: sess.ID, TurnID: turn.ID, TriggerRef: "ref", Playbook: pb, Instruction: "go"})
		require.NoError(t, err)
		time.Sleep(time.Millisecond)
	}
	tasks, err := s.SessionDelegations(ctx, sess.ID, 20)
	require.NoError(t, err)
	require.Len(t, tasks, 2)
	assert.Equal(t, "reviewer", tasks[0].Playbook, "newest first")

	r, err := s.CreateRoutine(ctx, "ana@acme.com", linearDraft(), "")
	require.NoError(t, err)
	run, ok, err := s.ClaimRoutineRun(ctx, r.ID, time.Now().UTC(), "schedule", "running")
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, s.SetRoutineRunChat(ctx, run.ID, chat.ID))
	runs, err := s.ChatRoutineRuns(ctx, chat.ID, 20)
	require.NoError(t, err)
	require.Len(t, runs, 1)
	assert.Equal(t, run.ID, runs[0].ID)
	assert.Equal(t, r.Name, runs[0].RoutineName)
	assert.Equal(t, chat.ID, runs[0].ChatID)
}
