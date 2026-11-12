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

const (
	alice = "alice@acme.com"
	bob   = "bob@acme.com"
	carol = "carol@acme.com"
)

// visibilityFixture is one of each kind of conversation, and the sessions that hold them:
//
//	aliceWeb   alice's own web chat
//	slackBob   a Slack thread bob spoke in (resolved), and a stranger too
//	strangers  a Slack thread only people outside Podium spoke in
//	prCarol    a pull request carol spoke in
type visibilityFixture struct {
	aliceWeb, slackBob, strangers, prCarol Chat
}

func newVisibilityFixture(t *testing.T, s *Store) visibilityFixture {
	t.Helper()
	ctx := context.Background()
	var f visibilityFixture
	var err error
	f.aliceWeb, err = s.CreateChat(ctx, alice, "mine")
	require.NoError(t, err)
	f.slackBob, err = s.CreateMirrorChat(ctx, "slack:C1:1.1", OriginSlack, "Bob", "deploy")
	require.NoError(t, err)
	f.strangers, err = s.CreateMirrorChat(ctx, "slack:C1:2.2", OriginSlack, "Mallory", "hello")
	require.NoError(t, err)
	f.prCarol, err = s.CreateMirrorChat(ctx, "github:acme/repo#1", OriginGitHub, "carol", "review")
	require.NoError(t, err)

	for _, key := range []string{ChatSourceKey(f.aliceWeb.ID), "slack:C1:1.1", "slack:C1:2.2", "github:acme/repo#1"} {
		kind := "slack"
		switch {
		case key == ChatSourceKey(f.aliceWeb.ID):
			kind = "chat"
		case key == "github:acme/repo#1":
			kind = "github"
		}
		_, err = s.UpsertSession(ctx, Session{SourceKind: kind, SourceKey: key, Profile: "podium"})
		require.NoError(t, err)
	}
	_, err = s.AddChatParticipant(ctx, f.slackBob.ID, bob, JoinedSlack, time.Now())
	require.NoError(t, err)
	_, err = s.AddChatParticipant(ctx, f.prCarol.ID, carol, JoinedGitHub, time.Now())
	require.NoError(t, err)
	return f
}

func chatIDs(cs []Chat) []string {
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.ID)
	}
	return out
}

func sessionKeys(ss []Session) []string {
	out := make([]string, 0, len(ss))
	for _, s := range ss {
		out = append(out, s.SourceKey)
	}
	return out
}

// The permission matrix for phase 1: every viewer against every conversation, through every
// read — the chat list, opening one chat, the sessions list. The three must agree, because the
// sessions screen and the chat list are two views of the same partition.
func TestVisibilityMatrix(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	f := newVisibilityFixture(t, s)

	type want struct{ aliceWeb, slackBob, strangers, prCarol bool }
	for _, tc := range []struct {
		viewer string
		all    bool
		want   want
	}{
		{viewer: alice, want: want{aliceWeb: true}},
		{viewer: bob, want: want{slackBob: true}},
		{viewer: carol, want: want{prCarol: true}},
		{viewer: "dave@acme.com", want: want{}},
		// The dev token sees everything, a thread nobody in Podium spoke in included.
		{viewer: "local", all: true, want: want{true, true, true, true}},
	} {
		t.Run(tc.viewer, func(t *testing.T) {
			keyOf := map[string]string{
				f.aliceWeb.ID: ChatSourceKey(f.aliceWeb.ID), f.slackBob.ID: "slack:C1:1.1",
				f.strangers.ID: "slack:C1:2.2", f.prCarol.ID: "github:acme/repo#1",
			}
			expected := map[string]bool{
				f.aliceWeb.ID: tc.want.aliceWeb, f.slackBob.ID: tc.want.slackBob,
				f.strangers.ID: tc.want.strangers, f.prCarol.ID: tc.want.prCarol,
			}
			chats, _, err := s.ListChats(ctx, tc.viewer, tc.all, 100, "")
			require.NoError(t, err)
			listed := map[string]bool{}
			for _, id := range chatIDs(chats) {
				listed[id] = true
			}
			sessions, _, err := s.ListSessions(ctx, tc.viewer, tc.all, 100, "")
			require.NoError(t, err)
			keyed := map[string]bool{}
			for _, k := range sessionKeys(sessions) {
				keyed[k] = true
			}
			for id, visible := range expected {
				assert.Equal(t, visible, listed[id], "list chats: %s", id)
				if !tc.all {
					open, err := s.ChatVisibleTo(ctx, id, tc.viewer)
					require.NoError(t, err)
					assert.Equal(t, visible, open, "open chat: %s", id)
				}
				assert.Equal(t, visible, keyed[keyOf[id]], "list sessions: %s", id)
			}
		})
	}

	_, err := s.ChatVisibleTo(ctx, "chat_missing", alice)
	require.ErrorIs(t, err, ErrNotFound)
}

// Two conductors resolving two different speakers on the same new thread at the same moment
// end with both as participants and exactly one owner — whichever claim landed first.
func TestParticipantsAndOwnerUnderRace(t *testing.T) {
	ctx := context.Background()
	url := newDatabase(t)
	a, err := New(ctx, url)
	require.NoError(t, err)
	t.Cleanup(a.Close)
	require.NoError(t, a.Migrate(ctx))
	b, err := New(ctx, url)
	require.NoError(t, err)
	t.Cleanup(b.Close)

	chat, err := a.CreateMirrorChat(ctx, "slack:C9:9.9", OriginSlack, "Alice", "race")
	require.NoError(t, err)

	var wg sync.WaitGroup
	wins := make(chan string, 2)
	for _, side := range []struct {
		st    *Store
		login string
	}{{a, alice}, {b, bob}} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := side.st.AddChatParticipant(ctx, chat.ID, side.login, JoinedSlack, time.Now())
			assert.NoError(t, err)
			won, err := side.st.ClaimChatOwner(ctx, chat.ID, side.login)
			assert.NoError(t, err)
			if won {
				wins <- side.login
			}
		}()
	}
	wg.Wait()
	close(wins)
	var winners []string
	for w := range wins {
		winners = append(winners, w)
	}
	require.Len(t, winners, 1, "exactly one claim wins")

	row, err := a.GetChat(ctx, chat.ID)
	require.NoError(t, err)
	assert.Equal(t, winners[0], row.Login)
	for _, login := range []string{alice, bob} {
		ok, err := b.ChatVisibleTo(ctx, chat.ID, login)
		require.NoError(t, err)
		assert.True(t, ok, login)
	}
	// A later claim never moves the owner.
	won, err := a.ClaimChatOwner(ctx, chat.ID, carol)
	require.NoError(t, err)
	assert.False(t, won)
}

func TestIdentitiesResolveAndMove(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	ok, err := s.HasWebIdentity(ctx, alice)
	require.NoError(t, err)
	assert.False(t, ok)
	require.NoError(t, s.UpsertIdentity(ctx, Identity{Login: alice, Provider: ProviderWeb, ExternalID: alice}))
	ok, err = s.HasWebIdentity(ctx, alice)
	require.NoError(t, err)
	assert.True(t, ok)

	require.NoError(t, s.UpsertIdentity(ctx, Identity{Login: alice, Provider: ProviderGitHub, ExternalID: "42", Handle: "ali"}))
	login, err := s.IdentityLogin(ctx, ProviderGitHub, "42")
	require.NoError(t, err)
	assert.Equal(t, alice, login)

	// The newest proof of an account wins.
	require.NoError(t, s.UpsertIdentity(ctx, Identity{Login: bob, Provider: ProviderGitHub, ExternalID: "42"}))
	login, err = s.IdentityLogin(ctx, ProviderGitHub, "42")
	require.NoError(t, err)
	assert.Equal(t, bob, login)

	require.NoError(t, s.DeleteIdentitiesOf(ctx, ProviderGitHub, bob))
	_, err = s.IdentityLogin(ctx, ProviderGitHub, "42")
	require.ErrorIs(t, err, ErrNotFound)

	require.Error(t, s.UpsertIdentity(ctx, Identity{Login: alice, Provider: "myspace", ExternalID: "x"}))
}

// A Linear account is a provider the table accepts (0045), keyed by workspace and user.
func TestALinearAccountCanBeProvenToBeALogin(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	id := LinearExternalID("org-1", "u-1")
	require.NoError(t, s.UpsertIdentity(ctx, Identity{Login: alice, Provider: ProviderLinear, ExternalID: id, DisplayName: "Alice"}))
	login, err := s.IdentityLogin(ctx, ProviderLinear, id)
	require.NoError(t, err)
	assert.Equal(t, alice, login)

	// The check constraint, not only the Go guard, refuses a provider nobody named.
	_, err = s.pool.Exec(ctx, `insert into identities (login, provider, external_id, verified_at)
		values ($1, 'myspace', 'x', now())`, alice)
	require.Error(t, err)
}

// Participants are the Podium people first, then the names in the thread that are not one of
// them. A Slack display name a participant is known by is merged into them, not listed twice.
func TestChatParticipantsMergesAuthorsIntoPeople(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	chat, err := s.CreateMirrorChat(ctx, "slack:C1:3.3", OriginSlack, "Bob", "t")
	require.NoError(t, err)
	for _, who := range []string{"Bob", "Mallory", "Bob"} {
		_, err = s.AppendChatMessage(ctx, ChatMessage{ChatID: chat.ID, Role: RoleUser, Author: who, Text: "hi"})
		require.NoError(t, err)
	}
	require.NoError(t, s.UpsertIdentity(ctx, Identity{Login: bob, Provider: ProviderSlack, ExternalID: "T1/U2", DisplayName: "Bob"}))
	_, err = s.AddChatParticipant(ctx, chat.ID, bob, JoinedSlack, time.Now())
	require.NoError(t, err)

	got, err := s.ChatParticipants(ctx, chat.ID)
	require.NoError(t, err)
	assert.Equal(t, []ChatParticipant{
		{Login: bob, Name: "Bob", JoinedVia: JoinedSlack, Via: ViaSlack},
		{Name: "Mallory", Via: ViaWeb},
	}, got)
}

// A backfilled guess becomes a real participant the moment the person is actually seen.
func TestAddChatParticipantUpgradesABackfilledRow(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	chat, err := s.CreateMirrorChat(ctx, "github:acme/repo#2", OriginGitHub, "carol", "t")
	require.NoError(t, err)
	first, err := s.AddChatParticipant(ctx, chat.ID, carol, JoinedBackfill, time.Now())
	require.NoError(t, err)
	assert.True(t, first)
	again, err := s.AddChatParticipant(ctx, chat.ID, carol, JoinedGitHub, time.Now())
	require.NoError(t, err)
	assert.False(t, again)
	got, err := s.ChatParticipants(ctx, chat.ID)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, JoinedGitHub, got[0].JoinedVia)
}

// The backfill in 0031 runs again over data written before it: it only ever writes from
// proof (an OAuth-connected GitHub account, a web chat's owner), and it is idempotent.
func TestIdentitiesBackfill(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	web, err := s.CreateChat(ctx, alice, "mine")
	require.NoError(t, err)
	pr, err := s.CreateMirrorChat(ctx, "github:acme/repo#3", OriginGitHub, "someone", "t")
	require.NoError(t, err)
	_, err = s.AppendChatMessage(ctx, ChatMessage{ChatID: pr.ID, Role: RoleUser, Author: "Bobby", Text: "hi"})
	require.NoError(t, err)
	slackChat, err := s.CreateMirrorChat(ctx, "slack:C1:4.4", OriginSlack, "bobby", "t")
	require.NoError(t, err)
	require.NoError(t, s.PutGitHubAccount(ctx, GitHubAccount{
		Login: bob, GitHubID: 7, GitHubLogin: "bobby", ConnectedAt: time.Now(),
	}))
	// Undo what the live code paths wrote, so only the migration is being tested.
	_, err = s.pool.Exec(ctx, "delete from identities; delete from chat_participants")
	require.NoError(t, err)

	sql, err := migrationsFS.ReadFile("migrations/0031_identities.sql")
	require.NoError(t, err)
	for range 2 {
		_, err = s.pool.Exec(ctx, string(sql))
		require.NoError(t, err)
	}

	login, err := s.IdentityLogin(ctx, ProviderGitHub, "7")
	require.NoError(t, err)
	assert.Equal(t, bob, login)
	ok, err := s.HasWebIdentity(ctx, alice)
	require.NoError(t, err)
	assert.True(t, ok)

	for _, tc := range []struct {
		chat, viewer string
		want         bool
	}{
		{web.ID, alice, true},
		{pr.ID, bob, true},         // "Bobby" is bob's proven GitHub handle
		{slackChat.ID, bob, false}, // a Slack name is not proof
	} {
		got, err := s.ChatVisibleTo(ctx, tc.chat, tc.viewer)
		require.NoError(t, err)
		assert.Equal(t, tc.want, got, "%s for %s", tc.chat, tc.viewer)
	}
	ps, err := s.ChatParticipants(ctx, pr.ID)
	require.NoError(t, err)
	require.NotEmpty(t, ps)
	assert.Equal(t, JoinedBackfill, ps[0].JoinedVia)
}
