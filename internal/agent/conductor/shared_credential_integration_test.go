//go:build integration

package conductor_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/agent/conductor"
	"github.com/podium-ade/podium/internal/agent/store"
)

// A web chat spends its asker's own model key until a second person is in it, and the company's
// from then on, for good: a shared conversation has one context and one credential.
func TestAChatWithASecondPersonSpendsTheCompanyKey(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	c, err := st.CreateChat(ctx, "alice@podium.test", "t")
	require.NoError(t, err)
	sess, err := st.UpsertSession(ctx, store.Session{SourceKind: "chat", SourceKey: c.SourceKey, Profile: "podium"})
	require.NoError(t, err)

	assert.False(t, conductor.SharedConversationForTest(ctx, st, sess), "a chat nobody else is in is not shared")

	// A share alone puts nobody else in the conversation; a second human does.
	for _, login := range []string{"alice@podium.test", "bob@podium.test"} {
		_, err = st.AddChatParticipant(ctx, c.ID, login, store.JoinedWeb, time.Now())
		require.NoError(t, err)
	}
	_, _, err = st.MarkChatMultiplayer(ctx, c.ID)
	require.NoError(t, err)
	assert.True(t, conductor.SharedConversationForTest(ctx, st, sess), "a second person moves the chat to the company key")
}

// A Slack thread or a pull request is shared by nature, record or not; a conversation with no
// chat record and no shared surface is not.
func TestThreadsAreSharedConversations(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	for _, kind := range []string{conductor.SourceSlack, conductor.SourceGitHub} {
		assert.True(t, conductor.SharedConversationForTest(ctx, st, store.Session{SourceKind: kind, SourceKey: kind + ":x"}), kind)
	}
	assert.False(t, conductor.SharedConversationForTest(ctx, st, store.Session{SourceKind: "chat", SourceKey: "chat:missing"}))
}
