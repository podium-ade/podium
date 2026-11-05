//go:build integration

package api

import (
	"context"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/agent/store"
	agentv1 "github.com/podium-ade/podium/internal/proto/podium/agent/v1"
)

// fakeCanceller is the conductor as CancelChatTurn needs it.
type fakeCanceller struct {
	mu    sync.Mutex
	calls []string
}

func (f *fakeCanceller) CancelChatTurn(_ context.Context, chatID string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, chatID)
	return true, nil
}

// Phase 4's three RPCs, by who may call them: a queued turn is its asker's to take back, a
// running answer is stoppable by anybody who may write the chat, and nobody else learns the
// chat exists.
func TestTheQueueRPCsArePermissioned(t *testing.T) {
	canceller := &fakeCanceller{}
	f := newChatFixtureWith(t, AgentServiceOptions{Canceller: canceller})
	ctx := context.Background()

	created, err := f.client.CreateChat(ctx, connect.NewRequest(&agentv1.CreateChatRequest{}))
	require.NoError(t, err)
	chatID := created.Msg.GetChat().GetId()
	_, err = f.store.AddChatParticipant(ctx, chatID, "bob", store.JoinedWeb, time.Now())
	require.NoError(t, err)
	row, err := f.store.GetChat(ctx, chatID)
	require.NoError(t, err)
	sess, err := f.store.UpsertSession(ctx, store.Session{SourceKind: "chat", SourceKey: row.SourceKey, Profile: "podium"})
	require.NoError(t, err)
	_, err = f.store.EnqueueTurn(ctx, store.QueuedTurn{
		SessionID: sess.ID, SourceKind: "chat", Login: "bob", Author: "bob", ChatID: chatID, MessageSeq: 3, Event: []byte(`{}`),
	})
	require.NoError(t, err)

	// alice owns the chat and still cannot take back bob's turn.
	_, err = f.clientAs("alice").CancelQueuedTurn(ctx, connect.NewRequest(&agentv1.CancelQueuedTurnRequest{ChatId: chatID, Seq: 3}))
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	_, err = f.clientAs("bob").CancelQueuedTurn(ctx, connect.NewRequest(&agentv1.CancelQueuedTurnRequest{ChatId: chatID, Seq: 3}))
	require.NoError(t, err)
	_, err = f.clientAs("bob").CancelQueuedTurn(ctx, connect.NewRequest(&agentv1.CancelQueuedTurnRequest{ChatId: chatID, Seq: 3}))
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err), "taken back once")

	// Either person in the chat may stop the answer; a stranger cannot learn the chat exists.
	for _, who := range []string{"alice", "bob"} {
		res, err := f.clientAs(who).CancelChatTurn(ctx, connect.NewRequest(&agentv1.CancelChatTurnRequest{ChatId: chatID}))
		require.NoError(t, err, who)
		assert.True(t, res.Msg.GetStopped())
	}
	_, err = f.clientAs("mallory").CancelChatTurn(ctx, connect.NewRequest(&agentv1.CancelChatTurnRequest{ChatId: chatID}))
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	assert.Equal(t, []string{chatID, chatID}, canceller.calls)
}

// The @-mention list is everybody who has signed in, named by whichever account says so.
func TestSearchMembersFindsPeopleByNameOrLogin(t *testing.T) {
	f := newChatFixture(t)
	ctx := context.Background()
	for _, id := range []store.Identity{
		{Login: "dana@podium.test", Provider: store.ProviderWeb, ExternalID: "dana@podium.test"},
		{Login: "dana@podium.test", Provider: store.ProviderSlack, ExternalID: "T1/U1", DisplayName: "Dana Scully"},
		{Login: "ed@podium.test", Provider: store.ProviderWeb, ExternalID: "ed@podium.test"},
		// Somebody seen in Slack who never signed in is not a member.
		{Login: "zed@podium.test", Provider: store.ProviderSlack, ExternalID: "T1/U9", DisplayName: "Dana Zed"},
	} {
		require.NoError(t, f.store.UpsertIdentity(ctx, id))
	}

	res, err := f.client.SearchMembers(ctx, connect.NewRequest(&agentv1.SearchMembersRequest{Query: "scul"}))
	require.NoError(t, err)
	require.Len(t, res.Msg.GetMembers(), 1)
	assert.Equal(t, "dana@podium.test", res.Msg.GetMembers()[0].GetLogin())
	assert.Equal(t, "Dana Scully", res.Msg.GetMembers()[0].GetName())

	res, err = f.client.SearchMembers(ctx, connect.NewRequest(&agentv1.SearchMembersRequest{Query: "dana"}))
	require.NoError(t, err)
	require.Len(t, res.Msg.GetMembers(), 1, "zed never signed in")

	res, err = f.client.SearchMembers(ctx, connect.NewRequest(&agentv1.SearchMembersRequest{Query: "%"}))
	require.NoError(t, err)
	assert.Empty(t, res.Msg.GetMembers(), "a LIKE wildcard is a literal")
}
