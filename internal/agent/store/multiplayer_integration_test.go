//go:build integration

package store

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A chat is multiplayer once a second Podium person has taken part, or it is shared (force),
// or it mirrors a conversation that lives elsewhere — and exactly one caller is told it
// changed, however many race to make it so.
func TestMultiplayerIsStickyAndAnnouncedOnce(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	mirrored, err := s.CreateMirrorChat(ctx, "slack:C1:1.1", OriginSlack, "Alice", "t")
	require.NoError(t, err)
	assert.True(t, mirrored.Multiplayer, "a mirrored conversation always has other people in it")

	web, err := s.CreateChat(ctx, "alice", "t")
	require.NoError(t, err)
	assert.False(t, web.Multiplayer)
	_, err = s.AddChatParticipant(ctx, web.ID, "alice", JoinedWeb, time.Now())
	require.NoError(t, err)
	_, changed, err := s.MarkChatMultiplayer(ctx, web.ID, false)
	require.NoError(t, err)
	assert.False(t, changed, "one person is not multiplayer")

	_, err = s.AddChatParticipant(ctx, web.ID, "bob", JoinedWeb, time.Now())
	require.NoError(t, err)
	var wg sync.WaitGroup
	var mu sync.Mutex
	flips := 0
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, changed, err := s.MarkChatMultiplayer(ctx, web.ID, false)
			assert.NoError(t, err)
			if changed {
				mu.Lock()
				flips++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	assert.Equal(t, 1, flips, "announced once")
	got, err := s.GetChat(ctx, web.ID)
	require.NoError(t, err)
	assert.True(t, got.Multiplayer)

	shared, err := s.CreateChat(ctx, "alice", "s")
	require.NoError(t, err)
	_, changed, err = s.MarkChatMultiplayer(ctx, shared.ID, true)
	require.NoError(t, err)
	assert.True(t, changed, "a share makes it multiplayer with one person")
}

// A chat with somebody else's personal assistant is listed under Podium for a participant
// (who has no picker entry for it), and never under the assistant's id for anybody but its
// owner.
func TestASharedPersonalityChatIsListedUnderPodium(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	p, err := s.CreatePersonality(ctx, "alice", PersonalityDraft{DisplayName: "Researcher", Instructions: "dig"})
	require.NoError(t, err)
	c, err := s.CreatePersonalityChat(ctx, "alice", "t", p.ID, p.DisplayName)
	require.NoError(t, err)
	_, err = s.AddChatParticipant(ctx, c.ID, "bob", JoinedWeb, time.Now())
	require.NoError(t, err)

	ids := func(login, personality string) []string {
		rows, _, err := s.ListChats(ctx, login, false, 50, "", ListChatsScope{FilterPersonality: true, PersonalityID: personality})
		require.NoError(t, err)
		var out []string
		for _, r := range rows {
			out = append(out, r.ID)
		}
		return out
	}
	assert.Equal(t, []string{c.ID}, ids("alice", p.ID), "its owner finds it under the assistant")
	assert.Empty(t, ids("alice", ""), "and not under Podium")
	assert.Equal(t, []string{c.ID}, ids("bob", ""), "a participant finds it under Podium")
	assert.Empty(t, ids("bob", p.ID), "the assistant is not the participant's to filter by")
	assert.Empty(t, ids("carol", ""), "and nobody else finds it at all")
}

// The turn queue: in order, one person's run taken together, and orphans found.
func TestTheTurnQueue(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	sess, err := s.UpsertSession(ctx, Session{SourceKind: "slack", SourceKey: "slack:C1:2.2", Profile: "podium"})
	require.NoError(t, err)
	for _, who := range []string{"alice", "alice", "bob", "alice"} {
		_, err := s.EnqueueTurn(ctx, QueuedTurn{SessionID: sess.ID, SourceKind: "slack", Login: who, Event: []byte(`{"Text":"` + who + `"}`)})
		require.NoError(t, err)
	}
	orphans, err := s.OrphanedQueues(ctx, "me")
	require.NoError(t, err)
	assert.Equal(t, []string{sess.ID}, orphans, "nobody holds the session")
	ok, err := s.AcquireLease(ctx, SessionLease(sess.ID), "other", time.Minute)
	require.NoError(t, err)
	require.True(t, ok)
	orphans, err = s.OrphanedQueues(ctx, "me")
	require.NoError(t, err)
	assert.Empty(t, orphans, "another live conductor serves it")
	orphans, err = s.OrphanedQueues(ctx, "other")
	require.NoError(t, err)
	assert.Equal(t, []string{sess.ID}, orphans, "its own server finds it after a restart")

	head, ok, err := s.PopQueuedTurn(ctx, sess.ID)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "alice", head.Login)
	_, ok, err = s.PopQueuedTurnFrom(ctx, sess.ID, "alice", "slack")
	require.NoError(t, err)
	assert.True(t, ok, "alice's second message joins her turn")
	_, ok, err = s.PopQueuedTurnFrom(ctx, sess.ID, "alice", "slack")
	require.NoError(t, err)
	assert.False(t, ok, "bob is next, so alice's run ends")
	n, err := s.CountQueuedTurns(ctx, sess.ID)
	require.NoError(t, err)
	assert.Equal(t, 2, n)
	next, ok, err := s.PopQueuedTurn(ctx, sess.ID)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "bob", next.Login)
}

// TestBecomingMultiplayerNamesTheOwnersEarlierMessages: a single-player chat leaves author
// empty, so once it is shared the owner's earlier messages are named too, and the thread does
// not show the owner as the one person who said things without a name.
func TestBecomingMultiplayerNamesTheOwnersEarlierMessages(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	web, err := s.CreateChat(ctx, "alice@acme.test", "t")
	require.NoError(t, err)
	_, _, err = s.RecordChatMessage(ctx, ChatMessage{
		ChatID: web.ID, Role: RoleUser, Text: "before the share", TS: time.Now().UTC(),
		AuthorLogin: "alice@acme.test", Via: ViaWeb, Addressed: true,
	})
	require.NoError(t, err)
	_, err = s.AppendChatMessage(ctx, ChatMessage{ChatID: web.ID, Role: RoleAssistant, Text: "an answer"})
	require.NoError(t, err)

	_, changed, err := s.MarkChatMultiplayer(ctx, web.ID, true)
	require.NoError(t, err)
	require.True(t, changed)

	msgs, err := s.ListChatMessages(ctx, web.ID, 0)
	require.NoError(t, err)
	require.Len(t, msgs, 2)
	assert.Equal(t, "alice@acme.test", msgs[0].Author, "the owner's message is named")
	assert.Empty(t, msgs[1].Author, "the assistant's is not a person's")
}
