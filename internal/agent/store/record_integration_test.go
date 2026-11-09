//go:build integration

package store

import (
	"context"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// twoStores is two conductors' handles on ONE database: two pools, as two processes would
// have. Everything the phase-2 record guarantees has to hold across them, not just inside one.
func twoStores(t *testing.T) (*Store, *Store) {
	t.Helper()
	url := newDatabase(t)
	a, err := New(context.Background(), url)
	require.NoError(t, err)
	t.Cleanup(a.Close)
	require.NoError(t, a.Migrate(context.Background()))
	b, err := New(context.Background(), url)
	require.NoError(t, err)
	t.Cleanup(b.Close)
	return a, b
}

// Concurrent writers to one chat — on two conductors — get distinct, gapless seqs. The old
// max(seq)+1 let two of them compute the same seq, and one failed on the primary key.
func TestConcurrentWritersAcrossConductorsGetDistinctSeqs(t *testing.T) {
	a, b := twoStores(t)
	ctx := context.Background()
	chat, err := a.CreateChat(ctx, "alice@acme.com", "t")
	require.NoError(t, err)

	const each = 25
	var wg sync.WaitGroup
	var mu sync.Mutex
	var seqs []uint64
	for _, s := range []*Store{a, b} {
		for i := 0; i < each; i++ {
			wg.Add(1)
			go func(s *Store) {
				defer wg.Done()
				m, err := s.AppendChatMessage(ctx, ChatMessage{ChatID: chat.ID, Role: RoleUser, Text: "x"})
				if !assert.NoError(t, err) {
					return
				}
				mu.Lock()
				seqs = append(seqs, m.Seq)
				mu.Unlock()
			}(s)
		}
	}
	wg.Wait()

	require.Len(t, seqs, 2*each)
	sort.Slice(seqs, func(i, j int) bool { return seqs[i] < seqs[j] })
	for i, seq := range seqs {
		assert.Equal(t, uint64(i+1), seq, "seqs are 1..n with no collision and no gap")
	}
}

// The same message recorded by two conductors at once is one row, and exactly one of them is
// told it wrote it — that one, and only that one, starts the turn.
func TestRecordingOneMessageTwiceWritesItOnce(t *testing.T) {
	a, b := twoStores(t)
	ctx := context.Background()
	chat, err := a.CreateMirrorChat(ctx, "slack:C1:1.1", OriginSlack, "alice", "t")
	require.NoError(t, err)

	var fresh atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		s := a
		if i%2 == 1 {
			s = b
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, ok, err := s.RecordChatMessage(ctx, ChatMessage{
				ChatID: chat.ID, Role: RoleUser, Author: "alice", Text: "hi",
				Via: ViaSlack, ExternalID: "1.2", Addressed: true,
			})
			if assert.NoError(t, err) && ok {
				fresh.Add(1)
			}
		}()
	}
	wg.Wait()

	assert.Equal(t, int32(1), fresh.Load(), "exactly one arrival recorded it")
	msgs, err := a.ListChatMessages(ctx, chat.ID, 0)
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	assert.Equal(t, "1.2", msgs[0].ExternalID)
	assert.Equal(t, ViaSlack, msgs[0].Via)
	assert.True(t, msgs[0].Addressed)

	// The same ts on another surface is another message.
	_, ok, err := a.RecordChatMessage(ctx, ChatMessage{
		ChatID: chat.ID, Role: RoleUser, Text: "hi", Via: ViaGitHub, ExternalID: "1.2",
	})
	require.NoError(t, err)
	assert.True(t, ok)
}

// A recorder says whether a message was addressed; an append always was.
func TestRecordedMessagesCarryTheirAttribution(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	chat, err := s.CreateMirrorChat(ctx, "github:acme/repo#1", OriginGitHub, "octocat", "t")
	require.NoError(t, err)

	got, ok, err := s.RecordChatMessage(ctx, ChatMessage{
		ChatID: chat.ID, Role: RoleUser, Author: "octocat", Text: "lgtm",
		AuthorHandle: "octocat", AuthorAvatar: "https://a/o.png", Via: ViaGitHub,
		ExternalID: "issue_comment:7", ExternalURL: "https://github.com/acme/repo/pull/1#issuecomment-7",
	})
	require.NoError(t, err)
	require.True(t, ok)
	assert.False(t, got.Addressed, "chatter nobody put to Podium")
	assert.Empty(t, got.AuthorLogin, "octocat is not in Podium")
	assert.Equal(t, "octocat", got.AuthorHandle)
	assert.Equal(t, "https://a/o.png", got.AuthorAvatar)
	assert.Equal(t, "https://github.com/acme/repo/pull/1#issuecomment-7", got.ExternalURL)

	web, err := s.AppendChatMessage(ctx, ChatMessage{ChatID: chat.ID, Role: RoleUser, Text: "x", AuthorLogin: "a@acme.com"})
	require.NoError(t, err)
	assert.True(t, web.Addressed)
	assert.Equal(t, ViaWeb, web.Via, "a user message with no surface is a web message")
	assert.Equal(t, "a@acme.com", web.AuthorLogin)

	bot, err := s.AppendChatMessage(ctx, ChatMessage{ChatID: chat.ID, Role: RoleAssistant, Text: "y"})
	require.NoError(t, err)
	assert.Equal(t, ViaBot, bot.Via)
}

// An edit replaces the text in place and stamps it; a delete keeps the text (decision 12).
// A change to a message never recorded is ErrNotFound.
func TestEditsAndDeletesChangeTheRecordedRow(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	chat, err := s.CreateMirrorChat(ctx, "slack:C1:9.9", OriginSlack, "alice", "t")
	require.NoError(t, err)
	first, ok, err := s.RecordChatMessage(ctx, ChatMessage{
		ChatID: chat.ID, Role: RoleUser, Text: "teh plan", Via: ViaSlack, ExternalID: "9.9",
	})
	require.NoError(t, err)
	require.True(t, ok)
	_, err = s.AppendChatMessage(ctx, ChatMessage{ChatID: chat.ID, Role: RoleAssistant, Text: "ok"})
	require.NoError(t, err)

	at := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	edited, err := s.EditChatMessage(ctx, chat.ID, ViaSlack, "9.9", "the plan", at)
	require.NoError(t, err)
	assert.Equal(t, first.Seq, edited.Seq, "an edit stays where it was said")
	assert.Equal(t, "the plan", edited.Text)
	require.NotNil(t, edited.EditedAt)
	assert.True(t, at.Equal(*edited.EditedAt))

	deleted, err := s.DeleteChatMessage(ctx, chat.ID, ViaSlack, "9.9", at.Add(time.Minute))
	require.NoError(t, err)
	require.NotNil(t, deleted.DeletedAt)
	assert.Equal(t, "the plan", deleted.Text, "the store keeps a deleted message's text")

	_, err = s.EditChatMessage(ctx, chat.ID, ViaSlack, "nope", "x", at)
	assert.ErrorIs(t, err, ErrNotFound)
	_, err = s.DeleteChatMessage(ctx, chat.ID, ViaGitHub, "9.9", at)
	assert.ErrorIs(t, err, ErrNotFound, "the id is the surface's own: a Slack ts is not a GitHub id")

	again, err := s.GetChatMessage(ctx, chat.ID, first.Seq)
	require.NoError(t, err)
	assert.Equal(t, deleted, again)
}

// A notification sent on one conductor's store is heard by another's LISTEN.
func TestANotificationReachesAnotherConductor(t *testing.T) {
	a, b := twoStores(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ready := make(chan struct{}, 1)
	heard := make(chan string, 4)
	go func() {
		_ = b.Listen(ctx, ChatChannel, func() { ready <- struct{}{} }, func(p string) { heard <- p }, nil)
	}()
	select {
	case <-ready:
	case <-time.After(10 * time.Second):
		t.Fatal("the listener never connected")
	}

	require.NoError(t, a.Notify(ctx, ChatChannel, `{"c":"chat_1"}`))
	select {
	case p := <-heard:
		assert.Equal(t, `{"c":"chat_1"}`, p)
	case <-time.After(10 * time.Second):
		t.Fatal("the notification never arrived")
	}

	big := make([]byte, MaxNotifyPayload+1)
	assert.Error(t, a.Notify(ctx, ChatChannel, string(big)), "an oversized payload is refused before Postgres")
}
