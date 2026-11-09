//go:build integration

package conductor_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/agent/chat"
	"github.com/podium-ade/podium/internal/agent/conductor"
	"github.com/podium-ade/podium/internal/agent/conductor/fakesource"
	"github.com/podium-ade/podium/internal/agent/store"
)

const startChatGuest = "dana@acme.com"

// startChatFixture is a conductor with the web chat behind it, a conversation of testSpeaker's
// to open chats from, and a token for a turn of it.
type startChatFixture struct {
	st     *store.Store
	fake   *fakePodium
	cond   *conductor.Conductor
	origin store.Chat
	sessID string
	token  string
}

func newStartChatFixture(t *testing.T) startChatFixture {
	t.Helper()
	ctx := context.Background()
	st := newStore(t)
	fake := newFakePodium(t)
	web, err := chat.New(chat.Options{Store: st, DisplayName: "Podium", UIURL: "https://podium.example/"})
	require.NoError(t, err)
	src := fakesource.New(conductor.KindDev)
	t.Cleanup(src.Close)
	r := startWith(t, st, fake, src, func(o *conductor.Options) { o.Mirror = web })
	require.NoError(t, st.UpsertIdentity(ctx, store.Identity{
		Login: startChatGuest, Provider: store.ProviderWeb, ExternalID: startChatGuest, DisplayName: "Dana",
	}))
	origin, err := st.CreateChat(ctx, testSpeaker, "The login outage")
	require.NoError(t, err)
	sess, err := st.UpsertSession(ctx, store.Session{
		SourceKind: conductor.SourceChat, SourceKey: origin.SourceKey, Profile: "podium", Playbook: "podium",
	})
	require.NoError(t, err)
	token, err := r.cond.MintTurnTokenForTest(sess.ID, "turn_start_1", testSpeaker)
	require.NoError(t, err)
	return startChatFixture{st: st, fake: fake, cond: r.cond, origin: origin, sessID: sess.ID, token: token}
}

// TestATurnOpensASharedChatAndHandsBackTheLink is the whole of "starting a triage on this
// session — click here to join": the chat is the asker's, starts with the assistant's
// opening and a link home, is shared with the person named (who is notified), takes link
// access, and is audited as the asker's doing.
func TestATurnOpensASharedChatAndHandsBackTheLink(t *testing.T) {
	ctx := context.Background()
	f := newStartChatFixture(t)
	// The origin is link-shared: its token must not leak into the new chat's backlink.
	_, err := f.st.SetChatGeneralAccess(ctx, f.origin.ID, store.AccessLink, store.RoleViewer)
	require.NoError(t, err)
	originWithToken, err := f.st.RotateChatLinkToken(ctx, f.origin.ID)
	require.NoError(t, err)

	started, err := f.cond.StartChat(ctx, f.token, conductor.StartChatRequest{
		Title:     "Triage:   the login\noutage",
		Opening:   "Starting a triage of the login outage. Known so far: 502s since 09:00.",
		ShareWith: []string{"Dana@ACME.com", " ", startChatGuest, testSpeaker},
		Access:    "link",
	})
	require.NoError(t, err)

	c := started.Chat
	assert.Equal(t, testSpeaker, c.Login, "the chat is the asker's")
	assert.Equal(t, "Triage: the login outage", c.Title)
	assert.Equal(t, f.origin.ID, c.StartedFromChat)
	assert.True(t, c.Multiplayer, "a shared chat is multiplayer from the start")
	assert.Equal(t, store.AccessLink, c.GeneralAccess)
	require.NotEmpty(t, c.LinkToken)
	assert.Equal(t, "https://podium.example/agent/chat/"+c.ID+"?k="+c.LinkToken, started.URL)
	assert.Equal(t, []string{startChatGuest}, started.SharedWith, "named once, in Podium's spelling, never the owner")

	role, err := f.st.ChatRole(ctx, c.ID, startChatGuest)
	require.NoError(t, err)
	assert.Equal(t, store.RoleEditor, role)
	notes, err := f.st.ListNotifications(ctx, startChatGuest, true, 10)
	require.NoError(t, err)
	require.Len(t, notes, 1)
	assert.Equal(t, c.ID, notes[0].ChatID)

	msgs, err := f.st.ListChatMessages(ctx, c.ID, 0)
	require.NoError(t, err)
	require.NotEmpty(t, msgs)
	first := msgs[0]
	assert.Equal(t, store.RoleAssistant, first.Role)
	assert.True(t, strings.HasPrefix(first.Text, "Starting a triage of the login outage."), first.Text)
	assert.Contains(t, first.Text, "https://podium.example/agent/chat/"+f.origin.ID+")")
	assert.NotContains(t, first.Text, originWithToken.LinkToken, "the backlink never carries the origin's link token")
	assert.NotContains(t, first.Text, "The login outage", "the origin's title stays out of a chat others may read")

	assert.Equal(t, []string{"chat.start", "chat.share", "chat.general_access"}, f.fake.audit.Actions(c.ID))

	visible, err := f.st.ChatVisibleTo(ctx, c.ID, startChatGuest)
	require.NoError(t, err)
	assert.True(t, visible, "the guest has it in their own list")
}

// TestStartChatRefusesBeforeWritingAnything: a refused call leaves no chat behind.
func TestStartChatRefusesBeforeWritingAnything(t *testing.T) {
	ctx := context.Background()
	f := newStartChatFixture(t)
	count := func() int {
		n, err := f.st.CountChatsStartedByTurn(ctx, "turn_start_1")
		require.NoError(t, err)
		return n
	}

	_, err := f.cond.StartChat(ctx, f.token, conductor.StartChatRequest{
		Title: "Triage", Opening: "x", ShareWith: []string{startChatGuest, "nobody@acme.com"},
	})
	require.ErrorIs(t, err, conductor.ErrInvalidStartChat)
	assert.Contains(t, err.Error(), "nobody@acme.com has not signed in to Podium")
	assert.Equal(t, 0, count())

	for _, req := range []conductor.StartChatRequest{
		{Opening: "x"},
		{Title: "t"},
		{Title: "t", Opening: "x", Access: "public"},
		{Title: "t", Opening: "x", Role: "owner"},
		{Title: "t", Opening: strings.Repeat("x", 17*1024)},
	} {
		_, err := f.cond.StartChat(ctx, f.token, req)
		require.ErrorIs(t, err, conductor.ErrInvalidStartChat, "%+v", req)
	}
	assert.Equal(t, 0, count())

	_, err = f.cond.StartChat(ctx, "not-a-token", conductor.StartChatRequest{Title: "t", Opening: "x"})
	require.ErrorIs(t, err, conductor.ErrNoTurn)

	// A turn nobody in Podium started — an automatic review — has nobody to own a chat.
	anon, err := f.cond.MintTurnTokenForTest(f.sessID, "turn_start_anon", "")
	require.NoError(t, err)
	_, err = f.cond.StartChat(ctx, anon, conductor.StartChatRequest{Title: "t", Opening: "x"})
	require.ErrorIs(t, err, conductor.ErrNoChatOwner)
}

// TestATurnOpensAtMostAFewChats: the bound is counted in the database, per turn.
func TestATurnOpensAtMostAFewChats(t *testing.T) {
	ctx := context.Background()
	f := newStartChatFixture(t)
	for i := range conductor.MaxChatsStartedPerTurn {
		started, err := f.cond.StartChat(ctx, f.token, conductor.StartChatRequest{Title: "Side thread", Opening: "x"})
		require.NoError(t, err, "chat %d", i)
		assert.False(t, started.Chat.Multiplayer, "a chat shared with nobody is the owner's alone")
		assert.Equal(t, store.AccessRestricted, started.Chat.GeneralAccess)
		assert.Equal(t, "https://podium.example/agent/chat/"+started.Chat.ID, started.URL)
	}
	_, err := f.cond.StartChat(ctx, f.token, conductor.StartChatRequest{Title: "One more", Opening: "x"})
	require.ErrorIs(t, err, conductor.ErrTooManyChats)

	// Another turn of the same conversation has its own allowance.
	next, err := f.cond.MintTurnTokenForTest(f.sessID, "turn_start_2", testSpeaker)
	require.NoError(t, err)
	_, err = f.cond.StartChat(ctx, next, conductor.StartChatRequest{Title: "Next turn", Opening: "x"})
	require.NoError(t, err)
}
