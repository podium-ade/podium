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

// questionStore is a fakeStore that also says whether a Linear conversation waits on somebody.
type questionStore struct {
	*fakeStore
	qmu    sync.Mutex
	issues map[string]store.LinearIssue
}

func (q *questionStore) GetLinearIssue(_ context.Context, key string) (store.LinearIssue, error) {
	q.qmu.Lock()
	defer q.qmu.Unlock()
	li, ok := q.issues[key]
	if !ok {
		return store.LinearIssue{}, store.ErrNotFound
	}
	return li, nil
}

func (q *questionStore) ask(key, of string) {
	q.qmu.Lock()
	defer q.qmu.Unlock()
	now := time.Now()
	q.issues[key] = store.LinearIssue{SourceKey: key, AskedAt: &now, AskedOf: of}
}

func (q *questionStore) answered(key string) {
	q.qmu.Lock()
	defer q.qmu.Unlock()
	q.issues[key] = store.LinearIssue{SourceKey: key}
}

// In a ticket's conversation that waits on a question, what the person it was asked of sends is
// put to Podium without naming it — which is also what the inbox's reply box sends — even while
// the turn that asked is in progress. Anybody else, and the same person once it is answered, is
// talking to the room.
func TestTheAskedPersonAnswersATicketsQuestionWithoutNamingPodium(t *testing.T) {
	st := &questionStore{fakeStore: newFakeStore(), issues: map[string]store.LinearIssue{}}
	c := st.add("chat_1", "")
	c.Multiplayer = true
	c.Origin = store.OriginLinear
	c.SourceKey = "linear:issue-1"
	st.chats["chat_1"] = c
	st.participants["chat_1"] = []string{"alice", "bob"}
	st.running["chat_1"] = true // the turn that asked is still up, waiting
	src := newSource(t, st)
	ctx := context.Background()

	st.ask("linear:issue-1", "alice")

	res, err := src.SendMessage(ctx, SendRequest{ChatID: "chat_1", Login: "bob", Text: "I think eu-west"})
	require.NoError(t, err)
	assert.False(t, res.Addressed, "somebody else is talking to the room")
	assert.Empty(t, src.Events())

	res, err = src.SendMessage(ctx, SendRequest{ChatID: "chat_1", Login: "alice", Text: "eu-west, since Monday"})
	require.NoError(t, err)
	assert.True(t, res.Addressed, "the asked person's message is the answer")
	assert.NotZero(t, res.Message.Seq, "recorded now, not held behind the turn waiting for it")
	ev := drainEvent(t, src)
	assert.Equal(t, "alice", ev.Login)
	assert.Equal(t, "linear:issue-1", ev.SourceKey)
	assert.False(t, ev.Held)

	st.answered("linear:issue-1")
	res, err = src.SendMessage(ctx, SendRequest{ChatID: "chat_1", Login: "alice", Text: "thanks all"})
	require.NoError(t, err)
	assert.False(t, res.Addressed, "once answered it is the room again")
	assert.Empty(t, src.Events())
}

// A store that cannot say, and a chat that is not a ticket's, never take a message as an answer.
func TestOnlyATicketsConversationTakesAnAnswerUnnamed(t *testing.T) {
	plain := newFakeStore()
	c := plain.add("chat_1", "alice")
	c.Multiplayer = true
	c.SourceKey = "linear:issue-1"
	plain.chats["chat_1"] = c
	src := newSource(t, plain)
	res, err := src.SendMessage(context.Background(), SendRequest{ChatID: "chat_1", Login: "alice", Text: "eu-west"})
	require.NoError(t, err)
	assert.False(t, res.Addressed)

	st := &questionStore{fakeStore: newFakeStore(), issues: map[string]store.LinearIssue{}}
	w := st.add("chat_2", "alice")
	w.Multiplayer = true
	w.SourceKey = "slack:C1/1.1"
	st.chats["chat_2"] = w
	st.ask("slack:C1/1.1", "alice")
	src = newSource(t, st)
	res, err = src.SendMessage(context.Background(), SendRequest{ChatID: "chat_2", Login: "alice", Text: "eu-west"})
	require.NoError(t, err)
	assert.False(t, res.Addressed)
}
