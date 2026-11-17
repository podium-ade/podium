//go:build integration

package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// railIDs is the chat ids in login's rail, as the API asks for it.
func railIDs(t *testing.T, s *Store, login string, all bool) map[string]Chat {
	t.Helper()
	rows, _, err := s.ListChats(context.Background(), login, all, 100, "", ListChatsScope{
		ExcludeLinear: true, LinearRail: true,
	})
	require.NoError(t, err)
	out := map[string]Chat{}
	for _, c := range rows {
		out[c.ID] = c
	}
	return out
}

// A ticket's conversation is in the rail of the people who touched the issue through a linked
// Linear account or spoke in it — not of an admin, a routine's owner, or the dev token — with
// what the webhooks said about the issue, and out of it once hidden until something new is said.
func TestATicketsConversationIsInItsPeoplesRail(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	key := "linear:iss-6"
	chat, _, err := s.EnsureLinearConversation(ctx, key, "EME-6 Login is flaky")
	require.NoError(t, err)
	project, _, err := s.EnsureLinearConversation(ctx, "linear:project:p-1", "Q4")
	require.NoError(t, err)
	require.NoError(t, s.UpsertLinearIssue(ctx, LinearIssue{
		SourceKey: key, Identifier: "EME-6", Title: "Login is flaky", StateName: "In Progress", StateType: "started",
	}))
	require.NoError(t, s.UpsertIdentity(ctx, Identity{Login: "ada@acme.com", Provider: ProviderLinear, ExternalID: "org/ada"}))
	require.NoError(t, s.RecordLinearPeople(ctx, key, []LinearPerson{
		{LinearUser: "org/ada", Via: LinearViaAssigned},
		{LinearUser: "org/ghost", Via: LinearViaCommented}, // never linked
		{Login: "rita@acme.com", Via: LinearViaRoutine},
	}, time.Now()))
	require.NoError(t, s.RecordLinearPeople(ctx, "linear:project:p-1", []LinearPerson{
		{LinearUser: "org/ada", Via: LinearViaChanged},
	}, time.Now()))
	_, err = s.AddChatParticipant(ctx, chat.ID, "bob@acme.com", JoinedLinear, time.Now())
	require.NoError(t, err)
	web, err := s.CreateChat(ctx, "ada@acme.com", "my own")
	require.NoError(t, err)

	ada := railIDs(t, s, "ada@acme.com", false)
	require.Contains(t, ada, chat.ID, "touched through a linked account")
	assert.Contains(t, ada, web.ID, "beside their own chats")
	assert.NotContains(t, ada, project.ID, "a project's conversation is not a ticket's")
	li := ada[chat.ID].Linear
	require.NotNil(t, li)
	assert.Equal(t, "EME-6", li.Identifier)
	assert.Equal(t, "Login is flaky", li.Title)
	assert.Equal(t, "started", li.StateType)
	assert.Nil(t, li.AskedAt)
	assert.Nil(t, ada[web.ID].Linear)

	assert.Contains(t, railIDs(t, s, "bob@acme.com", false), chat.ID, "spoke in it")
	assert.NotContains(t, railIDs(t, s, "rita@acme.com", false), chat.ID, "owning a routine that ran there is not enough")
	assert.NotContains(t, railIDs(t, s, "carol@acme.com", false), chat.ID, "nor is being anybody else")
	assert.NotContains(t, railIDs(t, s, "", true), chat.ID, "the dev token reads them in the Linear view")

	// A question waits on whoever's turn asked it.
	require.NoError(t, s.SetLinearIssueAsked(ctx, key, true, "ada@acme.com"))
	li = railIDs(t, s, "ada@acme.com", false)[chat.ID].Linear
	require.NotNil(t, li.AskedAt)
	assert.Equal(t, "ada@acme.com", li.AskedOf)

	// Touching the issue makes them one of its writers (0049); an outsider is nobody.
	role, err := s.ChatRole(ctx, chat.ID, "ada@acme.com")
	require.NoError(t, err)
	assert.Equal(t, RoleEditor, role)
	role, err = s.ChatRole(ctx, chat.ID, "carol@acme.com")
	require.NoError(t, err)
	assert.Empty(t, role)

	// Hidden, it stays out until something new is said in it.
	require.NoError(t, s.SetChatRailHidden(ctx, chat.ID, "ada@acme.com", true, time.Now()))
	assert.NotContains(t, railIDs(t, s, "ada@acme.com", false), chat.ID)
	assert.Contains(t, railIDs(t, s, "bob@acme.com", false), chat.ID, "hiding is one person's")
	_, err = s.AppendChatMessage(ctx, ChatMessage{ChatID: chat.ID, Role: RoleUser, Text: "any news?", AuthorLogin: "bob@acme.com", TS: time.Now().Add(time.Second)})
	require.NoError(t, err)
	assert.Contains(t, railIDs(t, s, "ada@acme.com", false), chat.ID, "back once somebody spoke")

	require.NoError(t, s.SetChatRailHidden(ctx, chat.ID, "ada@acme.com", true, time.Now().Add(time.Minute)))
	assert.NotContains(t, railIDs(t, s, "ada@acme.com", false), chat.ID)
	require.NoError(t, s.SetChatRailHidden(ctx, chat.ID, "ada@acme.com", false, time.Now()))
	assert.Contains(t, railIDs(t, s, "ada@acme.com", false), chat.ID, "and back when they unhide it")

	// Without the rail, the list leaves every Linear conversation out, as before.
	rows, _, err := s.ListChats(ctx, "ada@acme.com", false, 100, "", ListChatsScope{ExcludeLinear: true})
	require.NoError(t, err)
	for _, c := range rows {
		assert.NotEqual(t, chat.ID, c.ID)
	}
}

// Each turn of a conversation says who it ran as and which tasks it ran, a routine's run its
// routine.
func TestAConversationsTurnsSayWhoTheyRanAs(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	key := "linear:iss-7"
	chat, _, err := s.EnsureLinearConversation(ctx, key, "EME-7")
	require.NoError(t, err)
	sess, err := s.GetSessionByKey(ctx, key)
	require.NoError(t, err)

	alice, err := s.CreateTurnFor(ctx, sess.ID, chat.ID, "alice@acme.com", Backend{})
	require.NoError(t, err)
	require.NoError(t, s.SetTurnTask(ctx, alice.ID, "task-own"))
	d, err := s.CreateDelegation(ctx, NewDelegation{SessionID: sess.ID, TurnID: alice.ID, TriggerRef: chat.ID, Playbook: "coder", Instruction: "fix"})
	require.NoError(t, err)
	require.NoError(t, s.SetDelegationTask(ctx, d.ID, "task-delegated"))
	_, err = s.CreateTurnFor(ctx, sess.ID, chat.ID, TurnStartedByRoutine("r-1"), Backend{})
	require.NoError(t, err)

	turns, err := s.ChatTurnRunners(ctx, chat.ID)
	require.NoError(t, err)
	require.Len(t, turns, 2)
	assert.Equal(t, "alice@acme.com", turns[0].Login)
	assert.ElementsMatch(t, []string{"task-own", "task-delegated"}, turns[0].Tasks)
	assert.Empty(t, turns[1].Login, "a system routine's run is nobody's")
	ix := NewTurnIndex(turns)
	assert.Equal(t, alice.ID, ix.TurnOf("task-delegated", time.Now()).TurnID)
}
