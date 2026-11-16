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

// issueDelivery is an Issue.create about one issue.
func issueDelivery(id, issueID, identifier string, at time.Time) LinearDelivery {
	payload, _ := json.Marshal(map[string]any{
		"type": "Issue", "action": "create",
		"data": map[string]any{"id": issueID, "identifier": identifier, "title": "Fix login"},
	})
	return LinearDelivery{DeliveryID: id, Event: "Issue.create", TeamKey: "ENG", Payload: payload, ReceivedAt: at}
}

func perTicketDraft() RoutineDraft {
	d := linearDraft()
	d.ChatMode = RoutineChatModeSubject
	return d
}

func TestAPerTicketRoutineKeepsItsModeAndRefusesItOnASchedule(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	r, err := s.CreateSystemRoutine(ctx, "admin@acme.com", perTicketDraft())
	require.NoError(t, err)
	assert.Equal(t, RoutineChatModeSubject, r.ChatMode)
	assert.True(t, r.PerSubject())

	d := routineDraft(time.Now().Add(time.Hour))
	d.ChatMode = RoutineChatModeSubject
	_, err = s.UpdateRoutine(ctx, r.ID, "admin@acme.com", d)
	require.ErrorIs(t, err, ErrInvalidRoutine)

	back := linearDraft()
	updated, err := s.UpdateRoutine(ctx, r.ID, "admin@acme.com", back)
	require.NoError(t, err)
	assert.Equal(t, RoutineChatModeRoutine, updated.ChatMode)
	revs, err := s.ListRoutineRevisions(ctx, r.ID, 10)
	require.NoError(t, err)
	require.Len(t, revs, 2, "switching where runs happen is a revision")
	assert.Contains(t, revs[1].Schedule, "in each ticket's conversation")
}

func TestEventsAreGroupedByTicketAndEachTicketIsItsConversation(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	r, err := s.CreateRoutine(ctx, "ana@acme.com", perTicketDraft(), "")
	require.NoError(t, err)
	assert.Empty(t, r.ChatID, "a routine on Linear events opens no chat when it is made")
	base := time.Now().UTC().Add(-time.Hour)
	for i, d := range []LinearDelivery{
		issueDelivery("d-1", "iss-a", "ENG-1", base),
		issueDelivery("d-2", "iss-b", "ENG-2", base.Add(time.Minute)),
		issueDelivery("d-3", "iss-a", "ENG-1", base.Add(2*time.Minute)),
		{DeliveryID: "d-4", Event: "Comment.create", TeamKey: "ENG", ReceivedAt: base.Add(3 * time.Minute),
			Payload: json.RawMessage(`{"type":"Comment","action":"create","data":{"id":"cmt-1","body":"hi"}}`)},
	} {
		n, err := s.StoreRoutineEvents(ctx, d)
		require.NoError(t, err, i)
		require.Equal(t, 1, n, i)
	}

	subjects, err := s.PendingRoutineSubjects(ctx, r.ID)
	require.NoError(t, err)
	require.Len(t, subjects, 3)
	assert.Equal(t, "Issue:iss-a", subjects[0].Key, "the ticket waiting longest first")
	assert.Equal(t, "ENG-1 Fix login", subjects[0].Title)
	assert.Equal(t, 2, subjects[0].Pending)
	assert.Equal(t, "Issue:iss-b", subjects[1].Key)
	assert.Empty(t, subjects[2].Key, "a comment on no issue is about nothing in particular")

	key := LinearConversationKey(subjects[0].Key)
	chat, created, err := s.EnsureLinearConversation(ctx, key, subjects[0].Title)
	require.NoError(t, err)
	assert.True(t, created)
	again, created, err := s.EnsureLinearConversation(ctx, key, subjects[0].Title)
	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, chat.ID, again.ID, "one conversation per issue")
	assert.Empty(t, chat.Login, "nobody's until a Podium person speaks in it")
	assert.Empty(t, chat.RoutineID, "it is the issue's, not the routine's")
	assert.Equal(t, OriginLinear, chat.Origin)
	sess, err := s.GetSessionByKey(ctx, key)
	require.NoError(t, err, "its session is opened with it, so the issue's prompts are turns of it")
	assert.Equal(t, OriginLinear, sess.SourceKind)

	run, ok, err := s.ClaimRoutineRun(ctx, r.ID, time.Now(), RoutineTriggerEvent, RoutineRunRunning)
	require.NoError(t, err)
	require.True(t, ok)
	n, err := s.AttachRoutineSubjectEvents(ctx, run.ID, r.ID, "Issue:iss-a", 20)
	require.NoError(t, err)
	assert.Equal(t, 2, n, "every event about that ticket and no other")
	require.NoError(t, s.SetRoutineRunChat(ctx, run.ID, chat.ID))
	running, err := s.RunningRoutineRun(ctx, r.ID)
	require.NoError(t, err)
	assert.Equal(t, chat.ID, running.ChatID)

	subjects, err = s.PendingRoutineSubjects(ctx, r.ID)
	require.NoError(t, err)
	require.Len(t, subjects, 2)
	n, err = s.AttachRoutineSubjectEvents(ctx, run.ID, r.ID, "", 20)
	require.NoError(t, err)
	assert.Equal(t, 1, n, "the events about nothing in particular are a group of their own")
}

func TestARoutineOnLinearEventsGetsItsOwnChatOnlyWhenARunNeedsIt(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	r, err := s.CreateRoutine(ctx, "ana@acme.com", linearDraft(), "")
	require.NoError(t, err)
	assert.Empty(t, r.ChatID)
	chatID, created, err := s.EnsureRoutineChat(ctx, r)
	require.NoError(t, err)
	assert.True(t, created)
	again, created, err := s.EnsureRoutineChat(ctx, r)
	require.NoError(t, err)
	assert.False(t, created, "a second tick finds the chat the first opened")
	assert.Equal(t, chatID, again)
	c, err := s.GetChat(ctx, chatID)
	require.NoError(t, err)
	assert.Equal(t, "ana@acme.com", c.Login)
	assert.Equal(t, r.ID, c.RoutineID)
	back, err := s.GetRoutine(ctx, r.ID)
	require.NoError(t, err)
	assert.Equal(t, chatID, back.ChatID)

	sys, err := s.CreateSystemRoutine(ctx, "admin@acme.com", linearDraft())
	require.NoError(t, err)
	assert.Empty(t, sys.ChatID)
	sysChat, _, err := s.EnsureRoutineChat(ctx, sys)
	require.NoError(t, err)
	sc, err := s.GetChat(ctx, sysChat)
	require.NoError(t, err)
	assert.Empty(t, sc.Login, "a system routine's chat is nobody's")

	// Moved to a schedule, a routine that never needed a chat gets one.
	other, err := s.CreateRoutine(ctx, "ana@acme.com", linearDraft(), "")
	require.NoError(t, err)
	moved, err := s.UpdateRoutine(ctx, other.ID, "ana@acme.com", routineDraft(time.Now().Add(time.Hour)))
	require.NoError(t, err)
	assert.NotEmpty(t, moved.ChatID)
}

func TestWhoTouchedAnIssueReadsItsConversation(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	key := LinearIssueKey("iss-a")
	chat, _, err := s.EnsureLinearConversation(ctx, key, "ENG-1 Fix login")
	require.NoError(t, err)
	require.NoError(t, s.UpsertIdentity(ctx, Identity{Login: "bo@acme.com", Provider: ProviderWeb, ExternalID: "bo@acme.com"}))

	role := func(login string) string {
		r, err := s.ChatRole(ctx, chat.ID, login)
		require.NoError(t, err)
		return r
	}
	assert.Empty(t, role("bo@acme.com"))
	require.NoError(t, s.RecordLinearPeople(ctx, key, []LinearPerson{
		{LinearUser: "org-1/u-bo", Via: LinearViaCommented},
		{Login: "ana@acme.com", Via: LinearViaRoutine},
		{Via: LinearViaCreated},
	}, time.Now()))
	assert.Empty(t, role("bo@acme.com"), "an unlinked Linear account grants nothing")
	assert.Equal(t, RoleEditor, role("ana@acme.com"), "a routine's owner writes where it ran")

	// Linked later, the account's past is theirs.
	require.NoError(t, s.UpsertIdentity(ctx, Identity{Login: "bo@acme.com", Provider: ProviderLinear, ExternalID: "org-1/u-bo"}))
	assert.Equal(t, RoleEditor, role("bo@acme.com"), "who touched the issue writes its conversation")
	touched, err := s.LinearTouched(ctx, key, "bo@acme.com")
	require.NoError(t, err)
	assert.True(t, touched)

	require.NoError(t, s.UpsertLinearIssue(ctx, LinearIssue{SourceKey: key, Identifier: "ENG-1", Title: "Fix login", TeamKey: "eng"}))
	require.NoError(t, s.UpsertLinearIssue(ctx, LinearIssue{SourceKey: key, StateName: "In Progress", StateType: "started"}))
	li, err := s.GetLinearIssue(ctx, key)
	require.NoError(t, err)
	assert.Equal(t, "ENG-1", li.Identifier, "a delivery that names no identifier keeps the last one")
	assert.Equal(t, "ENG", li.TeamKey)
	assert.Equal(t, "In Progress", li.StateName)

	mine, _, err := s.ListLinearConversations(ctx, "bo@acme.com", LinearConversationFilter{}, 50, "")
	require.NoError(t, err)
	require.Len(t, mine, 1)
	assert.Equal(t, LinearStateNew, mine[0].State)
	none, _, err := s.ListLinearConversations(ctx, "cy@acme.com", LinearConversationFilter{}, 50, "")
	require.NoError(t, err)
	assert.Empty(t, none, "somebody who never touched it does not list it")
	all, _, err := s.ListLinearConversations(ctx, "cy@acme.com", LinearConversationFilter{SeeAll: true}, 50, "")
	require.NoError(t, err)
	assert.Len(t, all, 1, "an admin lists every one")

	require.NoError(t, s.SetLinearIssueAsked(ctx, key, true, "bo@acme.com"))
	waiting, _, err := s.ListLinearConversations(ctx, "bo@acme.com", LinearConversationFilter{State: LinearStateAttention}, 50, "")
	require.NoError(t, err)
	require.Len(t, waiting, 1)
	assert.Equal(t, LinearStateWaiting, waiting[0].State)
	assert.Equal(t, "bo@acme.com", waiting[0].AskedOf)

	chats, _, err := s.ListChats(ctx, "bo@acme.com", false, 50, "", ListChatsScope{ExcludeLinear: true})
	require.NoError(t, err)
	assert.Empty(t, chats, "an issue's conversation is not in the chat rail")
}
