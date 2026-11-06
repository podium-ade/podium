//go:build integration

package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const dana = "dana@acme.com"

// member makes logins Podium people: a web identity, which is what signing in leaves.
func member(t *testing.T, s *Store, logins ...string) {
	t.Helper()
	for _, l := range logins {
		require.NoError(t, s.UpsertIdentity(context.Background(), Identity{
			Login: l, Provider: ProviderWeb, ExternalID: l, DisplayName: l,
		}))
	}
}

func roleOf(t *testing.T, s *Store, chatID, login string) string {
	t.Helper()
	r, err := s.ChatRole(context.Background(), chatID, login)
	require.NoError(t, err)
	return r
}

func listed(t *testing.T, s *Store, login, section string) []string {
	t.Helper()
	cs, _, err := s.ListChats(context.Background(), login, false, 100, "", ListChatsScope{Section: section})
	require.NoError(t, err)
	return chatIDs(cs)
}

// The phase 5 role matrix, read straight from chat_role: every way of having access, and
// what each one makes you. The strongest grant wins.
func TestChatRoleMatrix(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	ctx := context.Background()
	member(t, s, alice, bob, carol, dana)

	c, err := s.CreateChat(ctx, alice, "plan")
	require.NoError(t, err)
	assert.Equal(t, RoleOwner, roleOf(t, s, c.ID, alice))
	assert.Equal(t, "", roleOf(t, s, c.ID, bob), "restricted: nobody else")

	_, err = s.ShareChat(ctx, c.ID, bob, RoleViewer, alice)
	require.NoError(t, err)
	assert.Equal(t, RoleViewer, roleOf(t, s, c.ID, bob))

	// Speaking makes an editor of a viewer: the strongest grant wins.
	_, err = s.AddChatParticipant(ctx, c.ID, bob, JoinedWeb, time.Now())
	require.NoError(t, err)
	assert.Equal(t, RoleEditor, roleOf(t, s, c.ID, bob))

	// A participant the migration only guessed from a name is a viewer.
	_, err = s.AddChatParticipant(ctx, c.ID, carol, JoinedBackfill, time.Now())
	require.NoError(t, err)
	assert.Equal(t, RoleViewer, roleOf(t, s, c.ID, carol))

	// Members access reaches dana, with the general role, and nobody without a web identity.
	_, err = s.SetChatGeneralAccess(ctx, c.ID, AccessMembers, RoleEditor)
	require.NoError(t, err)
	assert.Equal(t, RoleEditor, roleOf(t, s, c.ID, dana))
	assert.Equal(t, "", roleOf(t, s, c.ID, "stranger@else.com"))

	// Link access grants nothing by itself: the token is the API's to check.
	_, err = s.SetChatGeneralAccess(ctx, c.ID, AccessLink, RoleEditor)
	require.NoError(t, err)
	assert.Equal(t, "", roleOf(t, s, c.ID, dana))

	_, err = s.ChatRole(ctx, "chat_missing", alice)
	assert.ErrorIs(t, err, ErrNotFound)
}

// Shared by name is in the list at once; shared with members is under Workspace until
// opened (JoinChat), then in the caller's own list and out of Workspace.
func TestSharedAndWorkspaceSections(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	ctx := context.Background()
	member(t, s, alice, bob, carol)

	named, err := s.CreateChat(ctx, alice, "named")
	require.NoError(t, err)
	everyone, err := s.CreateChat(ctx, alice, "everyone")
	require.NoError(t, err)
	_, err = s.ShareChat(ctx, named.ID, bob, RoleEditor, alice)
	require.NoError(t, err)
	_, err = s.SetChatGeneralAccess(ctx, everyone.ID, AccessMembers, RoleViewer)
	require.NoError(t, err)

	assert.ElementsMatch(t, []string{named.ID}, listed(t, s, bob, SectionMine))
	assert.ElementsMatch(t, []string{named.ID}, listed(t, s, bob, SectionShared))
	assert.ElementsMatch(t, []string{everyone.ID}, listed(t, s, bob, SectionWorkspace))
	assert.Empty(t, listed(t, s, alice, SectionShared), "an owner's own chats are not shared with them")
	assert.Empty(t, listed(t, s, alice, SectionWorkspace), "nor are they workspace chats for them")
	assert.ElementsMatch(t, []string{everyone.ID}, listed(t, s, carol, SectionWorkspace))
	assert.Empty(t, listed(t, s, "stranger@else.com", SectionWorkspace), "workspace is for members")

	joined, err := s.JoinChat(ctx, everyone.ID, bob, RoleViewer, GrantedByMembers)
	require.NoError(t, err)
	assert.True(t, joined)
	again, err := s.JoinChat(ctx, everyone.ID, bob, RoleViewer, GrantedByMembers)
	require.NoError(t, err)
	assert.False(t, again, "opening twice writes one row")
	assert.ElementsMatch(t, []string{named.ID, everyone.ID}, listed(t, s, bob, SectionMine))
	assert.Empty(t, listed(t, s, bob, SectionWorkspace))

	cs, _, err := s.ListChats(ctx, bob, false, 100, "", ListChatsScope{Section: SectionShared})
	require.NoError(t, err)
	byID := map[string]ChatSharing{}
	for _, c := range cs {
		byID[c.ID] = c.Sharing
	}
	assert.Equal(t, ChatSharing{MyRole: RoleEditor, AccessReason: AccessShared, SharedBy: alice}, byID[named.ID])
	assert.Equal(t, ChatSharing{MyRole: RoleViewer, AccessReason: AccessShared, SharedBy: GrantedByMembers}, byID[everyone.ID])

	// Sessions follow the same sections.
	for _, c := range []Chat{named, everyone} {
		_, err = s.UpsertSession(ctx, Session{SourceKind: "chat", SourceKey: ChatSourceKey(c.ID), Profile: "podium"})
		require.NoError(t, err)
	}
	ss, _, err := s.ListSessions(ctx, carol, false, 100, "", SectionWorkspace)
	require.NoError(t, err)
	require.Len(t, ss, 1)
	assert.Equal(t, everyone.ID, ss[0].Sharing.ChatID)
	assert.Equal(t, AccessWorkspace, ss[0].Sharing.AccessReason)
	assert.Equal(t, RoleViewer, ss[0].Sharing.MyRole)
	ss, _, err = s.ListSessions(ctx, bob, false, 100, "", SectionShared)
	require.NoError(t, err)
	assert.Len(t, ss, 2)

	_, _, err = s.ListSessions(ctx, bob, false, 100, "", "nonsense")
	assert.ErrorIs(t, err, ErrInvalidSection)
}

// A JoinChat never overwrites what the owner granted: a viewer link cannot demote an editor.
func TestJoinNeverDemotesAShare(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	ctx := context.Background()
	member(t, s, alice, bob)
	c, err := s.CreateChat(ctx, alice, "x")
	require.NoError(t, err)
	_, err = s.ShareChat(ctx, c.ID, bob, RoleEditor, alice)
	require.NoError(t, err)
	joined, err := s.JoinChat(ctx, c.ID, bob, RoleViewer, GrantedByLink)
	require.NoError(t, err)
	assert.False(t, joined)
	assert.Equal(t, RoleEditor, roleOf(t, s, c.ID, bob))

	// Sharing again changes the role, and is not a first share.
	first, err := s.ShareChat(ctx, c.ID, bob, RoleViewer, alice)
	require.NoError(t, err)
	assert.False(t, first)
	assert.Equal(t, RoleViewer, roleOf(t, s, c.ID, bob))
}

func TestShareRefusesSomebodyWhoNeverSignedIn(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	ctx := context.Background()
	member(t, s, alice)
	c, err := s.CreateChat(ctx, alice, "x")
	require.NoError(t, err)
	_, err = s.ShareChat(ctx, c.ID, "nobody@acme.com", RoleViewer, alice)
	assert.ErrorIs(t, err, ErrNoSuchMember)
	member(t, s, bob)
	_, err = s.ShareChat(ctx, c.ID, bob, "admin", alice)
	assert.ErrorIs(t, err, ErrInvalidAccess)
	_, err = s.SetChatGeneralAccess(ctx, c.ID, "public", RoleViewer)
	assert.ErrorIs(t, err, ErrInvalidAccess)
}

// Leaving removes the grant and the participant row; speaking again rejoins.
func TestLeaveAndRejoin(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	ctx := context.Background()
	member(t, s, alice, bob)
	c, err := s.CreateChat(ctx, alice, "x")
	require.NoError(t, err)
	_, err = s.ShareChat(ctx, c.ID, bob, RoleViewer, alice)
	require.NoError(t, err)
	_, err = s.AddChatParticipant(ctx, c.ID, bob, JoinedWeb, time.Now())
	require.NoError(t, err)

	left, err := s.LeaveChat(ctx, c.ID, bob)
	require.NoError(t, err)
	assert.True(t, left)
	assert.Equal(t, "", roleOf(t, s, c.ID, bob))
	assert.Empty(t, listed(t, s, bob, SectionMine))
	left, err = s.LeaveChat(ctx, c.ID, bob)
	require.NoError(t, err)
	assert.False(t, left, "nothing left to leave")

	_, err = s.AddChatParticipant(ctx, c.ID, bob, JoinedSlack, time.Now())
	require.NoError(t, err)
	assert.Equal(t, RoleEditor, roleOf(t, s, c.ID, bob))
	assert.ElementsMatch(t, []string{c.ID}, listed(t, s, bob, SectionMine))
}

// The link token is 128 random bits, hex, and resetting it replaces it.
func TestLinkTokenRotates(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	ctx := context.Background()
	c, err := s.CreateChat(ctx, alice, "x")
	require.NoError(t, err)
	assert.Empty(t, c.LinkToken)
	a, err := s.RotateChatLinkToken(ctx, c.ID)
	require.NoError(t, err)
	assert.Len(t, a.LinkToken, 32)
	b, err := s.RotateChatLinkToken(ctx, c.ID)
	require.NoError(t, err)
	assert.Len(t, b.LinkToken, 32)
	assert.NotEqual(t, a.LinkToken, b.LinkToken)
	got, err := s.GetChat(ctx, c.ID)
	require.NoError(t, err)
	assert.Equal(t, b.LinkToken, got.LinkToken)
}

// Unread is what somebody else said after your read mark; your own words never count, and
// the mark only moves forward.
func TestUnreadCounts(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	ctx := context.Background()
	member(t, s, alice, bob)
	c, err := s.CreateChat(ctx, alice, "x")
	require.NoError(t, err)
	_, err = s.ShareChat(ctx, c.ID, bob, RoleEditor, alice)
	require.NoError(t, err)
	say := func(who, role string) ChatMessage {
		m, err := s.AppendChatMessage(ctx, ChatMessage{ChatID: c.ID, Role: role, Text: "hi", AuthorLogin: who})
		require.NoError(t, err)
		return m
	}
	say(alice, RoleUser)
	last := say("", RoleAssistant)
	say(bob, RoleUser)
	_, err = s.AppendChatMessage(ctx, ChatMessage{ChatID: c.ID, Role: RoleNotice, Text: "shared"})
	require.NoError(t, err)

	unread := func(login string) int64 {
		cs, _, err := s.ListChats(ctx, login, false, 100, "")
		require.NoError(t, err)
		require.Len(t, cs, 1)
		return cs[0].Sharing.UnreadCount
	}
	assert.Equal(t, int64(2), unread(alice), "Podium's answer and bob's message; not her own, not the notice")
	assert.Equal(t, int64(2), unread(bob), "alice's message and Podium's answer")

	require.NoError(t, s.MarkChatRead(ctx, c.ID, bob, last.Seq))
	assert.Equal(t, int64(0), unread(bob))
	require.NoError(t, s.MarkChatRead(ctx, c.ID, bob, 1))
	assert.Equal(t, int64(0), unread(bob), "a read mark never moves back")
}

// A share notifies once; a mention notifies whoever can open the chat, never the author and
// never somebody without access.
func TestNotifications(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	ctx := context.Background()
	member(t, s, alice, bob, carol)
	c, err := s.CreateChat(ctx, alice, "launch")
	require.NoError(t, err)

	_, err = s.ShareChat(ctx, c.ID, bob, RoleViewer, alice)
	require.NoError(t, err)
	_, err = s.ShareChat(ctx, c.ID, bob, RoleEditor, alice)
	require.NoError(t, err)
	ns, err := s.ListNotifications(ctx, bob, true, 10)
	require.NoError(t, err)
	require.Len(t, ns, 1, "a change of role is not news")
	assert.Equal(t, NotifyShare, ns[0].Kind)
	assert.Equal(t, alice, ns[0].Actor)
	assert.Equal(t, "launch", ns[0].ChatTitle)

	m, err := s.AppendChatMessage(ctx, ChatMessage{
		ChatID: c.ID, Role: RoleUser, Text: "@bob @carol @alice look", AuthorLogin: alice,
		Mentions: []string{bob, carol, alice},
	})
	require.NoError(t, err)
	ns, err = s.ListNotifications(ctx, bob, true, 10)
	require.NoError(t, err)
	require.Len(t, ns, 2)
	assert.Equal(t, NotifyMention, ns[0].Kind)
	assert.Equal(t, m.Seq, ns[0].Seq)
	none, err := s.ListNotifications(ctx, carol, true, 10)
	require.NoError(t, err)
	assert.Empty(t, none, "carol cannot open the chat, so a mention tells her nothing")
	self, err := s.ListNotifications(ctx, alice, true, 10)
	require.NoError(t, err)
	assert.Empty(t, self, "nobody is notified of their own words")

	n, err := s.MarkNotificationsRead(ctx, bob, []int64{ns[0].ID}, false)
	require.NoError(t, err)
	assert.Equal(t, int64(1), n)
	n, err = s.MarkNotificationsRead(ctx, carol, []int64{ns[1].ID}, false)
	require.NoError(t, err)
	assert.Equal(t, int64(0), n, "somebody else's notification is not yours to mark")
	n, err = s.MarkNotificationsRead(ctx, bob, nil, true)
	require.NoError(t, err)
	assert.Equal(t, int64(1), n)
	unread, err := s.ListNotifications(ctx, bob, true, 10)
	require.NoError(t, err)
	assert.Empty(t, unread)
	all, err := s.ListNotifications(ctx, bob, false, 10)
	require.NoError(t, err)
	assert.Len(t, all, 2)

	// Unsharing hides what was about the chat; access coming back shows it again.
	_, err = s.UnshareChat(ctx, c.ID, bob)
	require.NoError(t, err)
	gone, err := s.ListNotifications(ctx, bob, false, 10)
	require.NoError(t, err)
	assert.Empty(t, gone)
}

// Renaming is the owner's and an editor's, never a viewer's.
func TestRenameIsForEditors(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	ctx := context.Background()
	member(t, s, alice, bob, carol)
	c, err := s.CreateChat(ctx, alice, "x")
	require.NoError(t, err)
	_, err = s.ShareChat(ctx, c.ID, bob, RoleEditor, alice)
	require.NoError(t, err)
	_, err = s.ShareChat(ctx, c.ID, carol, RoleViewer, alice)
	require.NoError(t, err)
	got, err := s.RenameChat(ctx, c.ID, bob, "by bob", false)
	require.NoError(t, err)
	assert.Equal(t, "by bob", got.Title)
	_, err = s.RenameChat(ctx, c.ID, carol, "by carol", false)
	assert.ErrorIs(t, err, ErrNotFound)
}

// Two conductors share one database: a share written through one is the other's answer at
// once, because the rule is in SQL and not in either process.
func TestShareSeenByASecondConductor(t *testing.T) {
	t.Parallel()
	url := newDatabase(t)
	ctx := context.Background()
	a, err := New(ctx, url)
	require.NoError(t, err)
	t.Cleanup(a.Close)
	require.NoError(t, a.Migrate(ctx))
	b, err := New(ctx, url)
	require.NoError(t, err)
	t.Cleanup(b.Close)
	member(t, a, alice, bob)

	c, err := a.CreateChat(ctx, alice, "x")
	require.NoError(t, err)
	assert.Empty(t, listed(t, b, bob, SectionMine))
	_, err = a.ShareChat(ctx, c.ID, bob, RoleViewer, alice)
	require.NoError(t, err)
	assert.Equal(t, []string{c.ID}, listed(t, b, bob, SectionMine))
	assert.Equal(t, RoleViewer, roleOf(t, b, c.ID, bob))
}
