//go:build integration

package api

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/agent/store"
	agentv1 "github.com/podium-ade/podium/internal/proto/podium/agent/v1"
	"github.com/podium-ade/podium/internal/proto/podium/agent/v1/agentv1connect"
)

func watch(c agentv1connect.AgentServiceClient, chatID string, on bool) error {
	_, err := c.SetChatWatched(context.Background(), connect.NewRequest(&agentv1.SetChatWatchedRequest{
		ChatId: chatID, Watched: on,
	}))
	return err
}

// Watching keeps a chat the caller may read in their rail; it grants nothing, so a chat they
// cannot read cannot be watched and drops out once they cannot read it any more.
func TestWatchingKeepsAReadableChatInTheRail(t *testing.T) {
	ctx := context.Background()
	f := newSharingFixture(t)

	created, err := f.clientAs("alice").CreateChat(ctx, connect.NewRequest(&agentv1.CreateChatRequest{}))
	require.NoError(t, err)
	chatID := created.Msg.GetChat().GetId()

	assert.Equal(t, "not_found", codeOf(watch(f.clientAs("mallory"), chatID, true)), "no access, no watch")
	assert.NotContains(t, f.listAs(t, "mallory", ""), chatID)

	// Shared with members, carol may read it without its being in her list until she opens it.
	_, err = f.clientAs("alice").UpdateChatSharing(ctx, connect.NewRequest(&agentv1.UpdateChatSharingRequest{
		ChatId: chatID, GeneralAccess: store.AccessMembers, GeneralRole: store.RoleViewer,
	}))
	require.NoError(t, err)
	assert.NotContains(t, f.listAs(t, "carol", ""), chatID)
	require.NoError(t, watch(f.clientAs("carol"), chatID, true))
	listed := f.listAs(t, "carol", "")
	require.Contains(t, listed, chatID, "a watched chat is in the rail")
	assert.True(t, listed[chatID].GetWatched())

	// Narrowed again, carol may no longer read it, and it drops out though the watch remains.
	_, err = f.clientAs("alice").UpdateChatSharing(ctx, connect.NewRequest(&agentv1.UpdateChatSharingRequest{
		ChatId: chatID, GeneralAccess: store.AccessRestricted, GeneralRole: store.RoleViewer,
	}))
	require.NoError(t, err)
	assert.NotContains(t, f.listAs(t, "carol", ""), chatID, "losing access drops a watched chat")

	// Unwatched, it is gone for good; unwatching is answered whatever the access.
	_, err = f.clientAs("alice").UpdateChatSharing(ctx, connect.NewRequest(&agentv1.UpdateChatSharingRequest{
		ChatId: chatID, GeneralAccess: store.AccessMembers, GeneralRole: store.RoleViewer,
	}))
	require.NoError(t, err)
	assert.Contains(t, f.listAs(t, "carol", ""), chatID)
	require.NoError(t, watch(f.clientAs("carol"), chatID, false))
	assert.NotContains(t, f.listAs(t, "carol", ""), chatID, "unwatching takes it out")
	require.NoError(t, watch(f.clientAs("mallory"), chatID, false))
}

// An admin reads a system routine's chat as an admin: watching it puts it in their rail, and a
// member may not watch it at all.
func TestAnAdminWatchesASystemRoutinesChat(t *testing.T) {
	ctx := context.Background()
	f := newSharingFixture(t)
	created, err := f.adminClient("alice").CreateRoutine(ctx, connect.NewRequest(&agentv1.CreateRoutineRequest{
		Draft: ciFixerDraft(), Scope: store.RoutineScopeSystem,
	}))
	require.NoError(t, err)
	chatID := created.Msg.GetRoutine().GetChatId()

	assert.Equal(t, "not_found", codeOf(watch(f.clientAs("bob"), chatID, true)))
	require.NoError(t, watch(f.adminClient("carol"), chatID, true))

	res, err := f.adminClient("carol").ListChats(ctx, connect.NewRequest(&agentv1.ListChatsRequest{}))
	require.NoError(t, err)
	var ids []string
	for _, c := range res.Msg.GetChats() {
		ids = append(ids, c.GetId())
	}
	assert.Contains(t, ids, chatID, "the admin's watched routine chat is in their rail")
	assert.NotContains(t, f.listAs(t, "carol", ""), chatID, "not once they are no longer an admin")
}
