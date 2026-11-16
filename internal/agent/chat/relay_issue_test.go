package chat

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/agent/store"
)

type recordingRelayer struct {
	mu   sync.Mutex
	msgs []store.ChatMessage
}

func (r *recordingRelayer) RelayMessage(_ context.Context, _ string, msg store.ChatMessage) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.msgs = append(r.msgs, msg)
	return true
}

func (r *recordingRelayer) got() []store.ChatMessage {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]store.ChatMessage(nil), r.msgs...)
}

// What a person writes in a Linear issue's conversation is handed on to be posted to the issue,
// whether or not it was put to Podium; a web chat's stays in Podium.
func TestAMessageInATicketsConversationIsRelayedToTheIssue(t *testing.T) {
	st := newFakeStore()
	c := st.add("chat_1", "alice")
	c.Multiplayer = true
	c.Origin = store.OriginLinear
	c.SourceKey = "linear:issue-1"
	st.chats["chat_1"] = c
	st.add("chat_2", "alice")
	src := newSource(t, st)
	r := &recordingRelayer{}
	src.SetRelayer(r)
	ctx := context.Background()

	_, err := src.SendMessage(ctx, SendRequest{ChatID: "chat_1", Login: "alice", Text: "for the team"})
	require.NoError(t, err)
	_, err = src.SendMessage(ctx, SendRequest{ChatID: "chat_1", Login: "alice", Text: "@Podium check it"})
	require.NoError(t, err)
	_, err = src.SendMessage(ctx, SendRequest{ChatID: "chat_2", Login: "alice", Text: "private"})
	require.NoError(t, err)

	require.Eventually(t, func() bool { return len(r.got()) == 2 }, 2*time.Second, time.Millisecond)
	got := r.got()
	texts := []string{got[0].Text, got[1].Text}
	assert.ElementsMatch(t, []string{"for the team", "@Podium check it"}, texts)
	for _, m := range got {
		assert.Equal(t, "alice", m.AuthorLogin)
	}
	time.Sleep(20 * time.Millisecond)
	assert.Len(t, r.got(), 2, "a web chat's message is not relayed")
}
