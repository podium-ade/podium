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

	"github.com/podium-ade/podium/internal/agent/conductor"
	"github.com/podium-ade/podium/internal/agent/store"
	agentv1 "github.com/podium-ade/podium/internal/proto/podium/agent/v1"
)

// fakeRetrier is the conductor's outbox, as RetryChatDelivery sees it.
type fakeRetrier struct {
	mu    sync.Mutex
	calls []uint64
}

func (f *fakeRetrier) RetryDelivery(_ context.Context, _ string, seq uint64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, seq)
	if len(f.calls) > 1 {
		return conductor.ErrNothingToRetry
	}
	return nil
}

// An answer that never reached its thread says so in the web chat, and somebody who can read
// the chat can put it back in the queue. Somebody who cannot read the chat cannot even tell
// it is there.
func TestAFailedDeliveryIsShownAndCanBeRetriedByAParticipant(t *testing.T) {
	retrier := &fakeRetrier{}
	f := newChatFixtureWith(t, AgentServiceOptions{Deliveries: retrier})
	ctx := context.Background()

	thread, err := f.store.CreateMirrorChat(ctx, "slack:C1:5.5", store.OriginSlack, "alice", "in slack")
	require.NoError(t, err)
	_, err = f.store.AddChatParticipant(ctx, thread.ID, "alice", store.ViaSlack, time.Now())
	require.NoError(t, err)
	answer, err := f.store.AppendChatMessage(ctx, store.ChatMessage{
		ChatID: thread.ID, Role: store.RoleAssistant, Text: "the answer", Via: store.ViaBot,
	})
	require.NoError(t, err)
	it, err := f.store.EnqueueOutbox(ctx, thread.ID, answer.Seq, "slack", "C1/5.5/5.5",
		store.OutboxPayload{Type: conductor.OutFinal, Text: "the answer"}, time.Now())
	require.NoError(t, err)
	require.NoError(t, f.store.MarkOutboxRetry(ctx, it.ID, "channel_not_found", time.Now(), true))

	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stream := f.streamAs(sctx, t, "alice", thread.ID, 0)
	var msg *agentv1.ChatMessage
	for range 5 {
		if msg = nextFrame(t, stream).GetMessage(); msg != nil {
			break
		}
	}
	require.NotNil(t, msg, "the replay carries the answer")
	require.Len(t, msg.GetDeliveries(), 1)
	assert.Equal(t, "slack", msg.GetDeliveries()[0].GetSurface())
	assert.Equal(t, store.OutboxFailed, msg.GetDeliveries()[0].GetStatus())
	assert.Equal(t, "channel_not_found", msg.GetDeliveries()[0].GetError())

	_, err = f.clientAs("mallory").RetryChatDelivery(ctx, connect.NewRequest(&agentv1.RetryChatDeliveryRequest{
		ChatId: thread.ID, Seq: answer.Seq,
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err), "a chat you cannot read is not there")
	assert.Empty(t, retrier.calls)

	_, err = f.clientAs("alice").RetryChatDelivery(ctx, connect.NewRequest(&agentv1.RetryChatDeliveryRequest{
		ChatId: thread.ID, Seq: answer.Seq,
	}))
	require.NoError(t, err)
	assert.Equal(t, []uint64{answer.Seq}, retrier.calls)

	_, err = f.clientAs("alice").RetryChatDelivery(ctx, connect.NewRequest(&agentv1.RetryChatDeliveryRequest{
		ChatId: thread.ID, Seq: answer.Seq,
	}))
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err), "nothing left to retry")

	_, err = f.clientAs("alice").RetryChatDelivery(ctx, connect.NewRequest(&agentv1.RetryChatDeliveryRequest{ChatId: thread.ID}))
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestRetryChatDeliveryWithoutAnOutboxIsUnimplemented(t *testing.T) {
	f := newChatFixture(t)
	_, err := f.client.RetryChatDelivery(context.Background(), connect.NewRequest(&agentv1.RetryChatDeliveryRequest{
		ChatId: "chat_x", Seq: 1,
	}))
	assert.Equal(t, connect.CodeUnimplemented, connect.CodeOf(err))
}
