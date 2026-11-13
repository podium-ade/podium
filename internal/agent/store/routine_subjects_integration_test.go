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
	assert.Contains(t, revs[1].Schedule, "a chat per ticket")
}

func TestEventsAreGroupedByTicketAndEachTicketGetsOneChat(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	r, err := s.CreateRoutine(ctx, "ana@acme.com", perTicketDraft(), "")
	require.NoError(t, err)
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
	assert.Empty(t, subjects[0].ChatID)
	assert.Equal(t, "Issue:iss-b", subjects[1].Key)
	assert.Empty(t, subjects[2].Key, "a comment on no issue is about nothing in particular")

	chatID, created, err := s.EnsureRoutineSubjectChat(ctx, r, subjects[0].Key, subjects[0].Title)
	require.NoError(t, err)
	assert.True(t, created)
	again, created, err := s.EnsureRoutineSubjectChat(ctx, r, subjects[0].Key, subjects[0].Title)
	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, chatID, again, "one chat per ticket")
	own, _, err := s.EnsureRoutineSubjectChat(ctx, r, "", "")
	require.NoError(t, err)
	assert.Equal(t, r.ChatID, own)

	c, err := s.GetChat(ctx, chatID)
	require.NoError(t, err)
	assert.Equal(t, "ana@acme.com", c.Login, "opened as the routine's own chat was")
	assert.Equal(t, r.ID, c.RoutineID, "so it is never shared and gets the routine's brief")
	assert.Equal(t, "Triage new issues · ENG-1 Fix login", c.Title)
	byChat, err := s.RoutineByChat(ctx, chatID)
	require.NoError(t, err)
	assert.Equal(t, r.ID, byChat.ID)
	sc, err := s.RoutineSubjectOfChat(ctx, chatID)
	require.NoError(t, err)
	assert.Equal(t, "Issue:iss-a", sc.SubjectKey)
	_, err = s.RoutineSubjectOfChat(ctx, r.ChatID)
	assert.ErrorIs(t, err, ErrNotFound)

	opened, err := s.CountRoutineSubjectChatsSince(ctx, r.ID, time.Now().Add(-24*time.Hour))
	require.NoError(t, err)
	assert.Equal(t, 1, opened)

	run, ok, err := s.ClaimRoutineRun(ctx, r.ID, time.Now(), RoutineTriggerEvent, RoutineRunRunning)
	require.NoError(t, err)
	require.True(t, ok)
	n, err := s.AttachRoutineSubjectEvents(ctx, run.ID, r.ID, "Issue:iss-a", 20)
	require.NoError(t, err)
	assert.Equal(t, 2, n, "every event about that ticket and no other")
	require.NoError(t, s.SetRoutineRunChat(ctx, run.ID, chatID))
	running, err := s.RunningRoutineRun(ctx, r.ID)
	require.NoError(t, err)
	assert.Equal(t, chatID, running.ChatID)

	subjects, err = s.PendingRoutineSubjects(ctx, r.ID)
	require.NoError(t, err)
	require.Len(t, subjects, 2)
	n, err = s.AttachRoutineSubjectEvents(ctx, run.ID, r.ID, "", 20)
	require.NoError(t, err)
	assert.Equal(t, 1, n, "the events about nothing in particular are a group of their own")
}

func TestATicketChatIsListedOnceARunInItSaidSomething(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	r, err := s.CreateRoutine(ctx, "ana@acme.com", perTicketDraft(), "")
	require.NoError(t, err)
	chatID, _, err := s.EnsureRoutineSubjectChat(ctx, r, "Issue:iss-a", "ENG-1 Fix login")
	require.NoError(t, err)

	listed := func() bool {
		chats, _, err := s.ListChats(ctx, "ana@acme.com", false, 50, "")
		require.NoError(t, err)
		for _, c := range chats {
			if c.ID == chatID {
				return true
			}
		}
		return false
	}
	now := time.Now().UTC()
	for _, m := range []ChatMessage{
		{ChatID: chatID, Role: RoleNotice, Text: "Routine run · nothing to do", TS: now},
		{ChatID: chatID, Role: RoleProgress, Text: "Nothing new on ENG-1.", TS: now},
	} {
		_, err := s.AppendChatMessage(ctx, m)
		require.NoError(t, err)
	}
	assert.False(t, listed(), "a chat holding only quiet runs stays out of the list")

	_, err = s.AppendChatMessage(ctx, ChatMessage{ChatID: chatID, Role: RoleAssistant, Text: "ENG-1 is labelled.", TS: now})
	require.NoError(t, err)
	assert.True(t, listed(), "a run that said something lists it")
}

func TestDeletingATicketChatLetsTheNextEventOpenAFreshOne(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	r, err := s.CreateRoutine(ctx, "ana@acme.com", perTicketDraft(), "")
	require.NoError(t, err)
	first, _, err := s.EnsureRoutineSubjectChat(ctx, r, "Issue:iss-a", "ENG-1 Fix login")
	require.NoError(t, err)
	require.NoError(t, s.DeleteChat(ctx, first, "ana@acme.com", false))
	_, err = s.RoutineSubjectOfChat(ctx, first)
	assert.ErrorIs(t, err, ErrNotFound)
	fresh, created, err := s.EnsureRoutineSubjectChat(ctx, r, "Issue:iss-a", "ENG-1 Fix login")
	require.NoError(t, err)
	assert.True(t, created)
	assert.NotEqual(t, first, fresh)

	// Deleting the routine keeps every chat of it, as ordinary chats.
	require.NoError(t, s.DeleteRoutine(ctx, r.ID))
	c, err := s.GetChat(ctx, fresh)
	require.NoError(t, err)
	assert.Empty(t, c.RoutineID)
	_, err = s.RoutineSubjectOfChat(ctx, fresh)
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestASystemRoutinesTicketChatHasNoOwner(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	r, err := s.CreateSystemRoutine(ctx, "admin@acme.com", perTicketDraft())
	require.NoError(t, err)
	chatID, created, err := s.EnsureRoutineSubjectChat(ctx, r, "Project:prj-1", "")
	require.NoError(t, err)
	require.True(t, created)
	c, err := s.GetChat(ctx, chatID)
	require.NoError(t, err)
	assert.Empty(t, c.Login)
	assert.Equal(t, r.ID, c.RoutineID)
	assert.Equal(t, "Triage new issues · Project:prj-1", c.Title, "the key stands in for a title Linear did not send")
}
