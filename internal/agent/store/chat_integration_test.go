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
	turn, err := s.CreateTurn(ctx, sess.ID, chatID)
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
