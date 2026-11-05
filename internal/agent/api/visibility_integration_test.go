//go:build integration

package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/agent/store"
	agentv1 "github.com/podium-ade/podium/internal/proto/podium/agent/v1"
	"github.com/podium-ade/podium/internal/proto/podium/agent/v1/agentv1connect"
)

// A Slack thread bob spoke in is his to read through every RPC that reads a conversation —
// the chat list, the sessions list, the session and its turns — and nobody else's but the
// dev token's. Its participants may write it from the web too (phase 4).
func TestAParticipantReadsAMirroredThreadThroughTheService(t *testing.T) {
	f := newChatFixture(t)
	ctx := context.Background()

	thread, err := f.store.CreateMirrorChat(ctx, "slack:C1:1.1", store.OriginSlack, "Bob", "in slack")
	require.NoError(t, err)
	sess, err := f.store.UpsertSession(ctx, store.Session{SourceKind: "slack", SourceKey: "slack:C1:1.1", Profile: "podium"})
	require.NoError(t, err)
	_, err = f.store.CreateTurn(ctx, sess.ID, "C1/1.1/1.1", store.Backend{})
	require.NoError(t, err)
	_, err = f.store.AddChatParticipant(ctx, thread.ID, "bob", store.JoinedSlack, time.Now())
	require.NoError(t, err)
	_, err = f.store.ClaimChatOwner(ctx, thread.ID, "bob")
	require.NoError(t, err)

	for _, tc := range []struct {
		name    string
		client  func() agentv1connect.AgentServiceClient
		visible bool
	}{
		{"participant", func() agentv1connect.AgentServiceClient { return f.clientAs("bob") }, true},
		{"somebody else", func() agentv1connect.AgentServiceClient { return f.clientAs("carol") }, false},
		{"dev token", f.clientAll, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := tc.client()
			chats, err := c.ListChats(ctx, connect.NewRequest(&agentv1.ListChatsRequest{}))
			require.NoError(t, err)
			var ids []string
			for _, ch := range chats.Msg.GetChats() {
				ids = append(ids, ch.GetId())
			}
			assert.Equal(t, tc.visible, contains(ids, thread.ID), "ListChats")

			sessions, err := c.ListSessions(ctx, connect.NewRequest(&agentv1.ListSessionsRequest{}))
			require.NoError(t, err)
			var keys []string
			for _, s := range sessions.Msg.GetSessions() {
				keys = append(keys, s.GetId())
			}
			assert.Equal(t, tc.visible, contains(keys, sess.ID), "ListSessions")

			_, err = c.GetSession(ctx, connect.NewRequest(&agentv1.GetSessionRequest{SessionId: sess.ID}))
			assert.Equal(t, tc.visible, err == nil, "GetSession: %v", err)
			turns, err := c.ListTurns(ctx, connect.NewRequest(&agentv1.ListTurnsRequest{SessionId: sess.ID}))
			assert.Equal(t, tc.visible, err == nil, "ListTurns: %v", err)
			if tc.visible {
				assert.Len(t, turns.Msg.GetTurns(), 1)
			} else {
				assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
			}
		})
	}

	// Phase 4: a participant may write the thread from the web too. It is multiplayer, so a
	// message has to mention Podium to be put to it; what does not is recorded and starts
	// nothing. Somebody who has not taken part cannot write it at all.
	said, err := f.clientAs("bob").SendChatMessage(ctx, connect.NewRequest(&agentv1.SendChatMessageRequest{
		ChatId: thread.ID, Text: "from the web",
	}))
	require.NoError(t, err)
	assert.False(t, said.Msg.GetAddressed())
	asked, err := f.clientAs("bob").SendChatMessage(ctx, connect.NewRequest(&agentv1.SendChatMessageRequest{
		ChatId: thread.ID, Text: "@Podium from the web",
	}))
	require.NoError(t, err)
	assert.True(t, asked.Msg.GetAddressed())
	_, err = f.clientAs("carol").SendChatMessage(ctx, connect.NewRequest(&agentv1.SendChatMessageRequest{
		ChatId: thread.ID, Text: "@Podium let me in",
	}))
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

// RecordWebIdentities records each proxied login once, and never the dev token or a call
// with no login.
func TestRecordWebIdentities(t *testing.T) {
	st := newStore(t)
	h := RequireBearer("tok", RecordWebIdentities(st, nil, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})))
	call := func(login string, all bool) {
		req := httptest.NewRequest(http.MethodPost, "/", nil)
		req.Header.Set("Authorization", "Bearer tok")
		if login != "" {
			req.Header.Set(LoginHeader, login)
		}
		if all {
			req.Header.Set(ScopeHeader, ScopeAll)
		}
		h.ServeHTTP(httptest.NewRecorder(), req)
	}
	call("ada@acme.com", false)
	call("ada@acme.com", false)
	call("", false)
	call("local", true)
	call("root@acme.com", true)

	for login, want := range map[string]bool{
		"ada@acme.com": true, "unknown": false, "local": false, "root@acme.com": false,
	} {
		ok, err := st.HasWebIdentity(t.Context(), login)
		require.NoError(t, err)
		assert.Equal(t, want, ok, login)
	}
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
