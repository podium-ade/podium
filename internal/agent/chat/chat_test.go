package chat

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alvaroibarguen/podium/internal/agent/conductor"
	"github.com/alvaroibarguen/podium/internal/agent/store"
)

// fakeStore is the two chat tables in memory. It keeps the one behaviour the SQL is relied
// on for — a per-chat monotonic seq — and nothing else.
type fakeStore struct {
	mu       sync.Mutex
	chats    map[string]store.Chat
	messages map[string][]store.ChatMessage
	running  map[string]bool
	// failAppend makes the next AppendChatMessage fail, to prove the turn slot is given
	// back when the write does not land.
	failAppend bool
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		chats:    map[string]store.Chat{},
		messages: map[string][]store.ChatMessage{},
		running:  map[string]bool{},
	}
}

func (f *fakeStore) add(id, login string) store.Chat {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := store.Chat{ID: id, Title: store.DefaultChatTitle, Login: login, CreatedAt: time.Now().UTC()}
	f.chats[id] = c
	return c
}

func (f *fakeStore) GetChat(_ context.Context, id string) (store.Chat, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.chats[id]
	if !ok {
		return store.Chat{}, fmt.Errorf("%w: chat %s", store.ErrNotFound, id)
	}
	return c, nil
}

func (f *fakeStore) AppendChatMessage(_ context.Context, msg store.ChatMessage) (store.ChatMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failAppend {
		return store.ChatMessage{}, errors.New("fake store: the append failed")
	}
	msg.Seq = uint64(len(f.messages[msg.ChatID])) + 1
	if msg.TS.IsZero() {
		msg.TS = time.Now().UTC()
	}
	f.messages[msg.ChatID] = append(f.messages[msg.ChatID], msg)
	return msg, nil
}

func (f *fakeStore) ListChatMessages(_ context.Context, chatID string, fromSeq uint64) ([]store.ChatMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []store.ChatMessage
	for _, m := range f.messages[chatID] {
		if m.Seq > fromSeq {
			out = append(out, m)
		}
	}
	return out, nil
}

func (f *fakeStore) AttachToLastAssistantMessage(
	_ context.Context, chatID string, file store.ChatAttachment,
) (store.ChatMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.messages[chatID]) - 1; i >= 0; i-- {
		if f.messages[chatID][i].Role != store.RoleAssistant {
			continue
		}
		f.messages[chatID][i].Attachments = append(f.messages[chatID][i].Attachments, file)
		return f.messages[chatID][i], nil
	}
	return store.ChatMessage{}, fmt.Errorf("%w: chat %s has no message to attach to", store.ErrNotFound, chatID)
}

func (f *fakeStore) ChatTurnRunning(_ context.Context, chatID string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.running[chatID], nil
}

func newSource(t *testing.T, st Store) *Source {
	t.Helper()
	src, err := New(Options{Store: st, DisplayName: "Podium", UIURL: "https://podium.example/"})
	require.NoError(t, err)
	return src
}

// drain reads one event, or fails rather than hanging.
func drainEvent(t *testing.T, src *Source) conductor.InboundEvent {
	t.Helper()
	select {
	case ev := <-src.Events():
		return ev
	case <-time.After(2 * time.Second):
		t.Fatal("no inbound event arrived")
		return conductor.InboundEvent{}
	}
}

func TestSendStoresTheMessageAndStartsATurn(t *testing.T) {
	st := newFakeStore()
	st.add("chat_1", "alice")
	src := newSource(t, st)
	sub := src.Subscribe(context.Background(), "chat_1")
	defer sub.Close()

	msg, err := src.Send(context.Background(), SendRequest{
		ChatID: "chat_1", Login: "alice", Text: "how many active accounts last month", Skill: "analyst",
	})
	require.NoError(t, err)
	assert.Equal(t, uint64(1), msg.Seq)
	assert.Equal(t, store.RoleUser, msg.Role)

	// The human's own message is broadcast before the turn starts, so the bubble appears
	// as soon as it is sent rather than when the answer arrives.
	frame := recv(t, sub)
	assert.Equal(t, FrameMessage, frame.Kind)
	assert.Equal(t, "how many active accounts last month", frame.Message.Text)

	ev := drainEvent(t, src)
	assert.Equal(t, conductor.SourceChat, ev.SourceKind)
	assert.Equal(t, conductor.SourceChat, ev.BriefKind, "the runtime's schema knows chat")
	assert.Equal(t, "chat:chat_1", ev.SourceKey)
	assert.Equal(t, "chat_1", ev.Ref)
	assert.Equal(t, "alice", ev.Author)
	assert.Equal(t, "analyst", ev.Skill, "the skill chip bypasses the profile's routing rules")
	assert.Equal(t, "https://podium.example/agent/chat/chat_1", ev.URL,
		"the deep link becomes a memory's provenance chip")
	assert.Empty(t, ev.Env, "only the dev source asks for task environment")
}

func TestAMessageWithNoSkillRunsTheProfilesChatDefault(t *testing.T) {
	st := newFakeStore()
	st.add("chat_1", "alice")
	src, err := New(Options{Store: st, DisplayName: "Podium", DefaultSkill: "analyst"})
	require.NoError(t, err)

	// This is what makes profile.yaml's chat_default_skill a profile decision rather than a
	// UI hint: a message that names no skill runs it, not the profile's general default.
	_, err = src.Send(context.Background(), SendRequest{ChatID: "chat_1", Login: "alice", Text: "hello"})
	require.NoError(t, err)
	assert.Equal(t, "analyst", drainEvent(t, src).Skill)

	require.NoError(t, src.React(context.Background(), "chat_1", conductor.ReactionDone))
	// And the chip still wins when it names one.
	_, err = src.Send(context.Background(), SendRequest{
		ChatID: "chat_1", Login: "alice", Text: "hello", Skill: "general",
	})
	require.NoError(t, err)
	assert.Equal(t, "general", drainEvent(t, src).Skill)
}

func TestSeqIsMonotonicPerChat(t *testing.T) {
	st := newFakeStore()
	st.add("chat_1", "alice")
	st.add("chat_2", "alice")
	src := newSource(t, st)
	ctx := context.Background()

	for i := range 3 {
		msg, err := src.Send(ctx, SendRequest{ChatID: "chat_1", Login: "alice", Text: "one"})
		require.NoError(t, err)
		assert.Equal(t, uint64(i+1), msg.Seq)
		drainEvent(t, src)
		require.NoError(t, src.React(ctx, "chat_1", conductor.ReactionDone))
	}
	// A second chat has its own seq space.
	msg, err := src.Send(ctx, SendRequest{ChatID: "chat_2", Login: "alice", Text: "one"})
	require.NoError(t, err)
	assert.Equal(t, uint64(1), msg.Seq)
}

func TestAnotherLoginsChatIsNotThere(t *testing.T) {
	st := newFakeStore()
	st.add("chat_1", "alice")
	src := newSource(t, st)

	_, err := src.Send(context.Background(), SendRequest{ChatID: "chat_1", Login: "bob", Text: "hello"})
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrNotFound,
		"the existence of another login's chat is not bob's business")
	// And nothing was written.
	msgs, err := st.ListChatMessages(context.Background(), "chat_1", 0)
	require.NoError(t, err)
	assert.Empty(t, msgs)
}

func TestASecondMessageWhileATurnRunsIsRefused(t *testing.T) {
	st := newFakeStore()
	st.add("chat_1", "alice")
	src := newSource(t, st)
	ctx := context.Background()

	_, err := src.Send(ctx, SendRequest{ChatID: "chat_1", Login: "alice", Text: "first"})
	require.NoError(t, err)

	// The turn row does not exist yet — the conductor has not even read the event — and
	// the second send is still refused. That window is exactly what the in-process claim
	// is for.
	require.False(t, st.running["chat_1"])
	_, err = src.Send(ctx, SendRequest{ChatID: "chat_1", Login: "alice", Text: "second"})
	assert.ErrorIs(t, err, ErrTurnRunning)

	msgs, err := st.ListChatMessages(ctx, "chat_1", 0)
	require.NoError(t, err)
	assert.Len(t, msgs, 1, "a refused send stores nothing")

	// The outcome of the turn gives the slot back.
	require.NoError(t, src.React(ctx, "chat_1", conductor.ReactionDone))
	_, err = src.Send(ctx, SendRequest{ChatID: "chat_1", Login: "alice", Text: "second"})
	require.NoError(t, err)
}

func TestATurnLeftRunningByADeadProcessStillRefusesASend(t *testing.T) {
	st := newFakeStore()
	st.add("chat_1", "alice")
	st.running["chat_1"] = true
	// A fresh process: nothing in memory, and the turns table is the only witness.
	src := newSource(t, st)

	_, err := src.Send(context.Background(), SendRequest{ChatID: "chat_1", Login: "alice", Text: "hello"})
	assert.ErrorIs(t, err, ErrTurnRunning)
}

func TestAFailedWriteGivesTheTurnSlotBack(t *testing.T) {
	st := newFakeStore()
	st.add("chat_1", "alice")
	src := newSource(t, st)
	st.failAppend = true

	_, err := src.Send(context.Background(), SendRequest{ChatID: "chat_1", Login: "alice", Text: "hello"})
	require.Error(t, err)

	st.failAppend = false
	_, err = src.Send(context.Background(), SendRequest{ChatID: "chat_1", Login: "alice", Text: "hello"})
	assert.NoError(t, err, "a chat must not be wedged by a write that failed")
}

func TestConcurrentSendsProduceExactlyOneTurn(t *testing.T) {
	st := newFakeStore()
	st.add("chat_1", "alice")
	src := newSource(t, st)

	const senders = 8
	var wg sync.WaitGroup
	accepted := make(chan struct{}, senders)
	for i := range senders {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := src.Send(context.Background(), SendRequest{
				ChatID: "chat_1", Login: "alice", Text: fmt.Sprintf("message %d", i),
			}); err == nil {
				accepted <- struct{}{}
			}
		}(i)
	}
	wg.Wait()
	assert.Len(t, accepted, 1, "one turn at a time per conversation, whatever the race")
}

func TestProgressIsBroadcastAndNeverStored(t *testing.T) {
	st := newFakeStore()
	st.add("chat_1", "alice")
	src := newSource(t, st)
	sub := src.Subscribe(context.Background(), "chat_1")
	defer sub.Close()
	ctx := context.Background()

	id, err := src.Post(ctx, "chat_1", conductor.Outbound{Type: conductor.OutProgress, Text: "reading the schema"})
	require.NoError(t, err)
	require.NotEmpty(t, id, "the turn loop edits whatever Post returned")
	require.NoError(t, src.Edit(ctx, "chat_1", id, conductor.Outbound{
		Type: conductor.OutProgress, Text: "running the query",
	}))

	assert.Equal(t, "reading the schema", recv(t, sub).Progress)
	assert.Equal(t, "running the query", recv(t, sub).Progress)

	msgs, err := st.ListChatMessages(ctx, "chat_1", 0)
	require.NoError(t, err)
	assert.Empty(t, msgs, "a reload shows the answer, not the trail of thinking")
}

func TestAFinalIsStoredAndBroadcast(t *testing.T) {
	st := newFakeStore()
	st.add("chat_1", "alice")
	src := newSource(t, st)
	sub := src.Subscribe(context.Background(), "chat_1")
	defer sub.Close()
	ctx := context.Background()

	_, err := src.Post(ctx, "chat_1", conductor.Outbound{
		Type: conductor.OutFinal, Text: "4,812 active accounts in August.", TaskID: "task_01",
	})
	require.NoError(t, err)

	frame := recv(t, sub)
	require.Equal(t, FrameMessage, frame.Kind)
	assert.Equal(t, store.RoleAssistant, frame.Message.Role)
	assert.Equal(t, "4,812 active accounts in August.", frame.Message.Text)

	msgs, err := st.ListChatMessages(ctx, "chat_1", 0)
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	assert.Equal(t, "4,812 active accounts in August.", msgs[0].Text)
}

func TestAnAttachmentLandsOnTheFinalAndKeepsItsSeq(t *testing.T) {
	st := newFakeStore()
	st.add("chat_1", "alice")
	src := newSource(t, st)
	sub := src.Subscribe(context.Background(), "chat_1")
	defer sub.Close()
	ctx := context.Background()

	_, err := src.Post(ctx, "chat_1", conductor.Outbound{Type: conductor.OutFinal, Text: "See report.csv."})
	require.NoError(t, err)
	final := recv(t, sub)

	require.NoError(t, src.Attach(ctx, "chat_1", conductor.Attachment{
		Name: "report.csv", ContentType: "text/csv", Size: 1024,
		ArtifactID: "art_01", TaskID: "task_01",
	}))
	updated := recv(t, sub)
	require.Equal(t, FrameMessage, updated.Kind)
	assert.Equal(t, final.Message.Seq, updated.Message.Seq,
		"the same message, republished: a client keyed on seq replaces rather than appends")
	require.Len(t, updated.Message.Attachments, 1)
	assert.Equal(t, "art_01", updated.Message.Attachments[0].ArtifactID)
	assert.Equal(t, "report.csv", updated.Message.Attachments[0].Name)
}

func TestAnAttachmentWithNoArtifactIDIsRefused(t *testing.T) {
	st := newFakeStore()
	st.add("chat_1", "alice")
	src := newSource(t, st)
	_, err := src.Post(context.Background(), "chat_1", conductor.Outbound{
		Type: conductor.OutFinal, Text: "See report.csv.",
	})
	require.NoError(t, err)

	// The browser downloads GET /artifacts/{id}. A name alone is not a thing it can fetch.
	err = src.Attach(context.Background(), "chat_1", conductor.Attachment{Name: "report.csv"})
	assert.ErrorContains(t, err, "no artifact id")
}

func TestAFailurePostIsStoredSoItSurvivesAReload(t *testing.T) {
	st := newFakeStore()
	st.add("chat_1", "alice")
	src := newSource(t, st)
	ctx := context.Background()

	_, err := src.Send(ctx, SendRequest{ChatID: "chat_1", Login: "alice", Text: "hello"})
	require.NoError(t, err)
	drainEvent(t, src)
	_, err = src.Post(ctx, "chat_1", conductor.Outbound{
		Type: conductor.OutFailure, Text: "Something went wrong on my side. Task `task_01`.",
	})
	require.NoError(t, err)
	require.NoError(t, src.React(ctx, "chat_1", conductor.ReactionFailed))

	msgs, err := st.ListChatMessages(ctx, "chat_1", 0)
	require.NoError(t, err)
	require.Len(t, msgs, 2, "the human's question and the conductor's own words, and nothing invented")
	assert.Equal(t, "Something went wrong on my side. Task `task_01`.", msgs[1].Text)
}

func TestAFailureThatSaidNothingStillLeavesSomethingBehind(t *testing.T) {
	st := newFakeStore()
	st.add("chat_1", "alice")
	src := newSource(t, st)
	ctx := context.Background()

	_, err := src.Send(ctx, SendRequest{ChatID: "chat_1", Login: "alice", Text: "hello"})
	require.NoError(t, err)
	drainEvent(t, src)
	require.NoError(t, src.React(ctx, "chat_1", conductor.ReactionWorking))
	require.NoError(t, src.React(ctx, "chat_1", conductor.ReactionFailed))

	msgs, err := st.ListChatMessages(ctx, "chat_1", 0)
	require.NoError(t, err)
	require.Len(t, msgs, 2)
	assert.Contains(t, msgs[1].Text, "could not complete this turn",
		"a reload must not look like the question was ignored")
}

func TestReactionsBecomeStatusFrames(t *testing.T) {
	st := newFakeStore()
	st.add("chat_1", "alice")
	src := newSource(t, st)
	sub := src.Subscribe(context.Background(), "chat_1")
	defer sub.Close()
	ctx := context.Background()

	require.NoError(t, src.React(ctx, "chat_1", conductor.ReactionWorking))
	require.NoError(t, src.React(ctx, "chat_1", conductor.ReactionDone))
	assert.Equal(t, StatusStarted, recv(t, sub).State)
	assert.Equal(t, StatusFinished, recv(t, sub).State)

	assert.ErrorContains(t, src.React(ctx, "chat_1", conductor.Reaction("shrug")), "unknown reaction")
}

func TestTheTranscriptIsTheWholeConversation(t *testing.T) {
	st := newFakeStore()
	st.add("chat_1", "alice")
	src := newSource(t, st)
	ctx := context.Background()

	_, err := src.Send(ctx, SendRequest{ChatID: "chat_1", Login: "alice", Text: "first question"})
	require.NoError(t, err)
	drainEvent(t, src)
	_, err = src.Post(ctx, "chat_1", conductor.Outbound{Type: conductor.OutFinal, Text: "first answer"})
	require.NoError(t, err)
	// Progress is not in the history: it was never stored.
	_, err = src.Post(ctx, "chat_1", conductor.Outbound{Type: conductor.OutProgress, Text: "thinking"})
	require.NoError(t, err)

	entries, err := src.FetchTranscript(ctx, "chat_1")
	require.NoError(t, err)
	require.Len(t, entries, 2)
	assert.Equal(t, conductor.RoleUser, entries[0].Role)
	assert.Equal(t, "alice", entries[0].Author)
	assert.Equal(t, "first question", entries[0].Text)
	assert.Equal(t, conductor.RoleAssistant, entries[1].Role)
	assert.Equal(t, "Podium", entries[1].Author, "the bot's own entries carry its display name")
	assert.Equal(t, "first answer", entries[1].Text)
	assert.NotEmpty(t, entries[0].TS)
}

func TestTheTranscriptOfAChatThatIsNotThere(t *testing.T) {
	src := newSource(t, newFakeStore())
	_, err := src.FetchTranscript(context.Background(), "chat_nope")
	assert.ErrorIs(t, err, store.ErrNotFound)
}

func TestAnEmptyMessageIsRefused(t *testing.T) {
	st := newFakeStore()
	st.add("chat_1", "alice")
	src := newSource(t, st)
	_, err := src.Send(context.Background(), SendRequest{ChatID: "chat_1", Login: "alice", Text: "   \n "})
	assert.ErrorContains(t, err, "some text")
	_, err = src.Send(context.Background(), SendRequest{Login: "alice", Text: "hello"})
	assert.ErrorContains(t, err, "chat id is required")
}

func TestWithNoUIURLThereIsNoDeepLink(t *testing.T) {
	st := newFakeStore()
	st.add("chat_1", "alice")
	src, err := New(Options{Store: st, DisplayName: "Podium"})
	require.NoError(t, err)
	assert.Empty(t, src.URL("chat_1"), "a link to nowhere is worse than none")
}

func TestASourceNeedsAStore(t *testing.T) {
	_, err := New(Options{})
	assert.ErrorContains(t, err, "a store is required")
}
