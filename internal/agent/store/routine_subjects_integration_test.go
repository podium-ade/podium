//go:build integration

package store

import (
	"context"
	"encoding/json"
	"fmt"
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

	minePage, err := s.ListLinearConversations(ctx, "bo@acme.com", LinearConversationFilter{}, 50, "")
	require.NoError(t, err)
	assert.Empty(t, minePage.Rows, "a commenter reads the conversation, and is not involved in it")
	require.NoError(t, s.RecordLinearPeople(ctx, key, []LinearPerson{
		{LinearUser: "org-1/u-bo", Via: LinearViaAssigned},
	}, time.Now()))
	minePage, err = s.ListLinearConversations(ctx, "bo@acme.com", LinearConversationFilter{}, 50, "")
	require.NoError(t, err)
	mine := minePage.Rows
	require.Len(t, mine, 1, "the assignee lists it")
	assert.Equal(t, LinearStateNew, mine[0].State)
	assert.True(t, mine[0].Involved)
	nonePage, err := s.ListLinearConversations(ctx, "cy@acme.com", LinearConversationFilter{}, 50, "")
	require.NoError(t, err)
	none := nonePage.Rows
	assert.Empty(t, none, "somebody who never touched it does not list it")
	adminPage, err := s.ListLinearConversations(ctx, "cy@acme.com", LinearConversationFilter{SeeAll: true}, 50, "")
	require.NoError(t, err)
	assert.Empty(t, adminPage.Rows, "an admin's own list is what they are involved in")
	allPage, err := s.ListLinearConversations(ctx, "cy@acme.com", LinearConversationFilter{SeeAll: true, All: true}, 50, "")
	require.NoError(t, err)
	all := allPage.Rows
	require.Len(t, all, 1, "an admin asking for every one lists it")
	assert.False(t, all[0].Involved)
	sneaky, err := s.ListLinearConversations(ctx, "cy@acme.com", LinearConversationFilter{All: true}, 50, "")
	require.NoError(t, err)
	assert.Empty(t, sneaky.Rows, "all lists what the caller may read, and no more")

	require.NoError(t, s.SetLinearIssueAsked(ctx, key, true, "bo@acme.com"))
	waitingPage, err := s.ListLinearConversations(ctx, "bo@acme.com", LinearConversationFilter{State: LinearStateAttention}, 50, "")
	require.NoError(t, err)
	waiting := waitingPage.Rows
	require.Len(t, waiting, 1)
	assert.Equal(t, LinearStateWaiting, waiting[0].State)
	assert.Equal(t, "bo@acme.com", waiting[0].AskedOf)

	chats, _, err := s.ListChats(ctx, "bo@acme.com", false, 50, "", ListChatsScope{ExcludeLinear: true})
	require.NoError(t, err)
	assert.Empty(t, chats, "an issue's conversation is not in the chat rail")
}

func TestTheBoardListsOneStateAPage(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	open := func(key, title string, li LinearIssue) {
		t.Helper()
		_, _, err := s.EnsureLinearConversation(ctx, key, title)
		require.NoError(t, err)
		li.SourceKey = key
		require.NoError(t, s.UpsertLinearIssue(ctx, li))
	}
	for i := range 4 {
		id := fmt.Sprintf("ENG-%d", i+1)
		open(LinearIssueKey("eng-"+id), id, LinearIssue{Identifier: id, TeamKey: "ENG", StateName: "In Progress", StateType: "started"})
	}
	open(LinearIssueKey("ops"), "OPS-1", LinearIssue{Identifier: "OPS-1", TeamKey: "OPS", StateName: "Todo", StateType: "unstarted"})
	open(LinearProjectKey("p1"), "Roadmap", LinearIssue{Title: "Roadmap"})
	require.NoError(t, s.SetLinearIssueAsked(ctx, LinearIssueKey("eng-ENG-4"), true, "bo@acme.com"))

	list := func(f LinearConversationFilter, limit int, cursor string) LinearConversationPage {
		t.Helper()
		f.SeeAll, f.All = true, true
		p, err := s.ListLinearConversations(ctx, "root@acme.com", f, limit, cursor)
		require.NoError(t, err)
		return p
	}

	first := list(LinearConversationFilter{State: LinearStateNew}, 2, "")
	assert.Len(t, first.Rows, 2)
	assert.Equal(t, 5, first.Total, "three ENG issues, the OPS one and the project are not started; ENG-4 waits")
	require.NotEmpty(t, first.Next)
	second := list(LinearConversationFilter{State: LinearStateNew}, 2, first.Next)
	assert.Len(t, second.Rows, 2)
	assert.Zero(t, second.Total, "only the first page counts")
	third := list(LinearConversationFilter{State: LinearStateNew}, 2, second.Next)
	assert.Len(t, third.Rows, 1)
	assert.Empty(t, third.Next)
	for _, r := range append(append(first.Rows, second.Rows...), third.Rows...) {
		assert.Equal(t, LinearStateNew, r.State)
	}

	eng := list(LinearConversationFilter{State: LinearStateNew, Team: "eng"}, 10, "")
	assert.Equal(t, 3, eng.Total, "the team filter applies to a column too")

	waiting := list(LinearConversationFilter{State: LinearStateWaiting}, 10, "")
	require.Len(t, waiting.Rows, 1)
	assert.Equal(t, "ENG-4", waiting.Rows[0].Identifier)
	assert.Equal(t, "started", waiting.Rows[0].StateType, "the tracker's state is still on the row")

	empty := list(LinearConversationFilter{State: LinearStateFailed}, 10, "")
	assert.Empty(t, empty.Rows)
	assert.Zero(t, empty.Total)

	nobody, err := s.ListLinearConversations(ctx, "cy@acme.com", LinearConversationFilter{State: LinearStateNew, All: true}, 10, "")
	require.NoError(t, err)
	assert.Empty(t, nobody.Rows, "a column lists only what the caller may read")
	assert.Zero(t, nobody.Total, "and counts only that")
}

// TestAPersonsListIsTheIssuesTheyAreInvolvedIn: assigned, created, asked Podium (delegated,
// mentioned the app, prompted), spoke in the conversation, or @mentioned in it. Commenting,
// changing it, owning a routine that ran in it or being shared it lets a person read the
// conversation, and leaves it out of their list. A mention alone grants no reading, so it lists
// the issue only for somebody who reads it another way.
func TestAPersonsListIsTheIssuesTheyAreInvolvedIn(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	now := time.Now()
	link := func(login, account string) {
		t.Helper()
		require.NoError(t, s.UpsertIdentity(ctx, Identity{Login: login, Provider: ProviderWeb, ExternalID: login}))
		require.NoError(t, s.UpsertIdentity(ctx, Identity{Login: login, Provider: ProviderLinear, ExternalID: account}))
	}
	cases := []struct {
		via             string
		listed, touched bool
	}{
		{LinearViaAssigned, true, true},
		{LinearViaCreated, true, true},
		{LinearViaDelegated, true, true},
		{LinearViaMentioned, true, true},
		{LinearViaPrompted, true, true},
		{LinearViaPersonMentioned, false, false},
		{LinearViaCommented, false, true},
		{LinearViaChanged, false, true},
	}
	for i, c := range cases {
		login := fmt.Sprintf("p%d@acme.com", i)
		account := fmt.Sprintf("org-1/u-%d", i)
		link(login, account)
		key := LinearIssueKey(fmt.Sprintf("iss-%d", i))
		_, _, err := s.EnsureLinearConversation(ctx, key, c.via)
		require.NoError(t, err)
		require.NoError(t, s.RecordLinearPeople(ctx, key, []LinearPerson{{LinearUser: account, Via: c.via}}, now))

		page, err := s.ListLinearConversations(ctx, login, LinearConversationFilter{}, 50, "")
		require.NoError(t, err)
		if c.listed {
			assert.Len(t, page.Rows, 1, c.via)
		} else {
			assert.Empty(t, page.Rows, c.via)
		}
		touched, err := s.LinearTouched(ctx, key, login)
		require.NoError(t, err)
		assert.Equal(t, c.touched, touched, "%s reads the conversation as before", c.via)
		if c.via == LinearViaPersonMentioned {
			rail, _, err := s.ListChats(ctx, login, false, 50, "", ListChatsScope{ExcludeLinear: true, LinearRail: true})
			require.NoError(t, err)
			assert.Empty(t, rail, "a mention alone puts nothing in the chat rail")
			// Who was @mentioned and also commented reads it, and lists it.
			require.NoError(t, s.RecordLinearPeople(ctx, key, []LinearPerson{{LinearUser: account, Via: LinearViaCommented}}, now))
			page, err = s.ListLinearConversations(ctx, login, LinearConversationFilter{}, 50, "")
			require.NoError(t, err)
			require.Len(t, page.Rows, 1, "mentioned and able to read it")
			assert.True(t, page.Rows[0].Involved)
		}
	}

	// A routine's owner and a person it was shared with read it, and do not list it.
	key := LinearIssueKey("iss-shared")
	chat, _, err := s.EnsureLinearConversation(ctx, key, "shared")
	require.NoError(t, err)
	require.NoError(t, s.UpsertIdentity(ctx, Identity{Login: "ro@acme.com", Provider: ProviderWeb, ExternalID: "ro@acme.com"}))
	require.NoError(t, s.UpsertIdentity(ctx, Identity{Login: "sh@acme.com", Provider: ProviderWeb, ExternalID: "sh@acme.com"}))
	require.NoError(t, s.RecordLinearPeople(ctx, key, []LinearPerson{{Login: "ro@acme.com", Via: LinearViaRoutine}}, now))
	_, err = s.ShareChat(ctx, chat.ID, "sh@acme.com", RoleViewer, "root@acme.com")
	require.NoError(t, err)
	for _, login := range []string{"ro@acme.com", "sh@acme.com"} {
		role, err := s.ChatRole(ctx, chat.ID, login)
		require.NoError(t, err)
		assert.NotEmpty(t, role, login)
		page, err := s.ListLinearConversations(ctx, login, LinearConversationFilter{}, 50, "")
		require.NoError(t, err)
		assert.Empty(t, page.Rows, login)
	}

	// Speaking in the conversation from Podium is asking Podium; a backfilled guess is not.
	_, err = s.AddChatParticipant(ctx, chat.ID, "sh@acme.com", JoinedWeb, now)
	require.NoError(t, err)
	page, err := s.ListLinearConversations(ctx, "sh@acme.com", LinearConversationFilter{}, 50, "")
	require.NoError(t, err)
	require.Len(t, page.Rows, 1)
	assert.True(t, page.Rows[0].Involved)
	teams, err := s.LinearConversationTeams(ctx, "sh@acme.com", false, false)
	require.NoError(t, err)
	assert.Empty(t, teams, "the issue names no team")
	_, err = s.AddChatParticipant(ctx, chat.ID, "bf@acme.com", JoinedBackfill, now)
	require.NoError(t, err)
	page, err = s.ListLinearConversations(ctx, "bf@acme.com", LinearConversationFilter{}, 50, "")
	require.NoError(t, err)
	assert.Empty(t, page.Rows)

	// Linked later, an account's assignment is theirs from then on.
	other := LinearIssueKey("iss-later")
	_, _, err = s.EnsureLinearConversation(ctx, other, "later")
	require.NoError(t, err)
	require.NoError(t, s.UpsertLinearIssue(ctx, LinearIssue{SourceKey: other, Identifier: "ENG-77", TeamKey: "ENG"}))
	require.NoError(t, s.RecordLinearPeople(ctx, other, []LinearPerson{{LinearUser: "org-1/u-late", Via: LinearViaAssigned}}, now))
	page, err = s.ListLinearConversations(ctx, "late@acme.com", LinearConversationFilter{}, 50, "")
	require.NoError(t, err)
	assert.Empty(t, page.Rows)
	link("late@acme.com", "org-1/u-late")
	page, err = s.ListLinearConversations(ctx, "late@acme.com", LinearConversationFilter{}, 50, "")
	require.NoError(t, err)
	assert.Len(t, page.Rows, 1)
	teams, err = s.LinearConversationTeams(ctx, "late@acme.com", false, false)
	require.NoError(t, err)
	assert.Equal(t, []string{"ENG"}, teams)
}
