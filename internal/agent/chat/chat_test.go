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

	"github.com/podium-ade/podium/internal/agent/conductor"
	"github.com/podium-ade/podium/internal/agent/profiles"
	"github.com/podium-ade/podium/internal/agent/store"
)

// fakeStore is the two chat tables in memory. It keeps the one behaviour the SQL is relied
// on for — a per-chat monotonic seq — and nothing else.
type fakeStore struct {
	mu       sync.Mutex
	chats    map[string]store.Chat
	messages map[string][]store.ChatMessage
	running  map[string]bool
	// pulls is chat_pull_requests, keyed the way its primary key is, with pullOrder
	// keeping the insertion order the query reads them back in.
	pulls     map[string]*fakePull
	pullOrder []string
	// failAppend makes the next AppendChatMessage fail, to prove the turn slot is given
	// back when the write does not land.
	failAppend bool
}

// fakePull is one row of chat_pull_requests, tombstone and all.
type fakePull struct {
	pr       store.ChatPullRequest
	detached bool
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
	c := store.Chat{ID: id, Title: store.DefaultChatTitle, Login: login, CreatedAt: time.Now().UTC(), AutoTitle: true}
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

func (f *fakeStore) SetChatChoice(
	_ context.Context, id string, c store.ChatChoice,
) (store.Chat, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	chat, ok := f.chats[id]
	if !ok {
		return store.Chat{}, fmt.Errorf("%w: chat %s", store.ErrNotFound, id)
	}
	// Whatever it is given, empty included: switching back to the default is a choice.
	chat.ChatChoice = c
	f.chats[id] = chat
	return chat, nil
}

func (f *fakeStore) SetChatTitle(_ context.Context, id, title string) (store.Chat, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.chats[id]
	if !ok {
		return store.Chat{}, fmt.Errorf("%w: chat %s", store.ErrNotFound, id)
	}
	if c.AutoTitle {
		c.Title = title
		f.chats[id] = c
	}
	return c, nil
}

// The pull-request half of the fake keeps the two behaviours the SQL is relied on for: one
// row per (chat, url), and a detach that tombstones rather than deletes.
func (f *fakeStore) key(chatID, url string) string { return chatID + "\x00" + url }

func (f *fakeStore) LinkChatPullRequest(_ context.Context, pr store.ChatPullRequest) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.pulls == nil {
		f.pulls = map[string]*fakePull{}
	}
	if _, ok := f.pulls[f.key(pr.ChatID, pr.URL)]; ok {
		return false, nil
	}
	pr.Source = store.PullRequestFromTurn
	pr.CreatedAt = time.Now().UTC()
	f.pulls[f.key(pr.ChatID, pr.URL)] = &fakePull{pr: pr}
	f.pullOrder = append(f.pullOrder, f.key(pr.ChatID, pr.URL))
	return true, nil
}

func (f *fakeStore) AttachChatPullRequest(
	_ context.Context, pr store.ChatPullRequest,
) (store.ChatPullRequest, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.pulls == nil {
		f.pulls = map[string]*fakePull{}
	}
	pr.Source = store.PullRequestFromHuman
	if have, ok := f.pulls[f.key(pr.ChatID, pr.URL)]; ok {
		have.detached = false
		have.pr.Source = store.PullRequestFromHuman
		return have.pr, nil
	}
	pr.CreatedAt = time.Now().UTC()
	f.pulls[f.key(pr.ChatID, pr.URL)] = &fakePull{pr: pr}
	f.pullOrder = append(f.pullOrder, f.key(pr.ChatID, pr.URL))
	return pr, nil
}

func (f *fakeStore) DetachChatPullRequest(_ context.Context, chatID, url string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	have, ok := f.pulls[f.key(chatID, url)]
	if !ok || have.detached {
		return fmt.Errorf("%w: chat %s has no link to %s", store.ErrNotFound, chatID, url)
	}
	have.detached = true
	return nil
}

func (f *fakeStore) ListChatPullRequests(_ context.Context, chatID string) ([]store.ChatPullRequest, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []store.ChatPullRequest{}
	for _, k := range f.pullOrder {
		have := f.pulls[k]
		if have.detached || have.pr.ChatID != chatID {
			continue
		}
		out = append(out, have.pr)
	}
	return out, nil
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
		ChatID: "chat_1", Login: "alice", Text: "how many active accounts last month",
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
	assert.Empty(t, ev.Playbook,
		"a chat names no playbook: the assistant answers it and delegates the work")
	assert.Equal(t, "https://podium.example/agent/chat/chat_1", ev.URL,
		"the deep link becomes a memory's provenance chip")
	assert.Empty(t, ev.Env, "only the dev source asks for task environment")
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

func TestProgressIsStoredAsTheTaskTalking(t *testing.T) {
	st := newFakeStore()
	st.add("chat_1", "alice")
	src := newSource(t, st)
	sub := src.Subscribe(context.Background(), "chat_1")
	defer sub.Close()
	ctx := context.Background()

	id, err := src.Post(ctx, "chat_1", conductor.Outbound{
		Type: conductor.OutProgress, Text: conductor.ProgressPrefix + "reading the schema",
	})
	require.NoError(t, err)
	require.NotEmpty(t, id, "the turn loop edits whatever Post returned")
	// An edit appends: a chat has no placeholder to replace.
	require.NoError(t, src.Edit(ctx, "chat_1", id, conductor.Outbound{
		Type: conductor.OutProgress, Text: conductor.ProgressPrefix + "running the query",
	}))

	first := recv(t, sub)
	require.Equal(t, FrameMessage, first.Kind)
	assert.Equal(t, store.RoleProgress, first.Message.Role)
	assert.Equal(t, "reading the schema", first.Message.Text, "the prefix is Slack's, not the chat's")
	assert.Equal(t, "running the query", recv(t, sub).Message.Text)

	msgs, err := st.ListChatMessages(ctx, "chat_1", 0)
	require.NoError(t, err)
	require.Len(t, msgs, 2, "a reload shows the trail, because the task said it")
	assert.Equal(t, uint64(1), msgs[0].Seq)
	assert.Equal(t, uint64(2), msgs[1].Seq)
}

func TestThePlaceholderIsNotAMessage(t *testing.T) {
	st := newFakeStore()
	st.add("chat_1", "alice")
	src := newSource(t, st)
	sub := src.Subscribe(context.Background(), "chat_1")
	defer sub.Close()
	ctx := context.Background()

	_, err := src.Post(ctx, "chat_1", conductor.Outbound{
		Type: conductor.OutProgress, Text: conductor.Placeholder, TaskID: "task_01",
	})
	require.NoError(t, err)

	frame := recv(t, sub)
	require.Equal(t, FrameProgress, frame.Kind, "the running indicator's line, not the transcript's")
	assert.Equal(t, conductor.Placeholder, frame.Progress)
	assert.Equal(t, "task_01", frame.TaskID)

	msgs, err := st.ListChatMessages(ctx, "chat_1", 0)
	require.NoError(t, err)
	assert.Empty(t, msgs, "nobody said it")
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
	// Progress is stored, and still not in the history: a turn's own half-finished thoughts
	// are not what the next one needs to read.
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

func TestTheFirstMessageNamesTheChat(t *testing.T) {
	st := newFakeStore()
	st.add("chat_1", "alice")
	src := newSource(t, st)
	sub := src.Subscribe(context.Background(), "chat_1")
	defer sub.Close()

	_, err := src.Send(context.Background(), SendRequest{
		ChatID: "chat_1", Login: "alice", Text: "how many active accounts last month",
	})
	require.NoError(t, err)
	drainEvent(t, src)
	_ = recv(t, sub) // the user message
	meta := recv(t, sub)
	assert.Equal(t, FrameChat, meta.Kind)
	assert.Equal(t, "how many active accounts last month", meta.Chat.Title)

	got, err := st.GetChat(context.Background(), "chat_1")
	require.NoError(t, err)
	assert.Equal(t, "how many active accounts last month", got.Title)

	require.NoError(t, src.React(context.Background(), "chat_1", conductor.ReactionDone))
	_, err = src.Send(context.Background(), SendRequest{
		ChatID: "chat_1", Login: "alice", Text: "something else",
	})
	require.NoError(t, err)
	got, err = st.GetChat(context.Background(), "chat_1")
	require.NoError(t, err)
	assert.Equal(t, "how many active accounts last month", got.Title, "the title is not rewritten on later messages")
}

func TestASuppliedTitleIsNotOverwritten(t *testing.T) {
	st := newFakeStore()
	st.mu.Lock()
	st.chats["chat_1"] = store.Chat{
		ID: "chat_1", Title: "August numbers", Login: "alice", CreatedAt: time.Now().UTC(),
	}
	st.mu.Unlock()
	src := newSource(t, st)

	_, err := src.Send(context.Background(), SendRequest{
		ChatID: "chat_1", Login: "alice", Text: "how many active accounts",
	})
	require.NoError(t, err)
	got, err := st.GetChat(context.Background(), "chat_1")
	require.NoError(t, err)
	assert.Equal(t, "August numbers", got.Title)
}

func TestSetAutoTitleReplacesAQueryTitle(t *testing.T) {
	st := newFakeStore()
	st.add("chat_1", "alice")
	src := newSource(t, st)
	_, err := src.Send(context.Background(), SendRequest{
		ChatID: "chat_1", Login: "alice", Text: "how many active accounts last month",
	})
	require.NoError(t, err)

	require.NoError(t, src.SetAutoTitle(context.Background(), "chat_1", "August account totals"))
	got, err := st.GetChat(context.Background(), "chat_1")
	require.NoError(t, err)
	assert.Equal(t, "August account totals", got.Title)
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

// TestEveryRowRecordsWhoSaidIt. A conversation carries three kinds of line — the assistant
// thinking on this host, the conductor announcing a delegation, and a delegated task's own
// words — and they are all stored under the same two roles. The task id is the only thing
// that tells them apart, and dropping it here is what made the UI credit the assistant's own
// thinking to a container it had not started yet.
func TestEveryRowRecordsWhoSaidIt(t *testing.T) {
	st := newFakeStore()
	st.add("chat_1", "alice")
	src := newSource(t, st)
	ctx := context.Background()

	// The assistant, in the conductor's own process: no task.
	require.NoError(t, src.Edit(ctx, "chat_1", "", conductor.Outbound{
		Type: conductor.OutProgress, Text: conductor.ProgressPrefix + "delegating this",
	}))
	// The conductor announcing a delegation, and then the task talking.
	require.NoError(t, src.Edit(ctx, "chat_1", "", conductor.Outbound{
		Type: conductor.OutProgress, Text: "Working on this in a `podium` task", TaskID: "task_01",
	}))
	_, err := src.Post(ctx, "chat_1", conductor.Outbound{
		Type: conductor.OutFinal, Text: "done", TaskID: "task_01",
	})
	require.NoError(t, err)

	msgs, err := st.ListChatMessages(ctx, "chat_1", 0)
	require.NoError(t, err)
	require.Len(t, msgs, 3)
	assert.Empty(t, msgs[0].TaskID, "the assistant's own words are not a task's")
	assert.Equal(t, "task_01", msgs[1].TaskID)
	assert.Equal(t, "task_01", msgs[2].TaskID, "an answer relayed from a task says which one")
}

// TestAChatRemembersWhatItIsAnsweredOn. The point is that a person picks a model once. It is
// the OVERRIDE that is stored, so a conversation that asked for nothing keeps following
// profile.yaml — and switching back to the default is itself a choice, expressed by sending
// nothing and recorded by clearing the row.
func TestAChatRemembersWhatItIsAnsweredOn(t *testing.T) {
	st := newFakeStore()
	st.add("chat_1", "alice")
	src := newSource(t, st)
	ctx := context.Background()

	_, err := src.Send(ctx, SendRequest{
		ChatID: "chat_1", Login: "alice", Text: "on grok please",
		Override: profiles.Override{Agent: "grok", Model: "grok-4.6", Effort: "high"},
	})
	require.NoError(t, err)
	drainEvent(t, src)

	got, err := st.GetChat(ctx, "chat_1")
	require.NoError(t, err)
	assert.Equal(t, store.ChatChoice{Agent: "grok", Model: "grok-4.6", Effort: "high"}, got.ChatChoice)

	// Back to the assistant's own, which is a decision and not an absence of one.
	require.NoError(t, src.React(ctx, "chat_1", conductor.ReactionDone))
	_, err = src.Send(ctx, SendRequest{ChatID: "chat_1", Login: "alice", Text: "default is fine"})
	require.NoError(t, err)
	drainEvent(t, src)

	got, err = st.GetChat(ctx, "chat_1")
	require.NoError(t, err)
	assert.Equal(t, store.ChatChoice{}, got.ChatChoice, "clearing the override is remembered too")
}
