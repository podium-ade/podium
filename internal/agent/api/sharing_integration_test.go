//go:build integration

package api

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/agent/store"
	agentv1 "github.com/podium-ade/podium/internal/proto/podium/agent/v1"
	"github.com/podium-ade/podium/internal/proto/podium/agent/v1/agentv1connect"
)

// fakeAuditor is the control plane's audit_log, as the sharing RPCs write it.
type fakeAuditor struct {
	mu      sync.Mutex
	actions []string
}

func (f *fakeAuditor) AuditChat(_ context.Context, actor, action, chatID string, _ map[string]any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.actions = append(f.actions, actor+" "+action+" "+chatID)
	return nil
}

func (f *fakeAuditor) seen() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.actions...)
}

// okRetrier and okCanceller always succeed, so the matrix measures the permission check alone.
type okRetrier struct{}

func (okRetrier) RetryDelivery(context.Context, string, uint64) error { return nil }

type okCanceller struct{}

func (okCanceller) CancelChatTurn(context.Context, string) (bool, error) { return true, nil }

type sharingFixture struct {
	chatFixture
	audit *fakeAuditor
}

func newSharingFixture(t *testing.T) sharingFixture {
	t.Helper()
	audit := &fakeAuditor{}
	f := newChatFixtureWith(t, AgentServiceOptions{
		Auditor: audit, Deliveries: okRetrier{}, Canceller: okCanceller{},
	})
	// Everybody in these tests has signed in to Podium, which is what a web identity is.
	for _, l := range []string{"alice", "bob", "carol", "dana", "erin", "mallory"} {
		require.NoError(t, f.store.UpsertIdentity(context.Background(), store.Identity{
			Login: l, Provider: store.ProviderWeb, ExternalID: l, DisplayName: l,
		}))
	}
	return sharingFixture{chatFixture: f, audit: audit}
}

// openAs is StreamChat's first answer for one login: nil when the chat opened, the error
// otherwise. linkToken is the k= of a share link.
func (f sharingFixture) openAs(t *testing.T, login, chatID, linkToken string) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client := agentv1connect.NewAgentServiceClient(http.DefaultClient, f.url)
	req := connect.NewRequest(&agentv1.StreamChatRequest{ChatId: chatID, LinkToken: linkToken})
	req.Header().Set("Authorization", "Bearer "+chatToken)
	req.Header().Set(LoginHeader, login)
	stream, err := client.StreamChat(ctx, req)
	if err != nil {
		return err
	}
	defer func() { _ = stream.Close() }()
	if !stream.Receive() {
		return stream.Err()
	}
	return nil
}

func (f sharingFixture) listAs(t *testing.T, login, section string) map[string]*agentv1.Chat {
	t.Helper()
	res, err := f.clientAs(login).ListChats(context.Background(), connect.NewRequest(&agentv1.ListChatsRequest{Section: section}))
	require.NoError(t, err)
	out := map[string]*agentv1.Chat{}
	for _, c := range res.Msg.GetChats() {
		out[c.GetId()] = c
	}
	return out
}

func codeOf(err error) string {
	if err == nil {
		return "ok"
	}
	return connect.CodeOf(err).String()
}

// The phase 5 permission matrix, through the real handlers: every kind of access against
// every action. A viewer is told they only have view access; somebody with no access is told
// nothing exists.
func TestSharingPermissionMatrix(t *testing.T) {
	f := newSharingFixture(t)
	ctx := context.Background()

	created, err := f.clientAs("alice").CreateChat(ctx, connect.NewRequest(&agentv1.CreateChatRequest{}))
	require.NoError(t, err)
	chatID := created.Msg.GetChat().GetId()
	for login, role := range map[string]string{"bob": store.RoleEditor, "carol": store.RoleViewer} {
		_, err := f.clientAs("alice").ShareChat(ctx, connect.NewRequest(&agentv1.ShareChatRequest{
			ChatId: chatID, Login: login, Role: role,
		}))
		require.NoError(t, err)
	}

	type expect struct {
		list                                              bool
		open, send, rename, share, retry, cancel, sharing string
	}
	matrix := map[string]expect{
		"alice":   {true, "ok", "ok", "ok", "ok", "ok", "ok", "ok"},
		"bob":     {true, "ok", "ok", "ok", "permission_denied", "ok", "ok", "ok"},
		"carol":   {true, "ok", "permission_denied", "permission_denied", "permission_denied", "permission_denied", "permission_denied", "ok"},
		"mallory": {false, "not_found", "not_found", "not_found", "not_found", "not_found", "not_found", "not_found"},
	}
	for login, want := range matrix {
		c := f.clientAs(login)
		_, listed := f.listAs(t, login, "")[chatID]
		assert.Equal(t, want.list, listed, "%s list", login)
		assert.Equal(t, want.open, codeOf(f.openAs(t, login, chatID, "")), "%s open", login)
		_, err := c.SendChatMessage(ctx, connect.NewRequest(&agentv1.SendChatMessageRequest{ChatId: chatID, Text: "hello from " + login}))
		assert.Equal(t, want.send, codeOf(err), "%s send", login)
		_, err = c.RenameChat(ctx, connect.NewRequest(&agentv1.RenameChatRequest{ChatId: chatID, Title: "by " + login}))
		assert.Equal(t, want.rename, codeOf(err), "%s rename", login)
		_, err = c.ShareChat(ctx, connect.NewRequest(&agentv1.ShareChatRequest{ChatId: chatID, Login: "dana", Role: store.RoleViewer}))
		assert.Equal(t, want.share, codeOf(err), "%s share", login)
		_, err = c.RetryChatDelivery(ctx, connect.NewRequest(&agentv1.RetryChatDeliveryRequest{ChatId: chatID, Seq: 1}))
		assert.Equal(t, want.retry, codeOf(err), "%s retry", login)
		_, err = c.CancelChatTurn(ctx, connect.NewRequest(&agentv1.CancelChatTurnRequest{ChatId: chatID}))
		assert.Equal(t, want.cancel, codeOf(err), "%s cancel", login)
		sh, err := c.GetChatSharing(ctx, connect.NewRequest(&agentv1.GetChatSharingRequest{ChatId: chatID}))
		assert.Equal(t, want.sharing, codeOf(err), "%s sharing", login)
		if err == nil {
			assert.Equal(t, "alice", sh.Msg.GetSharing().GetOwner().GetLogin())
		}
	}

	// The roles the list reports are the ones the matrix enforced.
	assert.Equal(t, store.RoleOwner, f.listAs(t, "alice", "")[chatID].GetMyRole())
	assert.Equal(t, store.RoleEditor, f.listAs(t, "bob", "")[chatID].GetMyRole())
	assert.Equal(t, store.AccessShared, f.listAs(t, "carol", "")[chatID].GetAccessReason())
	assert.Equal(t, "alice", f.listAs(t, "carol", "shared")[chatID].GetSharedBy())

	// Leave is for anybody but the owner; delete is the owner's alone.
	_, err = f.clientAs("alice").LeaveChat(ctx, connect.NewRequest(&agentv1.LeaveChatRequest{ChatId: chatID}))
	assert.Equal(t, "failed_precondition", codeOf(err))
	for _, who := range []string{"bob", "carol"} {
		_, err = f.clientAs(who).DeleteChat(ctx, connect.NewRequest(&agentv1.DeleteChatRequest{ChatId: chatID}))
		assert.Equal(t, "permission_denied", codeOf(err), "%s delete", who)
	}
	_, err = f.clientAs("mallory").DeleteChat(ctx, connect.NewRequest(&agentv1.DeleteChatRequest{ChatId: chatID}))
	assert.Equal(t, "not_found", codeOf(err))
	_, err = f.clientAs("carol").LeaveChat(ctx, connect.NewRequest(&agentv1.LeaveChatRequest{ChatId: chatID}))
	require.NoError(t, err)
	assert.NotContains(t, f.listAs(t, "carol", ""), chatID)
	assert.Equal(t, "not_found", codeOf(f.openAs(t, "carol", chatID, "")))
	_, err = f.clientAs("alice").DeleteChat(ctx, connect.NewRequest(&agentv1.DeleteChatRequest{ChatId: chatID}))
	require.NoError(t, err)
	assert.NotContains(t, f.listAs(t, "bob", ""), chatID)

	assert.Subset(t, f.audit.seen(), []string{
		"alice chat.share " + chatID,
		"carol chat.leave " + chatID,
		"alice chat.delete " + chatID,
	})
}

// A link lets a signed-in person in, once, and leaves them a row; resetting it stops new
// people joining with the old one, and narrowing general access stops the link altogether.
// Only the owner is ever shown the token.
func TestLinkSharing(t *testing.T) {
	f := newSharingFixture(t)
	ctx := context.Background()
	created, err := f.clientAs("alice").CreateChat(ctx, connect.NewRequest(&agentv1.CreateChatRequest{}))
	require.NoError(t, err)
	chatID := created.Msg.GetChat().GetId()
	_, err = f.clientAs("bob").UpdateChatSharing(ctx, connect.NewRequest(&agentv1.UpdateChatSharingRequest{
		ChatId: chatID, GeneralAccess: store.AccessLink,
	}))
	assert.Equal(t, "not_found", codeOf(err), "bob cannot see it, let alone share it")

	res, err := f.clientAs("alice").UpdateChatSharing(ctx, connect.NewRequest(&agentv1.UpdateChatSharingRequest{
		ChatId: chatID, GeneralAccess: store.AccessLink, GeneralRole: store.RoleEditor,
	}))
	require.NoError(t, err)
	token := res.Msg.GetSharing().GetLinkToken()
	require.Len(t, token, 32, "turning the link on makes one")

	assert.Equal(t, "not_found", codeOf(f.openAs(t, "dana", chatID, "")), "no token, no entry")
	assert.Equal(t, "not_found", codeOf(f.openAs(t, "dana", chatID, token[:31]+"x")), "a wrong token is no token")
	require.NoError(t, f.openAs(t, "dana", chatID, token))
	listed := f.listAs(t, "dana", "")
	require.Contains(t, listed, chatID, "opening by link keeps it in her list")
	assert.Equal(t, store.RoleEditor, listed[chatID].GetMyRole())
	assert.Equal(t, store.GrantedByLink, listed[chatID].GetSharedBy())
	assert.True(t, listed[chatID].GetMultiplayer(), "a shared chat is multiplayer")

	sh, err := f.clientAs("dana").GetChatSharing(ctx, connect.NewRequest(&agentv1.GetChatSharingRequest{ChatId: chatID}))
	require.NoError(t, err)
	assert.Empty(t, sh.Msg.GetSharing().GetLinkToken(), "the link is the owner's to hand out")

	res, err = f.clientAs("alice").UpdateChatSharing(ctx, connect.NewRequest(&agentv1.UpdateChatSharingRequest{
		ChatId: chatID, RotateLink: true,
	}))
	require.NoError(t, err)
	fresh := res.Msg.GetSharing().GetLinkToken()
	assert.NotEqual(t, token, fresh)
	assert.Equal(t, "not_found", codeOf(f.openAs(t, "erin", chatID, token)), "the old link no longer lets anybody in")
	require.NoError(t, f.openAs(t, "dana", chatID, ""), "dana joined before the reset and keeps her access")
	require.NoError(t, f.openAs(t, "erin", chatID, fresh))

	_, err = f.clientAs("alice").UpdateChatSharing(ctx, connect.NewRequest(&agentv1.UpdateChatSharingRequest{
		ChatId: chatID, GeneralAccess: store.AccessRestricted,
	}))
	require.NoError(t, err)
	assert.Equal(t, "not_found", codeOf(f.openAs(t, "mallory", chatID, fresh)), "restricted: the link stops working")

	seen := f.audit.seen()
	assert.Contains(t, seen, "alice chat.general_access "+chatID)
	assert.Contains(t, seen, "alice chat.link_reset "+chatID)
	assert.Contains(t, seen, "dana chat.join "+chatID)
}

// Shared with members: in everybody's Workspace section, opened into their own list.
func TestMembersSharing(t *testing.T) {
	f := newSharingFixture(t)
	ctx := context.Background()
	created, err := f.clientAs("alice").CreateChat(ctx, connect.NewRequest(&agentv1.CreateChatRequest{}))
	require.NoError(t, err)
	chatID := created.Msg.GetChat().GetId()
	_, err = f.clientAs("alice").UpdateChatSharing(ctx, connect.NewRequest(&agentv1.UpdateChatSharingRequest{
		ChatId: chatID, GeneralAccess: store.AccessMembers,
	}))
	require.NoError(t, err)

	ws := f.listAs(t, "bob", "workspace")
	require.Contains(t, ws, chatID)
	assert.Equal(t, store.AccessWorkspace, ws[chatID].GetAccessReason())
	assert.Equal(t, store.RoleViewer, ws[chatID].GetMyRole())
	assert.NotContains(t, f.listAs(t, "bob", ""), chatID)

	require.NoError(t, f.openAs(t, "bob", chatID, ""))
	assert.Contains(t, f.listAs(t, "bob", ""), chatID)
	assert.NotContains(t, f.listAs(t, "bob", "workspace"), chatID)

	// A members viewer reads and cannot write.
	_, err = f.clientAs("bob").SendChatMessage(ctx, connect.NewRequest(&agentv1.SendChatMessageRequest{ChatId: chatID, Text: "hi"}))
	assert.Equal(t, "permission_denied", codeOf(err))

	_, err = f.clientAs("bob").ListChats(ctx, connect.NewRequest(&agentv1.ListChatsRequest{Section: "everything"}))
	assert.Equal(t, "invalid_argument", codeOf(err))
}

// Unsharing somebody who is watching ends their stream: the next change of the chat's row is
// answered for that viewer, and they no longer have access.
func TestUnshareEndsAWatchingStream(t *testing.T) {
	f := newSharingFixture(t)
	ctx := context.Background()
	created, err := f.clientAs("alice").CreateChat(ctx, connect.NewRequest(&agentv1.CreateChatRequest{}))
	require.NoError(t, err)
	chatID := created.Msg.GetChat().GetId()
	_, err = f.clientAs("alice").ShareChat(ctx, connect.NewRequest(&agentv1.ShareChatRequest{ChatId: chatID, Login: "bob", Role: store.RoleViewer}))
	require.NoError(t, err)

	sctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	stream := f.streamAs(sctx, t, "bob", chatID, 0)
	head := nextFrame(t, stream) // status
	require.NotNil(t, head.GetStatus())
	row := nextFrame(t, stream)
	require.NotNil(t, row.GetChat())
	assert.Equal(t, store.RoleViewer, row.GetChat().GetMyRole())

	// Promoted: the next row says so.
	_, err = f.clientAs("alice").ShareChat(ctx, connect.NewRequest(&agentv1.ShareChatRequest{ChatId: chatID, Login: "bob", Role: store.RoleEditor}))
	require.NoError(t, err)
	for {
		fr := nextFrame(t, stream)
		if c := fr.GetChat(); c != nil && c.GetMyRole() == store.RoleEditor {
			break
		}
	}

	_, err = f.clientAs("alice").UnshareChat(ctx, connect.NewRequest(&agentv1.UnshareChatRequest{ChatId: chatID, Login: "bob"}))
	require.NoError(t, err)
	for stream.Receive() {
		// Drain whatever was already on the way.
	}
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(stream.Err()))
}

// Deleting a chat somebody is watching ends their stream: the owner's delete announces a
// tombstone, so the stream is not left open on a chat that is gone.
func TestDeletingAChatEndsAWatchingStream(t *testing.T) {
	f := newSharingFixture(t)
	ctx := context.Background()
	created, err := f.clientAs("alice").CreateChat(ctx, connect.NewRequest(&agentv1.CreateChatRequest{}))
	require.NoError(t, err)
	chatID := created.Msg.GetChat().GetId()
	_, err = f.clientAs("alice").ShareChat(ctx, connect.NewRequest(&agentv1.ShareChatRequest{ChatId: chatID, Login: "bob", Role: store.RoleViewer}))
	require.NoError(t, err)

	sctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	stream := f.streamAs(sctx, t, "bob", chatID, 0)
	require.NotNil(t, nextFrame(t, stream).GetStatus())
	require.NotNil(t, nextFrame(t, stream).GetChat())

	_, err = f.clientAs("alice").DeleteChat(ctx, connect.NewRequest(&agentv1.DeleteChatRequest{ChatId: chatID}))
	require.NoError(t, err)
	for stream.Receive() {
	}
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(stream.Err()),
		"the stream ends on the delete, not when the client gives up")
}

// A path that takes somebody's access away without announcing it still cannot leave their
// stream showing what is said next: every message is checked against the viewer's role.
func TestAMessageAfterAccessIsGoneEndsTheStream(t *testing.T) {
	f := newSharingFixture(t)
	ctx := context.Background()
	created, err := f.clientAs("alice").CreateChat(ctx, connect.NewRequest(&agentv1.CreateChatRequest{}))
	require.NoError(t, err)
	chatID := created.Msg.GetChat().GetId()
	_, err = f.clientAs("alice").ShareChat(ctx, connect.NewRequest(&agentv1.ShareChatRequest{ChatId: chatID, Login: "bob", Role: store.RoleViewer}))
	require.NoError(t, err)

	sctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	stream := f.streamAs(sctx, t, "bob", chatID, 0)
	require.NotNil(t, nextFrame(t, stream).GetStatus())
	require.NotNil(t, nextFrame(t, stream).GetChat())

	// Straight to the store: nothing announces this.
	_, err = f.store.UnshareChat(ctx, chatID, "bob")
	require.NoError(t, err)
	_, err = f.clientAs("alice").SendChatMessage(ctx, connect.NewRequest(&agentv1.SendChatMessageRequest{ChatId: chatID, Text: "after bob left"}))
	require.NoError(t, err)
	for stream.Receive() {
		if m := stream.Msg().GetMessage(); m != nil {
			assert.NotEqual(t, "after bob left", m.GetText(), "a viewer without access is shown nothing more")
		}
	}
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(stream.Err()))
}

// Shares and mentions reach the person through their inbox; read marks clear unread counts.
func TestNotificationsAndReadMarksThroughTheService(t *testing.T) {
	f := newSharingFixture(t)
	ctx := context.Background()
	created, err := f.clientAs("alice").CreateChat(ctx, connect.NewRequest(&agentv1.CreateChatRequest{Title: "launch"}))
	require.NoError(t, err)
	chatID := created.Msg.GetChat().GetId()
	_, err = f.clientAs("alice").ShareChat(ctx, connect.NewRequest(&agentv1.ShareChatRequest{ChatId: chatID, Login: "bob", Role: store.RoleEditor}))
	require.NoError(t, err)
	_, err = f.clientAs("alice").ShareChat(ctx, connect.NewRequest(&agentv1.ShareChatRequest{ChatId: chatID, Login: "nobody", Role: store.RoleEditor}))
	assert.Equal(t, "failed_precondition", codeOf(err), "a login that never signed in")

	inbox, err := f.clientAs("bob").ListNotifications(ctx, connect.NewRequest(&agentv1.ListNotificationsRequest{UnreadOnly: true}))
	require.NoError(t, err)
	require.Len(t, inbox.Msg.GetNotifications(), 1)
	n := inbox.Msg.GetNotifications()[0]
	assert.Equal(t, store.NotifyShare, n.GetKind())
	assert.Equal(t, "launch", n.GetChatTitle())
	assert.Equal(t, "alice", n.GetActor())

	sent, err := f.clientAs("alice").SendChatMessage(ctx, connect.NewRequest(&agentv1.SendChatMessageRequest{
		ChatId: chatID, Text: "@[bob](mention:bob) can you look?",
	}))
	require.NoError(t, err)
	inbox, err = f.clientAs("bob").ListNotifications(ctx, connect.NewRequest(&agentv1.ListNotificationsRequest{UnreadOnly: true}))
	require.NoError(t, err)
	require.Len(t, inbox.Msg.GetNotifications(), 2)
	assert.Equal(t, store.NotifyMention, inbox.Msg.GetNotifications()[0].GetKind())

	assert.Equal(t, int64(1), f.listAs(t, "bob", "")[chatID].GetUnreadCount())
	_, err = f.clientAs("bob").MarkChatRead(ctx, connect.NewRequest(&agentv1.MarkChatReadRequest{
		ChatId: chatID, Seq: sent.Msg.GetMessage().GetSeq(),
	}))
	require.NoError(t, err)
	assert.Equal(t, int64(0), f.listAs(t, "bob", "")[chatID].GetUnreadCount())
	_, err = f.clientAs("mallory").MarkChatRead(ctx, connect.NewRequest(&agentv1.MarkChatReadRequest{ChatId: chatID, Seq: 1}))
	assert.Equal(t, "not_found", codeOf(err))

	_, err = f.clientAs("bob").MarkNotificationsRead(ctx, connect.NewRequest(&agentv1.MarkNotificationsReadRequest{All: true}))
	require.NoError(t, err)
	inbox, err = f.clientAs("bob").ListNotifications(ctx, connect.NewRequest(&agentv1.ListNotificationsRequest{UnreadOnly: true}))
	require.NoError(t, err)
	assert.Empty(t, inbox.Msg.GetNotifications())
}

// The share dialog warns about a mirrored Slack conversation: certainly private for a DM,
// possibly private for a channel Podium cannot tell.
func TestSourcePrivacy(t *testing.T) {
	for _, tc := range []struct {
		origin, key, privacy string
		private, unknown     bool
	}{
		{store.OriginWeb, "chat:x", "", false, false},
		// What Slack said about the channel wins.
		{store.OriginSlack, "slack:C123:1.1", store.ChannelPublic, false, false},
		{store.OriginSlack, "slack:C123:1.1", store.ChannelPrivate, true, false},
		{store.OriginSlack, "slack:D123:1.1", store.ChannelIM, true, false},
		{store.OriginSlack, "slack:C123:1.1", store.ChannelMPIM, true, false},
		// A chat recorded before it knew falls back to the id.
		{store.OriginSlack, "slack:D123:1.1", "", true, false},
		{store.OriginSlack, "slack:G123:1.1", "", true, false},
		{store.OriginSlack, "slack:C123:1.1", "", false, true},
		{store.OriginGitHub, "github:acme/repo#1", "", false, false},
	} {
		p, u := sourcePrivacy(store.Chat{Origin: tc.origin, SourceKey: tc.key, ChannelPrivacy: tc.privacy})
		assert.Equal(t, tc.private, p, tc.key)
		assert.Equal(t, tc.unknown, u, tc.key)
	}
}
