//go:build integration

package store

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// runningTurn puts a session and a running turn in the database for one chat, which is what
// ListChats and ChatTurnRunning read to answer "is the composer disabled".
func runningTurn(t *testing.T, s *Store, chatID string) Turn {
	t.Helper()
	ctx := context.Background()
	sess, err := s.UpsertSession(ctx, Session{
		SourceKind: "chat", SourceKey: ChatSourceKey(chatID), Profile: "podium", Playbook: "analyst",
	})
	require.NoError(t, err)
	turn, err := s.CreateTurn(ctx, sess.ID, chatID, Backend{})
	require.NoError(t, err)
	return turn
}

func TestCreateAndReadAChat(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	chat, err := s.CreateChat(ctx, "alice", "")
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(chat.ID, "chat_"), "ids are prefixed ULIDs: %s", chat.ID)
	assert.Equal(t, DefaultChatTitle, chat.Title, "an empty title gets a name rather than none")
	assert.Equal(t, "alice", chat.Login)
	assert.False(t, chat.CreatedAt.IsZero())

	read, err := s.GetChat(ctx, chat.ID)
	require.NoError(t, err)
	assert.Equal(t, chat, read)

	titled, err := s.CreateChat(ctx, "alice", "August numbers")
	require.NoError(t, err)
	assert.Equal(t, "August numbers", titled.Title)

	_, err = s.GetChat(ctx, "chat_nope")
	assert.ErrorIs(t, err, ErrNotFound)

	_, err = s.CreateChat(ctx, "", "no owner")
	assert.ErrorContains(t, err, "a login is required")
}

func TestTwoLoginsSeeDisjointChatLists(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	alice, err := s.CreateChat(ctx, "alice", "alice's chat")
	require.NoError(t, err)
	bob, err := s.CreateChat(ctx, "bob", "bob's chat")
	require.NoError(t, err)

	aliceChats, _, err := s.ListChats(ctx, "alice", 0, "")
	require.NoError(t, err)
	require.Len(t, aliceChats, 1)
	assert.Equal(t, alice.ID, aliceChats[0].ID)

	bobChats, _, err := s.ListChats(ctx, "bob", 0, "")
	require.NoError(t, err)
	require.Len(t, bobChats, 1)
	assert.Equal(t, bob.ID, bobChats[0].ID)

	// The login is in the query, so no cursor reaches somebody else's chat: bob's is not
	// in alice's list whether the page starts before it or after it.
	for _, cursor := range []string{"", alice.ID, bob.ID} {
		page, _, err := s.ListChats(ctx, "alice", 0, cursor)
		require.NoError(t, err)
		for _, c := range page {
			assert.Equal(t, "alice", c.Login, "cursor %q leaked a chat", cursor)
		}
	}

	_, _, err = s.ListChats(ctx, "", 0, "")
	assert.ErrorContains(t, err, "a login is required")
}

func TestTheChatListCarriesWhatTheRailShows(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	empty, err := s.CreateChat(ctx, "alice", "nothing said here")
	require.NoError(t, err)
	spoken, err := s.CreateChat(ctx, "alice", "August numbers")
	require.NoError(t, err)

	_, err = s.AppendChatMessage(ctx, ChatMessage{
		ChatID: spoken.ID, Role: RoleUser, Text: "how many active accounts last month",
	})
	require.NoError(t, err)
	last, err := s.AppendChatMessage(ctx, ChatMessage{
		ChatID: spoken.ID, Role: RoleAssistant,
		Text: strings.Repeat("é", ChatPreviewChars+20),
	})
	require.NoError(t, err)

	chats, next, err := s.ListChats(ctx, "alice", 0, "")
	require.NoError(t, err)
	require.Len(t, chats, 2)
	assert.Empty(t, next, "one page holds both")
	// Newest first: ULIDs sort by time, so the later chat leads.
	assert.Equal(t, spoken.ID, chats[0].ID)
	assert.Equal(t, empty.ID, chats[1].ID)

	require.NotNil(t, chats[0].LastMessageAt)
	assert.WithinDuration(t, last.TS, *chats[0].LastMessageAt, time.Second)
	// The preview is the LAST message, cut on a rune boundary rather than a byte.
	assert.Equal(t, strings.Repeat("é", ChatPreviewChars)+"…", chats[0].Preview)

	assert.Nil(t, chats[1].LastMessageAt, "a chat nobody has spoken in has no last message")
	assert.Empty(t, chats[1].Preview)
	assert.False(t, chats[0].TurnRunning)
}

func TestTheChatListReportsARunningTurn(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	chat, err := s.CreateChat(ctx, "alice", "August numbers")
	require.NoError(t, err)
	other, err := s.CreateChat(ctx, "alice", "something else")
	require.NoError(t, err)

	running, err := s.ChatTurnRunning(ctx, chat.ID)
	require.NoError(t, err)
	assert.False(t, running)

	turn := runningTurn(t, s, chat.ID)
	running, err = s.ChatTurnRunning(ctx, chat.ID)
	require.NoError(t, err)
	assert.True(t, running)

	// The flag is per chat, not per login.
	running, err = s.ChatTurnRunning(ctx, other.ID)
	require.NoError(t, err)
	assert.False(t, running)

	chats, _, err := s.ListChats(ctx, "alice", 0, "")
	require.NoError(t, err)
	byID := map[string]Chat{}
	for _, c := range chats {
		byID[c.ID] = c
	}
	assert.True(t, byID[chat.ID].TurnRunning)
	assert.False(t, byID[other.ID].TurnRunning)

	require.NoError(t, s.FinishTurn(ctx, turn.ID, TurnSucceeded, nil, nil, "4,812."))
	running, err = s.ChatTurnRunning(ctx, chat.ID)
	require.NoError(t, err)
	assert.False(t, running, "a finished turn releases the composer")
}

func TestChatMessageSeqIsMonotonicPerChatUnderConcurrency(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	one, err := s.CreateChat(ctx, "alice", "one")
	require.NoError(t, err)
	two, err := s.CreateChat(ctx, "alice", "two")
	require.NoError(t, err)

	const writers = 12
	var wg sync.WaitGroup
	seqs := make(chan uint64, writers)
	errs := make(chan error, writers)
	for i := range writers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			msg, err := s.AppendChatMessage(ctx, ChatMessage{
				ChatID: one.ID, Role: RoleUser, Text: fmt.Sprintf("message %d", i),
			})
			if err != nil {
				errs <- err
				return
			}
			seqs <- msg.Seq
		}(i)
	}
	wg.Wait()
	close(seqs)
	close(errs)
	for err := range errs {
		// Two appends racing for one seq is a primary-key violation, which is the right
		// answer: nothing is silently overwritten.
		assert.ErrorContains(t, err, "chat_messages_pkey")
	}

	// Whatever landed is contiguous, and no seq was handed out twice.
	seen := map[uint64]bool{}
	for seq := range seqs {
		assert.False(t, seen[seq], "seq %d was handed out twice", seq)
		seen[seq] = true
	}
	stored, err := s.ListChatMessages(ctx, one.ID, 0)
	require.NoError(t, err)
	require.NotEmpty(t, stored)
	for i, m := range stored {
		assert.Equal(t, uint64(i+1), m.Seq, "seqs are contiguous from 1")
	}

	// A second chat has its own seq space.
	msg, err := s.AppendChatMessage(ctx, ChatMessage{ChatID: two.ID, Role: RoleUser, Text: "hello"})
	require.NoError(t, err)
	assert.Equal(t, uint64(1), msg.Seq)
}

func TestListChatMessagesFromSeqIsExclusive(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	chat, err := s.CreateChat(ctx, "alice", "one")
	require.NoError(t, err)

	for i := range 4 {
		_, err := s.AppendChatMessage(ctx, ChatMessage{
			ChatID: chat.ID, Role: RoleUser, Text: fmt.Sprintf("message %d", i),
		})
		require.NoError(t, err)
	}
	all, err := s.ListChatMessages(ctx, chat.ID, 0)
	require.NoError(t, err)
	assert.Len(t, all, 4)

	tail, err := s.ListChatMessages(ctx, chat.ID, 2)
	require.NoError(t, err)
	require.Len(t, tail, 2, "from_seq is exclusive: replay-then-follow is exactly once")
	assert.Equal(t, uint64(3), tail[0].Seq)

	none, err := s.ListChatMessages(ctx, chat.ID, 4)
	require.NoError(t, err)
	assert.Empty(t, none)
}

func TestAttachmentsRoundTripAndLandOnTheLastAssistantMessage(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	chat, err := s.CreateChat(ctx, "alice", "one")
	require.NoError(t, err)

	// Nothing to attach to yet: the turn produced a file and said nothing.
	_, err = s.AttachToLastAssistantMessage(ctx, chat.ID, ChatAttachment{ArtifactID: "art_01", Name: "a.csv"})
	assert.ErrorIs(t, err, ErrNotFound)

	_, err = s.AppendChatMessage(ctx, ChatMessage{ChatID: chat.ID, Role: RoleUser, Text: "chart it"})
	require.NoError(t, err)
	final, err := s.AppendChatMessage(ctx, ChatMessage{
		ChatID: chat.ID, Role: RoleAssistant, Text: "See report.csv and trend.png.",
	})
	require.NoError(t, err)
	assert.Empty(t, final.Attachments, "a nil slice stays nil rather than becoming the literal null")

	csv := ChatAttachment{ArtifactID: "art_01", Name: "report.csv", ContentType: "text/csv", SizeBytes: 4096}
	png := ChatAttachment{ArtifactID: "art_02", Name: "trend.png", ContentType: "image/png", SizeBytes: 91_000}
	updated, err := s.AttachToLastAssistantMessage(ctx, chat.ID, csv)
	require.NoError(t, err)
	assert.Equal(t, final.Seq, updated.Seq, "the attachment lands on the final, not on a new message")
	updated, err = s.AttachToLastAssistantMessage(ctx, chat.ID, png)
	require.NoError(t, err)
	assert.Equal(t, []ChatAttachment{csv, png}, updated.Attachments)

	// Attaching the same artifact twice is not two chips.
	updated, err = s.AttachToLastAssistantMessage(ctx, chat.ID, csv)
	require.NoError(t, err)
	assert.Len(t, updated.Attachments, 2)

	read, err := s.ListChatMessages(ctx, chat.ID, 0)
	require.NoError(t, err)
	require.Len(t, read, 2)
	assert.Equal(t, []ChatAttachment{csv, png}, read[1].Attachments)
	assert.Empty(t, read[0].Attachments)
}

func TestAMessageForAChatThatIsNotThereIsRefused(t *testing.T) {
	s := newStore(t)
	// chat_messages.chat_id references chats(id), so a message can never outlive its chat
	// or arrive before it.
	_, err := s.AppendChatMessage(context.Background(), ChatMessage{
		ChatID: "chat_nope", Role: RoleUser, Text: "hello",
	})
	assert.ErrorContains(t, err, "chat_messages_chat_id_fkey")
}

func TestAChatMayBeRenamed(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	chat, err := s.CreateChat(ctx, "alice", "")
	require.NoError(t, err)
	assert.True(t, chat.AutoTitle)

	titled, err := s.SetChatTitle(ctx, chat.ID, "August numbers")
	require.NoError(t, err)
	assert.Equal(t, "August numbers", titled.Title)

	owned, err := s.CreateChat(ctx, "alice", "Keep this")
	require.NoError(t, err)
	assert.False(t, owned.AutoTitle)
	kept, err := s.SetChatTitle(ctx, owned.ID, "overwrite")
	require.NoError(t, err)
	assert.Equal(t, "Keep this", kept.Title, "a title supplied at create is never rewritten")

	listed, _, err := s.ListChats(ctx, "alice", 0, "")
	require.NoError(t, err)
	byID := map[string]Chat{}
	for _, c := range listed {
		byID[c.ID] = c
	}
	assert.Equal(t, "August numbers", byID[chat.ID].Title)

	// A rename is the owner's word on the name, so a later turn must not write over it.
	human, err := s.RenameChat(ctx, chat.ID, "alice", "Q3 forecast")
	require.NoError(t, err)
	assert.False(t, human.AutoTitle)
	fromTurn, err := s.SetChatTitle(ctx, chat.ID, "a model wrote this")
	require.NoError(t, err)
	assert.Equal(t, "Q3 forecast", fromTurn.Title)
}

func TestRunningChatTaskIsTheTurnInFlight(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	chat, err := s.CreateChat(ctx, "alice", "August numbers")
	require.NoError(t, err)
	id, err := s.RunningChatTask(ctx, chat.ID)
	require.NoError(t, err)
	assert.Empty(t, id, "a chat that has never had a turn has no task to cancel")

	turn := runningTurn(t, s, chat.ID)
	id, err = s.RunningChatTask(ctx, chat.ID)
	require.NoError(t, err)
	assert.Empty(t, id, "CreateTask has not answered yet, so there is nothing to cancel")

	require.NoError(t, s.SetTurnTask(ctx, turn.ID, "task_01xyz"))
	id, err = s.RunningChatTask(ctx, chat.ID)
	require.NoError(t, err)
	assert.Equal(t, "task_01xyz", id)

	other, err := s.CreateChat(ctx, "alice", "something else")
	require.NoError(t, err)
	id, err = s.RunningChatTask(ctx, other.ID)
	require.NoError(t, err)
	assert.Empty(t, id, "the running task is per chat, not per login")

	require.NoError(t, s.FinishTurn(ctx, turn.ID, TurnSucceeded, nil, nil, "4,812."))
	id, err = s.RunningChatTask(ctx, chat.ID)
	require.NoError(t, err)
	assert.Empty(t, id, "a finished turn is no longer a task to cancel")
}

func TestDeleteChatTakesItsMessagesAndNobodyElses(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	alice, err := s.CreateChat(ctx, "alice", "alice's")
	require.NoError(t, err)
	_, err = s.AppendChatMessage(ctx, ChatMessage{ChatID: alice.ID, Role: RoleUser, Text: "hello"})
	require.NoError(t, err)
	bob, err := s.CreateChat(ctx, "bob", "bob's")
	require.NoError(t, err)

	err = s.DeleteChat(ctx, alice.ID, "bob")
	assert.ErrorIs(t, err, ErrNotFound, "another login's chat is not there, not forbidden")
	_, err = s.GetChat(ctx, alice.ID)
	require.NoError(t, err, "bob's attempt must leave alice's chat")

	require.NoError(t, s.DeleteChat(ctx, alice.ID, "alice"))
	_, err = s.GetChat(ctx, alice.ID)
	assert.ErrorIs(t, err, ErrNotFound)
	msgs, err := s.ListChatMessages(ctx, alice.ID, 0)
	require.NoError(t, err)
	assert.Empty(t, msgs, "the messages go with the chat")

	_, err = s.GetChat(ctx, bob.ID)
	require.NoError(t, err, "bob's chat is not in alice's delete")

	err = s.DeleteChat(ctx, alice.ID, "alice")
	assert.ErrorIs(t, err, ErrNotFound, "deleting a chat that is already gone is not found")

	err = s.DeleteChat(ctx, "", "alice")
	assert.ErrorContains(t, err, "an id is required")
	err = s.DeleteChat(ctx, alice.ID, "")
	assert.ErrorContains(t, err, "a login is required")
}

func TestChatPreviewCutsRunesNotBytes(t *testing.T) {
	assert.Equal(t, "", preview("   ", 5))
	assert.Equal(t, "hello", preview("  hello  ", 5))
	assert.Equal(t, "héllo…", preview("héllo world", 6), "a trailing space before the ellipsis reads as a typo")
	assert.Equal(t, "héllo", preview("héllo", 6))
}

func TestRenameChat(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	chat, err := s.CreateChat(ctx, "alice", "August numbers")
	require.NoError(t, err)
	bob, err := s.CreateChat(ctx, "bob", "bob's chat")
	require.NoError(t, err)

	renamed, err := s.RenameChat(ctx, chat.ID, "alice", "  Q3   forecast ")
	require.NoError(t, err)
	assert.Equal(t, "Q3 forecast", renamed.Title)
	assert.Equal(t, chat.ID, renamed.ID)

	read, err := s.GetChat(ctx, chat.ID)
	require.NoError(t, err)
	assert.Equal(t, "Q3 forecast", read.Title)

	_, err = s.RenameChat(ctx, chat.ID, "bob", "stolen")
	assert.ErrorIs(t, err, ErrNotFound, "knowing the id is not access")
	still, err := s.GetChat(ctx, chat.ID)
	require.NoError(t, err)
	assert.Equal(t, "Q3 forecast", still.Title, "a refused rename must not write")

	_, err = s.RenameChat(ctx, bob.ID, "alice", "stolen")
	assert.ErrorIs(t, err, ErrNotFound)

	_, err = s.RenameChat(ctx, "chat_nope", "alice", "gone")
	assert.ErrorIs(t, err, ErrNotFound)

	_, err = s.RenameChat(ctx, chat.ID, "alice", "   ")
	assert.ErrorIs(t, err, ErrInvalidChatTitle)

	_, err = s.RenameChat(ctx, chat.ID, "", "no owner")
	assert.ErrorContains(t, err, "a login is required")
}

// linkPR is one pull request as the conductor would hand it over.
func linkPR(chatID, owner, repo string, number int) ChatPullRequest {
	return ChatPullRequest{
		ChatID: chatID,
		URL:    fmt.Sprintf("https://github.com/%s/%s/pull/%d", owner, repo, number),
		Owner:  owner,
		Repo:   repo,
		Number: number,
	}
}

func urlsOf(prs []ChatPullRequest) []string {
	out := make([]string, 0, len(prs))
	for _, pr := range prs {
		out = append(out, pr.URL)
	}
	return out
}

func TestAChatCarriesThePullRequestsItsTurnsProduced(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	chat, err := s.CreateChat(ctx, "alice", "the fix")
	require.NoError(t, err)

	first, err := s.LinkChatPullRequest(ctx, linkPR(chat.ID, "acme", "api", 41))
	require.NoError(t, err)
	assert.True(t, first, "the first time is what links it")

	again, err := s.LinkChatPullRequest(ctx, linkPR(chat.ID, "acme", "api", 41))
	require.NoError(t, err)
	assert.False(t, again, "a second turn naming the same one links nothing")

	_, err = s.LinkChatPullRequest(ctx, linkPR(chat.ID, "acme", "web", 12))
	require.NoError(t, err)

	prs, err := s.ListChatPullRequests(ctx, chat.ID)
	require.NoError(t, err)
	require.Len(t, prs, 2)
	assert.Equal(t, []string{
		"https://github.com/acme/api/pull/41",
		"https://github.com/acme/web/pull/12",
	}, urlsOf(prs), "oldest first: the order the work happened in")
	assert.Equal(t, PullRequestFromTurn, prs[0].Source)
	assert.Equal(t, 41, prs[0].Number)
	assert.False(t, prs[0].CreatedAt.IsZero())
}

func TestADetachedPullRequestStaysDetachedUntilAHumanAsksForItBack(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	chat, err := s.CreateChat(ctx, "alice", "the fix")
	require.NoError(t, err)
	_, err = s.LinkChatPullRequest(ctx, linkPR(chat.ID, "acme", "api", 41))
	require.NoError(t, err)

	url := "https://github.com/acme/api/pull/41"
	require.NoError(t, s.DetachChatPullRequest(ctx, chat.ID, url))
	prs, err := s.ListChatPullRequests(ctx, chat.ID)
	require.NoError(t, err)
	assert.Empty(t, prs)

	// The next turn in the same chat mentions it again. It must not come back: a person
	// removing a link means it.
	linked, err := s.LinkChatPullRequest(ctx, linkPR(chat.ID, "acme", "api", 41))
	require.NoError(t, err)
	assert.False(t, linked)
	prs, err = s.ListChatPullRequests(ctx, chat.ID)
	require.NoError(t, err)
	assert.Empty(t, prs)

	// Attaching it by hand is the person asking for it back, and the link is theirs now.
	row, err := s.AttachChatPullRequest(ctx, linkPR(chat.ID, "acme", "api", 41))
	require.NoError(t, err)
	assert.Equal(t, PullRequestFromHuman, row.Source)
	prs, err = s.ListChatPullRequests(ctx, chat.ID)
	require.NoError(t, err)
	require.Len(t, prs, 1)
	assert.Equal(t, PullRequestFromHuman, prs[0].Source)

	// Detaching twice is not found the second time: it was not there, or it was already
	// off, and the two are the same answer.
	require.NoError(t, s.DetachChatPullRequest(ctx, chat.ID, url))
	assert.ErrorIs(t, s.DetachChatPullRequest(ctx, chat.ID, url), ErrNotFound)
	assert.ErrorIs(t, s.DetachChatPullRequest(ctx, chat.ID,
		"https://github.com/acme/api/pull/9"), ErrNotFound)
}

func TestDeletingAChatLeavesNoPullRequestsBehind(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	alice, err := s.CreateChat(ctx, "alice", "alice's")
	require.NoError(t, err)
	bob, err := s.CreateChat(ctx, "bob", "bob's")
	require.NoError(t, err)
	_, err = s.LinkChatPullRequest(ctx, linkPR(alice.ID, "acme", "api", 41))
	require.NoError(t, err)
	// The same pull request on two chats is two links: a row belongs to a conversation.
	_, err = s.LinkChatPullRequest(ctx, linkPR(bob.ID, "acme", "api", 41))
	require.NoError(t, err)
	// A detached link is still a row, and the cascade has to take those too.
	require.NoError(t, s.DetachChatPullRequest(ctx, alice.ID,
		"https://github.com/acme/api/pull/41"))

	require.NoError(t, s.DeleteChat(ctx, alice.ID, "alice"))

	var rows int
	require.NoError(t, s.pool.QueryRow(ctx,
		"select count(*) from chat_pull_requests where chat_id = $1", alice.ID).Scan(&rows))
	assert.Zero(t, rows, "the links go with the chat, tombstones included")

	prs, err := s.ListChatPullRequests(ctx, bob.ID)
	require.NoError(t, err)
	assert.Len(t, prs, 1, "bob's chat is not in alice's delete")
}

// ---------------------------------------------------------------------------
// mirrored conversations
// ---------------------------------------------------------------------------

const slackKey = "slack:C1:100.1"

func TestAMirroredChatHasNoOwnerAndSaysWhoStartedIt(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	chat, err := s.CreateMirrorChat(ctx, slackKey, OriginSlack, "alice", "what does this repo do?")
	require.NoError(t, err)

	assert.Empty(t, chat.Login, "a mirrored conversation belongs to the workspace, not a login")
	assert.Equal(t, OriginSlack, chat.Origin)
	assert.Equal(t, "alice", chat.StartedBy)
	assert.Equal(t, "what does this repo do?", chat.Title)
	assert.True(t, chat.AutoTitle, "a later turn may still improve the name")

	same, err := s.ChatBySourceKey(ctx, slackKey)
	require.NoError(t, err)
	assert.Equal(t, chat.ID, same.ID)
}

// One thread is one chat. The source key is unique, so a second attempt is refused rather
// than quietly splitting a conversation in two.
func TestOneThreadCannotBecomeTwoMirroredChats(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	_, err := s.CreateMirrorChat(ctx, slackKey, OriginSlack, "alice", "first")
	require.NoError(t, err)
	_, err = s.CreateMirrorChat(ctx, slackKey, OriginSlack, "bob", "second")
	require.Error(t, err)
}

func TestCreateMirrorChatRefusesAWebOrigin(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	_, err := s.CreateMirrorChat(ctx, slackKey, OriginWeb, "alice", "hello")
	require.ErrorContains(t, err, "not a mirrored origin")
	_, err = s.CreateMirrorChat(ctx, "", OriginSlack, "alice", "hello")
	require.ErrorContains(t, err, "source key is required")
}

// The participants are the humans, in the order they first spoke — and the bot is not one
// of them however many times it answered.
func TestChatParticipantsAreThePeopleInFirstAppearanceOrder(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	chat, err := s.CreateMirrorChat(ctx, slackKey, OriginSlack, "alice", "one")
	require.NoError(t, err)
	for _, m := range []ChatMessage{
		{Role: RoleUser, Author: "alice", Text: "one"},
		{Role: RoleAssistant, Author: "Podium", Text: "answer"},
		{Role: RoleUser, Author: "bob", Text: "two"},
		{Role: RoleUser, Author: "alice", Text: "three"},
		{Role: RoleUser, Author: "", Text: "from nobody in particular"},
	} {
		m.ChatID = chat.ID
		_, err := s.AppendChatMessage(ctx, m)
		require.NoError(t, err)
	}

	people, err := s.ChatParticipants(ctx, chat.ID)
	require.NoError(t, err)
	assert.Equal(t, []string{"alice", "bob"}, people)
}

func TestAMessageRemembersWhoSaidIt(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	chat, err := s.CreateMirrorChat(ctx, slackKey, OriginSlack, "alice", "one")
	require.NoError(t, err)
	stored, err := s.AppendChatMessage(ctx, ChatMessage{
		ChatID: chat.ID, Role: RoleUser, Author: "alice", Text: "one",
	})
	require.NoError(t, err)
	assert.Equal(t, "alice", stored.Author)

	read, err := s.ListChatMessages(ctx, chat.ID, 0)
	require.NoError(t, err)
	require.Len(t, read, 1)
	assert.Equal(t, "alice", read[0].Author)
}

// A mirrored conversation is in every login's list, because it belongs to the workspace.
// A web chat is still its owner's alone.
func TestTheChatListShowsMirroredConversationsToEveryLogin(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	mine, err := s.CreateChat(ctx, "alice", "my own")
	require.NoError(t, err)
	theirs, err := s.CreateChat(ctx, "bob", "not mine")
	require.NoError(t, err)
	thread, err := s.CreateMirrorChat(ctx, slackKey, OriginSlack, "carol", "in slack")
	require.NoError(t, err)

	ids := func(login string) map[string]Chat {
		chats, _, err := s.ListChats(ctx, login, 0, "")
		require.NoError(t, err)
		out := map[string]Chat{}
		for _, c := range chats {
			out[c.ID] = c
		}
		return out
	}

	forAlice := ids("alice")
	assert.Contains(t, forAlice, mine.ID)
	assert.Contains(t, forAlice, thread.ID)
	assert.NotContains(t, forAlice, theirs.ID)

	forBob := ids("bob")
	assert.Contains(t, forBob, theirs.ID)
	assert.Contains(t, forBob, thread.ID, "the same thread, in somebody else's list")
	assert.NotContains(t, forBob, mine.ID)

	assert.Equal(t, "carol", forAlice[thread.ID].StartedBy)
	assert.Equal(t, OriginSlack, forAlice[thread.ID].Origin)
	assert.Equal(t, OriginWeb, forAlice[mine.ID].Origin)
}

// A mirrored thread belongs to the workspace: every login lists it, so any login may rename
// it or delete the copy — the same rule ListChats applies, and the only one that lets a
// reader clear a thread out of the sidebar. The thread itself lives in Slack; its next
// message mirrors it again.
func TestAMirroredChatMayBeRenamedAndDeletedByWhoeverSeesIt(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	thread, err := s.CreateMirrorChat(ctx, slackKey, OriginSlack, "alice", "in slack")
	require.NoError(t, err)

	renamed, err := s.RenameChat(ctx, thread.ID, "bob", "mine now")
	require.NoError(t, err)
	assert.Equal(t, "mine now", renamed.Title)
	assert.Empty(t, renamed.Login, "renaming does not adopt it")

	require.NoError(t, s.DeleteChat(ctx, thread.ID, "bob"))
	_, err = s.ChatBySourceKey(ctx, slackKey)
	require.ErrorIs(t, err, ErrNotFound, "the key is free for the thread's next message to reopen")
	require.ErrorIs(t, s.DeleteChat(ctx, thread.ID, "bob"), ErrNotFound)
}

// The running flag follows the session that owns the conversation, whatever shape its key
// has: a mirrored thread's key is Slack's, not 'chat:'||id.
func TestTheChatListReportsARunningTurnForAMirroredThread(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	thread, err := s.CreateMirrorChat(ctx, slackKey, OriginSlack, "alice", "in slack")
	require.NoError(t, err)
	sess, err := s.UpsertSession(ctx, Session{
		SourceKind: "slack", SourceKey: slackKey, Profile: "podium",
	})
	require.NoError(t, err)
	turn, err := s.CreateTurn(ctx, sess.ID, "C1/100.1/100.1", Backend{})
	require.NoError(t, err)

	chats, _, err := s.ListChats(ctx, "alice", 0, "")
	require.NoError(t, err)
	for _, c := range chats {
		if c.ID == thread.ID {
			assert.True(t, c.TurnRunning)
		}
	}

	require.NoError(t, s.FinishTurn(ctx, turn.ID, TurnSucceeded, nil, nil, "done"))
	chats, _, err = s.ListChats(ctx, "alice", 0, "")
	require.NoError(t, err)
	for _, c := range chats {
		if c.ID == thread.ID {
			assert.False(t, c.TurnRunning)
		}
	}
}

// The assistant's turn ends the moment it has delegated; the work it delegated does not. The
// list says so with a second flag, so the chat stays marked busy for as long as the task runs
// — and that flag is not the composer's: the conversation may go on meanwhile.
func TestTheChatListReportsARunningDelegatedTask(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	chat, err := s.CreateChat(ctx, "alice", "August numbers")
	require.NoError(t, err)
	turn := runningTurn(t, s, chat.ID)
	dlg, err := s.CreateDelegation(ctx, NewDelegation{
		SessionID: turn.SessionID, TurnID: turn.ID, TriggerRef: chat.ID,
		Playbook: "analyst", Instruction: "count the accounts",
	})
	require.NoError(t, err)
	require.NoError(t, s.FinishTurn(ctx, turn.ID, TurnSucceeded, nil, nil, "started a task"))

	listed := func() Chat {
		chats, _, err := s.ListChats(ctx, "alice", 0, "")
		require.NoError(t, err)
		for _, c := range chats {
			if c.ID == chat.ID {
				return c
			}
		}
		t.Fatalf("chat %s not listed", chat.ID)
		return Chat{}
	}
	c := listed()
	assert.False(t, c.TurnRunning, "the turn is over")
	assert.True(t, c.TaskRunning, "the task it delegated is not")
	running, err := s.ChatTurnRunning(ctx, chat.ID)
	require.NoError(t, err)
	assert.False(t, running, "a delegated task does not hold the composer")

	require.NoError(t, s.FinishDelegation(ctx, dlg.ID, TurnSucceeded, "4,812.", nil, nil))
	assert.False(t, listed().TaskRunning)
}
