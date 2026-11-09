package chat

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/agent/conductor"
	"github.com/podium-ade/podium/internal/agent/store"
)

// In a chat with one person in it, everything they say starts a turn, as it always has.
func TestASinglePlayerChatAnswersEveryMessage(t *testing.T) {
	st := newFakeStore()
	st.add("chat_1", "alice")
	src := newSource(t, st)

	res, err := src.SendMessage(context.Background(), SendRequest{ChatID: "chat_1", Login: "alice", Text: "no tag"})
	require.NoError(t, err)
	assert.True(t, res.Addressed)
	assert.True(t, res.Message.Addressed)
	assert.Len(t, src.Events(), 1)
}

// Once a chat is multiplayer, a message has to tag Podium. One that does not is the
// conversation around the bot: recorded, never a turn.
func TestAMultiplayerChatAnswersOnlyAMention(t *testing.T) {
	st := newFakeStore()
	c := st.add("chat_1", "alice")
	c.Multiplayer = true
	st.chats["chat_1"] = c
	src := newSource(t, st)
	ctx := context.Background()

	res, err := src.SendMessage(ctx, SendRequest{ChatID: "chat_1", Login: "alice", Text: "bob, look at this"})
	require.NoError(t, err)
	assert.False(t, res.Addressed)
	assert.False(t, res.Message.Addressed, "stored as unaddressed")
	assert.Empty(t, src.Events(), "and nothing for the conductor")

	res, err = src.SendMessage(ctx, SendRequest{ChatID: "chat_1", Login: "alice", Text: "@Podium summarise it"})
	require.NoError(t, err)
	assert.True(t, res.Addressed)
	require.Len(t, src.Events(), 1)
	ev := <-src.Events()
	assert.Equal(t, "alice", ev.Login, "the turn runs as whoever tagged it")
	assert.Equal(t, "chat_1", ev.ChatID)
	assert.Equal(t, res.Message.Seq, ev.ChatSeq)
}

// A person who has taken part may write the chat; anybody else gets "no such chat".
func TestOnlyTheOwnerAndParticipantsMayWrite(t *testing.T) {
	st := newFakeStore()
	st.add("chat_1", "alice")
	src := newSource(t, st)
	ctx := context.Background()

	_, err := src.Send(ctx, SendRequest{ChatID: "chat_1", Login: "mallory", Text: "hi"})
	assert.True(t, errors.Is(err, store.ErrNotFound), "a stranger cannot learn the chat exists")

	st.participants["chat_1"] = []string{"alice", "bob"}
	_, err = src.Send(ctx, SendRequest{ChatID: "chat_1", Login: "bob", Text: "@Podium hi"})
	require.NoError(t, err)
}

// A mirrored conversation can be written from the web. The message is put to the session
// the chat is the record of, and answered in the web chat (Ref is the chat).
func TestAMirroredChatCanBeWrittenAndStaysInTheWeb(t *testing.T) {
	st := newFakeStore()
	c := st.add("chat_1", "alice")
	c.Origin = "slack"
	c.SourceKey = "slack:C1:1.0"
	c.Multiplayer = true
	st.chats["chat_1"] = c
	src := newSource(t, st)

	res, err := src.SendMessage(context.Background(), SendRequest{
		ChatID: "chat_1", Login: "alice", Text: "@Podium what did we decide?",
	})
	require.NoError(t, err)
	require.True(t, res.Addressed)
	ev := <-src.Events()
	assert.Equal(t, "slack:C1:1.0", ev.SourceKey, "queued behind the thread's own turns")
	assert.Equal(t, "chat_1", ev.Ref, "answered here, never in Slack")
	assert.Equal(t, conductor.SourceChat, ev.SourceKind)
}

// The second Podium person to write a chat makes it multiplayer, once, with a notice.
func TestASecondPersonMakesAChatMultiplayerOnce(t *testing.T) {
	st := newFakeStore()
	st.add("chat_1", "alice")
	st.participants["chat_1"] = []string{"alice", "bob"} // bob was shared in (phase 5)
	src := newSource(t, st)
	ctx := context.Background()

	_, err := src.Send(ctx, SendRequest{ChatID: "chat_1", Login: "alice", Text: "first"})
	require.NoError(t, err)
	chat, _ := st.GetChat(ctx, "chat_1")
	assert.True(t, chat.Multiplayer)

	msgs, _ := st.ListChatMessages(ctx, "chat_1", 0)
	var notices int
	for _, m := range msgs {
		if m.Role == store.RoleNotice {
			notices++
		}
	}
	assert.Equal(t, 1, notices)

	_, err = src.Send(ctx, SendRequest{ChatID: "chat_1", Login: "bob", Text: "second"})
	require.NoError(t, err)
	msgs, _ = st.ListChatMessages(ctx, "chat_1", 0)
	notices = 0
	for _, m := range msgs {
		if m.Role == store.RoleNotice {
			notices++
		}
	}
	assert.Equal(t, 1, notices, "announced once")
}

// A shared chat with somebody else's personal assistant still talks to that assistant, under
// the sender's own name, and answers to the assistant's name.
func TestASharedPersonalityChatKeepsTheOwnersAssistant(t *testing.T) {
	st := newFakeStore()
	c := st.add("chat_1", "alice")
	c.PersonalityID = "pers_1"
	c.Multiplayer = true
	st.chats["chat_1"] = c
	st.personalities = map[string]store.Personality{
		"pers_1": {ID: "pers_1", Login: "alice", Name: "Researcher", Instructions: "be thorough"},
	}
	st.participants["chat_1"] = []string{"alice", "bob"}
	src := newSource(t, st)

	res, err := src.SendMessage(context.Background(), SendRequest{
		ChatID: "chat_1", Login: "bob", Text: "@Researcher what is new?",
	})
	require.NoError(t, err)
	require.True(t, res.Addressed)
	ev := <-src.Events()
	assert.Equal(t, "Researcher", ev.PersonalityName)
	assert.Equal(t, "be thorough", ev.PersonalityPrompt)
	assert.Equal(t, "bob", ev.Login, "bob's credentials, alice's assistant")
}

// The queue frame carries the turns waiting.
func TestQueueChangedPublishesTheQueue(t *testing.T) {
	st := newFakeStore()
	st.add("chat_1", "alice")
	st.queued["chat_1"] = []store.QueuedTurn{{Login: "bob", MessageSeq: 4, SourceKind: "chat"}}
	src := newSource(t, st)
	sub := src.Subscribe(context.Background(), "chat_1")
	defer sub.Close()

	src.QueueChanged("chat_1")
	f := recv(t, sub)
	assert.Equal(t, FrameQueue, f.Kind)
	require.Len(t, f.Queue, 1)
	assert.Equal(t, "bob", f.Queue[0].Login)
}
