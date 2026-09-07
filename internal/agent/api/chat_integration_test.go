//go:build integration

package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alvaroibarguen/podium/internal/agent/chat"
	"github.com/alvaroibarguen/podium/internal/agent/conductor"
	"github.com/alvaroibarguen/podium/internal/agent/profiles"
	"github.com/alvaroibarguen/podium/internal/agent/store"
	agentv1 "github.com/alvaroibarguen/podium/internal/proto/podium/agent/v1"
	"github.com/alvaroibarguen/podium/internal/proto/podium/agent/v1/agentv1connect"
)

// chatToken is the bearer podium-server would present. A test fixture, not a secret.
const chatToken = "chattoken-integration"

// chatFixture is the conductor's chat surface as a browser reaches it: a real store, a
// real chat source, the real handlers behind the real bearer, over a real listener. Only
// the relay is faked — the turn loop is the conductor's own test's business.
type chatFixture struct {
	store  *store.Store
	source *chat.Source
	client agentv1connect.AgentServiceClient
	url    string
}

func newChatFixture(t *testing.T) chatFixture {
	return newChatFixtureWith(t, AgentServiceOptions{})
}

func newChatFixtureWith(t *testing.T, opts AgentServiceOptions) chatFixture {
	t.Helper()
	st := newStore(t)
	src, err := chat.New(chat.Options{
		Store: st, DisplayName: "Podium", UIURL: "https://podium.example",
	})
	require.NoError(t, err)

	// Nothing drains the source's events in this test, so the buffer would fill after 32
	// sends. Draining keeps the source honest about the turn slot without pulling the
	// whole turn loop in.
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-src.Events():
			}
		}
	}()

	opts.Store = st
	opts.Chat = src
	if opts.Profiles == nil {
		opts.Profiles = profiles.NewLive(&profiles.Profile{DisplayName: "Podium", DefaultPlaybook: "general", Playbooks: map[string]profiles.Playbook{
			"general": {Name: "general", Image: "podium-agent-runtime:dev", SystemPrompt: "Answer."},
		}})
	}
	svc := NewAgentService(opts)
	path, handler := agentv1connect.NewAgentServiceHandler(svc)
	mux := http.NewServeMux()
	mux.Handle(path, RequireBearer(chatToken, handler))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	return chatFixture{
		store:  st,
		source: src,
		url:    srv.URL,
		client: agentv1connect.NewAgentServiceClient(srv.Client(), srv.URL,
			connect.WithInterceptors(proxyHeaders("alice"))),
	}
}

// proxyHeaders is what podium-server's reverse proxy adds on every hop: its own bearer and
// its word about who is calling.
func proxyHeaders(login string) connect.Interceptor {
	return connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			req.Header().Set("Authorization", "Bearer "+chatToken)
			req.Header().Set(LoginHeader, login)
			return next(ctx, req)
		}
	})
}

// clientAs is a second client presenting a different login, which is how the two-logins
// isolation is exercised without two identities in the transport.
func (f chatFixture) clientAs(login string) agentv1connect.AgentServiceClient {
	return agentv1connect.NewAgentServiceClient(http.DefaultClient, f.url,
		connect.WithInterceptors(proxyHeaders(login)))
}

// streamAs opens StreamChat as one login. The interceptor above is unary-only, so a
// streaming call sets the headers itself.
func (f chatFixture) streamAs(
	ctx context.Context, t *testing.T, login, chatID string, fromSeq uint64,
) *connect.ServerStreamForClient[agentv1.ChatFrame] {
	t.Helper()
	client := agentv1connect.NewAgentServiceClient(http.DefaultClient, f.url)
	req := connect.NewRequest(&agentv1.StreamChatRequest{ChatId: chatID, FromSeq: fromSeq})
	req.Header().Set("Authorization", "Bearer "+chatToken)
	req.Header().Set(LoginHeader, login)
	stream, err := client.StreamChat(ctx, req)
	require.NoError(t, err)
	return stream
}

// nextFrame reads one frame with a deadline, so a hung stream fails the test rather than
// the suite.
func nextFrame(t *testing.T, stream *connect.ServerStreamForClient[agentv1.ChatFrame]) *agentv1.ChatFrame {
	t.Helper()
	type result struct {
		frame *agentv1.ChatFrame
		ok    bool
	}
	out := make(chan result, 1)
	go func() {
		ok := stream.Receive()
		out <- result{frame: stream.Msg(), ok: ok}
	}()
	select {
	case r := <-out:
		require.True(t, r.ok, "the stream ended: %v", stream.Err())
		return r.frame
	case <-time.After(20 * time.Second):
		t.Fatal("no frame arrived on the chat stream")
		return nil
	}
}

func TestAChatRemembersItsPlaybookAndGetsAName(t *testing.T) {
	f := newChatFixture(t)
	ctx := context.Background()

	created, err := f.client.CreateChat(ctx, connect.NewRequest(&agentv1.CreateChatRequest{}))
	require.NoError(t, err)
	assert.Equal(t, store.DefaultChatTitle, created.Msg.GetChat().GetTitle())
	assert.Empty(t, created.Msg.GetChat().GetPlaybook())

	_, err = f.client.SendChatMessage(ctx, connect.NewRequest(&agentv1.SendChatMessageRequest{
		ChatId: created.Msg.GetChat().GetId(), Text: "how many active accounts last month", Playbook: "analyst",
	}))
	require.NoError(t, err)

	listed, err := f.client.ListChats(ctx, connect.NewRequest(&agentv1.ListChatsRequest{}))
	require.NoError(t, err)
	require.Len(t, listed.Msg.GetChats(), 1)
	assert.Equal(t, "analyst", listed.Msg.GetChats()[0].GetPlaybook())
	assert.Equal(t, "how many active accounts last month", listed.Msg.GetChats()[0].GetTitle())
}

func TestCreateAndListChatsThroughTheService(t *testing.T) {
	f := newChatFixture(t)
	ctx := context.Background()

	created, err := f.client.CreateChat(ctx, connect.NewRequest(&agentv1.CreateChatRequest{}))
	require.NoError(t, err)
	assert.Equal(t, store.DefaultChatTitle, created.Msg.GetChat().GetTitle())
	assert.False(t, created.Msg.GetChat().GetTurnRunning())

	listed, err := f.client.ListChats(ctx, connect.NewRequest(&agentv1.ListChatsRequest{}))
	require.NoError(t, err)
	require.Len(t, listed.Msg.GetChats(), 1)
	assert.Equal(t, created.Msg.GetChat().GetId(), listed.Msg.GetChats()[0].GetId())
	assert.Nil(t, listed.Msg.GetChats()[0].GetLastMessageAt())
}

func TestTwoLoginsSeeDisjointChatsThroughTheService(t *testing.T) {
	f := newChatFixture(t)
	ctx := context.Background()
	bob := f.clientAs("bob")

	alice, err := f.client.CreateChat(ctx, connect.NewRequest(&agentv1.CreateChatRequest{Title: "alice's"}))
	require.NoError(t, err)
	bobs, err := bob.CreateChat(ctx, connect.NewRequest(&agentv1.CreateChatRequest{Title: "bob's"}))
	require.NoError(t, err)

	aliceList, err := f.client.ListChats(ctx, connect.NewRequest(&agentv1.ListChatsRequest{}))
	require.NoError(t, err)
	require.Len(t, aliceList.Msg.GetChats(), 1)
	assert.Equal(t, "alice's", aliceList.Msg.GetChats()[0].GetTitle())

	bobList, err := bob.ListChats(ctx, connect.NewRequest(&agentv1.ListChatsRequest{}))
	require.NoError(t, err)
	require.Len(t, bobList.Msg.GetChats(), 1)
	assert.Equal(t, "bob's", bobList.Msg.GetChats()[0].GetTitle())

	// Knowing the id is not access: bob can neither send to nor watch alice's chat, and
	// what he is told is that it does not exist.
	_, err = bob.SendChatMessage(ctx, connect.NewRequest(&agentv1.SendChatMessageRequest{
		ChatId: alice.Msg.GetChat().GetId(), Text: "hello",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))

	streamCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	stream := f.streamAs(streamCtx, t, "bob", alice.Msg.GetChat().GetId(), 0)
	assert.False(t, stream.Receive())
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(stream.Err()))

	assert.NotEqual(t, alice.Msg.GetChat().GetId(), bobs.Msg.GetChat().GetId())
}

func TestDeleteChatThroughTheService(t *testing.T) {
	f := newChatFixture(t)
	ctx := context.Background()
	bob := f.clientAs("bob")

	alice, err := f.client.CreateChat(ctx, connect.NewRequest(&agentv1.CreateChatRequest{Title: "alice's"}))
	require.NoError(t, err)
	chatID := alice.Msg.GetChat().GetId()
	_, err = f.store.AppendChatMessage(ctx, store.ChatMessage{
		ChatID: chatID, Role: store.RoleUser, Text: "hello",
	})
	require.NoError(t, err)
	bobs, err := bob.CreateChat(ctx, connect.NewRequest(&agentv1.CreateChatRequest{Title: "bob's"}))
	require.NoError(t, err)

	// Knowing the id is not access: bob cannot delete alice's chat, and what he is told
	// is that it does not exist.
	_, err = bob.DeleteChat(ctx, connect.NewRequest(&agentv1.DeleteChatRequest{ChatId: chatID}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	_, err = f.store.GetChat(ctx, chatID)
	require.NoError(t, err)

	_, err = f.client.DeleteChat(ctx, connect.NewRequest(&agentv1.DeleteChatRequest{ChatId: chatID}))
	require.NoError(t, err)

	listed, err := f.client.ListChats(ctx, connect.NewRequest(&agentv1.ListChatsRequest{}))
	require.NoError(t, err)
	assert.Empty(t, listed.Msg.GetChats())
	msgs, err := f.store.ListChatMessages(ctx, chatID, 0)
	require.NoError(t, err)
	assert.Empty(t, msgs)

	bobList, err := bob.ListChats(ctx, connect.NewRequest(&agentv1.ListChatsRequest{}))
	require.NoError(t, err)
	require.Len(t, bobList.Msg.GetChats(), 1)
	assert.Equal(t, bobs.Msg.GetChat().GetId(), bobList.Msg.GetChats()[0].GetId())

	_, err = f.client.DeleteChat(ctx, connect.NewRequest(&agentv1.DeleteChatRequest{ChatId: chatID}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))

	_, err = f.client.DeleteChat(ctx, connect.NewRequest(&agentv1.DeleteChatRequest{}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

// fakeTasks is CancelTask as DeleteChat sees it: record the call, optionally fail it.
type fakeTasks struct {
	mu    sync.Mutex
	calls []cancelCall
	err   error
}

type cancelCall struct {
	taskID, reason string
}

func (f *fakeTasks) CancelTask(_ context.Context, taskID, reason string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, cancelCall{taskID: taskID, reason: reason})
	return f.err
}

func (f *fakeTasks) seen() []cancelCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]cancelCall(nil), f.calls...)
}

func TestDeleteChatStopsARunningTask(t *testing.T) {
	tasks := &fakeTasks{}
	f := newChatFixtureWith(t, AgentServiceOptions{Tasks: tasks})
	ctx := context.Background()

	created, err := f.client.CreateChat(ctx, connect.NewRequest(&agentv1.CreateChatRequest{Title: "August numbers"}))
	require.NoError(t, err)
	chatID := created.Msg.GetChat().GetId()
	startRunningChatTask(t, f.store, chatID, "task_01xyz")

	_, err = f.client.DeleteChat(ctx, connect.NewRequest(&agentv1.DeleteChatRequest{ChatId: chatID}))
	require.NoError(t, err)

	calls := tasks.seen()
	require.Len(t, calls, 1)
	assert.Equal(t, "task_01xyz", calls[0].taskID)
	assert.Contains(t, calls[0].reason, "deleted")
	_, err = f.store.GetChat(ctx, chatID)
	assert.ErrorIs(t, err, store.ErrNotFound)
}

func TestDeleteChatLeavesTheChatWhenCancelFails(t *testing.T) {
	tasks := &fakeTasks{err: errors.New("control plane is down")}
	f := newChatFixtureWith(t, AgentServiceOptions{Tasks: tasks})
	ctx := context.Background()

	created, err := f.client.CreateChat(ctx, connect.NewRequest(&agentv1.CreateChatRequest{Title: "August numbers"}))
	require.NoError(t, err)
	chatID := created.Msg.GetChat().GetId()
	startRunningChatTask(t, f.store, chatID, "task_01xyz")

	_, err = f.client.DeleteChat(ctx, connect.NewRequest(&agentv1.DeleteChatRequest{ChatId: chatID}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(err))
	_, err = f.store.GetChat(ctx, chatID)
	require.NoError(t, err, "a cancel that failed must leave the chat so the operator can retry")
}

func TestDeleteChatProceedsWhenTheTaskIsAlreadyTerminal(t *testing.T) {
	tasks := &fakeTasks{err: connect.NewError(connect.CodeFailedPrecondition, errors.New("already cancelled"))}
	f := newChatFixtureWith(t, AgentServiceOptions{Tasks: tasks})
	ctx := context.Background()

	created, err := f.client.CreateChat(ctx, connect.NewRequest(&agentv1.CreateChatRequest{Title: "August numbers"}))
	require.NoError(t, err)
	chatID := created.Msg.GetChat().GetId()
	startRunningChatTask(t, f.store, chatID, "task_01xyz")

	_, err = f.client.DeleteChat(ctx, connect.NewRequest(&agentv1.DeleteChatRequest{ChatId: chatID}))
	require.NoError(t, err)
	_, err = f.store.GetChat(ctx, chatID)
	assert.ErrorIs(t, err, store.ErrNotFound)
}

func TestDeleteChatDoesNotCancelAnotherLogin(t *testing.T) {
	tasks := &fakeTasks{}
	f := newChatFixtureWith(t, AgentServiceOptions{Tasks: tasks})
	ctx := context.Background()

	created, err := f.client.CreateChat(ctx, connect.NewRequest(&agentv1.CreateChatRequest{Title: "alice's"}))
	require.NoError(t, err)
	chatID := created.Msg.GetChat().GetId()
	startRunningChatTask(t, f.store, chatID, "task_01xyz")

	bob := f.clientAs("bob")
	_, err = bob.DeleteChat(ctx, connect.NewRequest(&agentv1.DeleteChatRequest{ChatId: chatID}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	assert.Empty(t, tasks.seen(), "bob must not be able to stop alice's task by knowing the chat id")
	_, err = f.store.GetChat(ctx, chatID)
	require.NoError(t, err)
}

// startRunningChatTask is the conductor's bookkeeping for a turn in flight: a session, a
// running turn, and the task id CreateTask already returned.
func startRunningChatTask(t *testing.T, s *store.Store, chatID, taskID string) {
	t.Helper()
	ctx := context.Background()
	sess, err := s.UpsertSession(ctx, store.Session{
		SourceKind: "chat", SourceKey: store.ChatSourceKey(chatID), Profile: "podium", Playbook: "general",
	})
	require.NoError(t, err)
	turn, err := s.CreateTurn(ctx, sess.ID, chatID)
	require.NoError(t, err)
	require.NoError(t, s.SetTurnTask(ctx, turn.ID, taskID))
}

func TestARequestWithNoLoginIsRefused(t *testing.T) {
	f := newChatFixture(t)
	// The bearer alone reaches a conductor directly rather than through podium-server's
	// proxy, and a chat has an owner or it does not exist.
	client := agentv1connect.NewAgentServiceClient(http.DefaultClient, f.url,
		connect.WithInterceptors(connect.UnaryInterceptorFunc(
			func(next connect.UnaryFunc) connect.UnaryFunc {
				return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
					req.Header().Set("Authorization", "Bearer "+chatToken)
					return next(ctx, req)
				}
			})))
	_, err := client.CreateChat(context.Background(), connect.NewRequest(&agentv1.CreateChatRequest{}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}

// TestStreamChatReplaysThenFollows is the whole streaming contract in one test: what is
// stored is replayed, what happens next arrives live, progress is ephemeral and an
// attachment republishes the message it belongs to.
func TestStreamChatReplaysThenFollows(t *testing.T) {
	f := newChatFixture(t)
	ctx := context.Background()

	created, err := f.client.CreateChat(ctx, connect.NewRequest(&agentv1.CreateChatRequest{}))
	require.NoError(t, err)
	chatID := created.Msg.GetChat().GetId()

	// Two stored messages before anybody is watching.
	_, err = f.store.AppendChatMessage(ctx, store.ChatMessage{
		ChatID: chatID, Role: store.RoleUser, Text: "how many active accounts last month",
	})
	require.NoError(t, err)
	_, err = f.store.AppendChatMessage(ctx, store.ChatMessage{
		ChatID: chatID, Role: store.RoleAssistant, Text: "4,812 in August.",
	})
	require.NoError(t, err)

	streamCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	stream := f.streamAs(streamCtx, t, "alice", chatID, 0)

	// The turn state always comes first, which is what puts the response headers on the
	// wire before anything else has happened.
	opening := nextFrame(t, stream).GetStatus()
	require.NotNil(t, opening, "the first frame is always the turn state")
	assert.Equal(t, chat.StatusFinished, opening.GetState())
	require.NotNil(t, nextFrame(t, stream).GetChat(), "the chat row follows, so the composer knows the playbook")
	require.NotNil(t, nextFrame(t, stream).GetPullRequests(),
		"then the pull requests, before the transcript they would otherwise be buried in")

	// Replay, in seq order.
	first := nextFrame(t, stream).GetMessage()
	require.NotNil(t, first)
	assert.Equal(t, uint64(1), first.GetSeq())
	assert.Equal(t, store.RoleUser, first.GetRole())
	second := nextFrame(t, stream).GetMessage()
	require.NotNil(t, second)
	assert.Equal(t, uint64(2), second.GetSeq())
	assert.Equal(t, "4,812 in August.", second.GetText())

	// Then live: a human message reaches the stream the moment it is sent.
	sent, err := f.client.SendChatMessage(ctx, connect.NewRequest(&agentv1.SendChatMessageRequest{
		ChatId: chatID, Text: "chart it", Playbook: "general",
	}))
	require.NoError(t, err)
	assert.Equal(t, uint64(3), sent.Msg.GetMessage().GetSeq())

	live := nextFrame(t, stream).GetMessage()
	require.NotNil(t, live)
	assert.Equal(t, uint64(3), live.GetSeq())
	assert.Equal(t, "chart it", live.GetText())
	named := nextFrame(t, stream).GetChat()
	require.NotNil(t, named)
	assert.Equal(t, "general", named.GetPlaybook())
	assert.Equal(t, "chart it", named.GetTitle())

	// A turn was started by that send, so the stream is told the composer is busy.
	require.NoError(t, f.source.React(ctx, chatID, conductor.ReactionWorking))
	status := nextFrame(t, stream).GetStatus()
	require.NotNil(t, status)
	assert.Equal(t, chat.StatusStarted, status.GetState())

	// The relay's progress: a frame, and no row.
	_, err = f.source.Post(ctx, chatID, conductor.Outbound{
		Type: conductor.OutProgress, Text: "⏳ reading the schema", TaskID: "task_01",
	})
	require.NoError(t, err)
	assert.Equal(t, "⏳ reading the schema", nextFrame(t, stream).GetProgress())

	// The relay's answer: a frame AND a row.
	_, err = f.source.Post(ctx, chatID, conductor.Outbound{
		Type: conductor.OutFinal, Text: "Here it is. See trend.png.", TaskID: "task_01",
	})
	require.NoError(t, err)
	final := nextFrame(t, stream).GetMessage()
	require.NotNil(t, final)
	assert.Equal(t, uint64(4), final.GetSeq())
	assert.Empty(t, final.GetAttachments(), "the final is posted before its attachments resolve")

	// The relay's attachment: the same message again, now with the artifact on it.
	require.NoError(t, f.source.Attach(ctx, chatID, conductor.Attachment{
		Name: "trend.png", ContentType: "image/png", Size: 91_000,
		ArtifactID: "art_01", TaskID: "task_01",
	}))
	attached := nextFrame(t, stream).GetMessage()
	require.NotNil(t, attached)
	assert.Equal(t, final.GetSeq(), attached.GetSeq(), "same seq: a client replaces rather than appends")
	require.Len(t, attached.GetAttachments(), 1)
	assert.Equal(t, "art_01", attached.GetAttachments()[0].GetArtifactId())
	assert.Equal(t, "trend.png", attached.GetAttachments()[0].GetName())
	assert.Equal(t, "image/png", attached.GetAttachments()[0].GetContentType())
	assert.Equal(t, int64(91_000), attached.GetAttachments()[0].GetSizeBytes())

	require.NoError(t, f.source.React(ctx, chatID, conductor.ReactionDone))
	done := nextFrame(t, stream).GetStatus()
	require.NotNil(t, done)
	assert.Equal(t, chat.StatusFinished, done.GetState())

	// A reload shows the four rows and no progress line.
	stored, err := f.store.ListChatMessages(ctx, chatID, 0)
	require.NoError(t, err)
	require.Len(t, stored, 4)
	for _, m := range stored {
		assert.NotContains(t, m.Text, "reading the schema", "progress is never a row")
	}
	require.NoError(t, stream.Close())
}

func TestStreamChatFromSeqSkipsWhatTheClientHas(t *testing.T) {
	f := newChatFixture(t)
	ctx := context.Background()
	created, err := f.client.CreateChat(ctx, connect.NewRequest(&agentv1.CreateChatRequest{}))
	require.NoError(t, err)
	chatID := created.Msg.GetChat().GetId()

	for _, text := range []string{"one", "two", "three"} {
		_, err := f.store.AppendChatMessage(ctx, store.ChatMessage{
			ChatID: chatID, Role: store.RoleUser, Text: text,
		})
		require.NoError(t, err)
	}
	streamCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	stream := f.streamAs(streamCtx, t, "alice", chatID, 2)
	require.NotNil(t, nextFrame(t, stream).GetStatus(), "the first frame is always the turn state")
	require.NotNil(t, nextFrame(t, stream).GetChat())
	require.NotNil(t, nextFrame(t, stream).GetPullRequests())

	msg := nextFrame(t, stream).GetMessage()
	require.NotNil(t, msg)
	assert.Equal(t, uint64(3), msg.GetSeq(), "from_seq is exclusive")
	assert.Equal(t, "three", msg.GetText())
	require.NoError(t, stream.Close())
}

func TestStreamChatSaysATurnIsAlreadyRunning(t *testing.T) {
	f := newChatFixture(t)
	ctx := context.Background()
	created, err := f.client.CreateChat(ctx, connect.NewRequest(&agentv1.CreateChatRequest{}))
	require.NoError(t, err)
	chatID := created.Msg.GetChat().GetId()

	_, err = f.client.SendChatMessage(ctx, connect.NewRequest(&agentv1.SendChatMessageRequest{
		ChatId: chatID, Text: "how many active accounts last month",
	}))
	require.NoError(t, err)

	// A browser that connects mid-turn must find the composer disabled without waiting for
	// the next progress line.
	streamCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	stream := f.streamAs(streamCtx, t, "alice", chatID, 0)
	status := nextFrame(t, stream).GetStatus()
	require.NotNil(t, status)
	assert.Equal(t, chat.StatusStarted, status.GetState(),
		"a browser joining mid-turn finds the composer disabled without waiting for a progress line")
	require.NotNil(t, nextFrame(t, stream).GetChat())
	require.NotNil(t, nextFrame(t, stream).GetPullRequests())
	assert.Equal(t, uint64(1), nextFrame(t, stream).GetMessage().GetSeq())
	require.NoError(t, stream.Close())
}

func TestASecondSendWhileATurnRunsIsFailedPrecondition(t *testing.T) {
	f := newChatFixture(t)
	ctx := context.Background()
	created, err := f.client.CreateChat(ctx, connect.NewRequest(&agentv1.CreateChatRequest{}))
	require.NoError(t, err)
	chatID := created.Msg.GetChat().GetId()

	_, err = f.client.SendChatMessage(ctx, connect.NewRequest(&agentv1.SendChatMessageRequest{
		ChatId: chatID, Text: "first",
	}))
	require.NoError(t, err)

	_, err = f.client.SendChatMessage(ctx, connect.NewRequest(&agentv1.SendChatMessageRequest{
		ChatId: chatID, Text: "second",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))

	stored, err := f.store.ListChatMessages(ctx, chatID, 0)
	require.NoError(t, err)
	assert.Len(t, stored, 1, "a refused send stores nothing")

	// The chat list agrees with the refusal, so a reload disables the composer too.
	listed, err := f.client.ListChats(ctx, connect.NewRequest(&agentv1.ListChatsRequest{}))
	require.NoError(t, err)
	require.Len(t, listed.Msg.GetChats(), 1)
	assert.Equal(t, "first", listed.Msg.GetChats()[0].GetPreview())
}

func TestSendChatMessageValidatesItsInput(t *testing.T) {
	f := newChatFixture(t)
	ctx := context.Background()

	_, err := f.client.SendChatMessage(ctx, connect.NewRequest(&agentv1.SendChatMessageRequest{Text: "hello"}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	created, err := f.client.CreateChat(ctx, connect.NewRequest(&agentv1.CreateChatRequest{}))
	require.NoError(t, err)
	_, err = f.client.SendChatMessage(ctx, connect.NewRequest(&agentv1.SendChatMessageRequest{
		ChatId: created.Msg.GetChat().GetId(),
		Text:   string(make([]byte, maxChatMessageBytes+1)),
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	_, err = f.client.SendChatMessage(ctx, connect.NewRequest(&agentv1.SendChatMessageRequest{
		ChatId: "chat_nope", Text: "hello",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

func TestRenameChatThroughTheService(t *testing.T) {
	f := newChatFixture(t)
	ctx := context.Background()
	bob := f.clientAs("bob")

	created, err := f.client.CreateChat(ctx, connect.NewRequest(&agentv1.CreateChatRequest{Title: "August numbers"}))
	require.NoError(t, err)
	id := created.Msg.GetChat().GetId()

	renamed, err := f.client.RenameChat(ctx, connect.NewRequest(&agentv1.RenameChatRequest{
		ChatId: id, Title: "  Q3   forecast ",
	}))
	require.NoError(t, err)
	assert.Equal(t, "Q3 forecast", renamed.Msg.GetChat().GetTitle())

	listed, err := f.client.ListChats(ctx, connect.NewRequest(&agentv1.ListChatsRequest{}))
	require.NoError(t, err)
	require.Len(t, listed.Msg.GetChats(), 1)
	assert.Equal(t, "Q3 forecast", listed.Msg.GetChats()[0].GetTitle())

	_, err = bob.RenameChat(ctx, connect.NewRequest(&agentv1.RenameChatRequest{ChatId: id, Title: "stolen"}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))

	_, err = f.client.RenameChat(ctx, connect.NewRequest(&agentv1.RenameChatRequest{ChatId: id, Title: "  "}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	_, err = f.client.RenameChat(ctx, connect.NewRequest(&agentv1.RenameChatRequest{Title: "no id"}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestTheChatRPCsWithoutASourceSaySo(t *testing.T) {
	svc := NewAgentService(AgentServiceOptions{Store: newStore(t)})
	_, err := svc.CreateChat(loginCtx("alice"), connect.NewRequest(&agentv1.CreateChatRequest{}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	_, err = svc.ListChats(loginCtx("alice"), connect.NewRequest(&agentv1.ListChatsRequest{}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	_, err = svc.RenameChat(loginCtx("alice"), connect.NewRequest(&agentv1.RenameChatRequest{
		ChatId: "chat_01abc", Title: "nope",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	_, err = svc.DeleteChat(loginCtx("alice"), connect.NewRequest(&agentv1.DeleteChatRequest{ChatId: "chat_01abc"}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
}

// TestStreamChatEndsWhenTheClientGoesAway pins the other half of "it never ends on its
// own": the subscription is released, so a browser that navigates away does not leak a
// goroutine and a channel per chat it visited.
func TestStreamChatEndsWhenTheClientGoesAway(t *testing.T) {
	f := newChatFixture(t)
	ctx := context.Background()
	created, err := f.client.CreateChat(ctx, connect.NewRequest(&agentv1.CreateChatRequest{}))
	require.NoError(t, err)
	chatID := created.Msg.GetChat().GetId()
	_, err = f.store.AppendChatMessage(ctx, store.ChatMessage{
		ChatID: chatID, Role: store.RoleUser, Text: "hello",
	})
	require.NoError(t, err)

	streamCtx, cancel := context.WithCancel(ctx)
	stream := f.streamAs(streamCtx, t, "alice", chatID, 0)
	require.NotNil(t, nextFrame(t, stream).GetStatus())
	require.NotNil(t, nextFrame(t, stream).GetChat())
	require.NotNil(t, nextFrame(t, stream).GetPullRequests())
	require.NotNil(t, nextFrame(t, stream).GetMessage())

	cancel()
	_ = stream.Close()
	assert.Eventually(t, func() bool { return subscriberGone(f, chatID) },
		10*time.Second, 50*time.Millisecond, "the subscription must go with the request")
}

func TestAttachAndDetachAPullRequestThroughTheService(t *testing.T) {
	f := newChatFixture(t)
	ctx := context.Background()
	created, err := f.client.CreateChat(ctx, connect.NewRequest(&agentv1.CreateChatRequest{}))
	require.NoError(t, err)
	chatID := created.Msg.GetChat().GetId()

	streamCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	stream := f.streamAs(streamCtx, t, "alice", chatID, 0)
	require.NotNil(t, nextFrame(t, stream).GetStatus())
	require.NotNil(t, nextFrame(t, stream).GetChat())
	assert.Empty(t, nextFrame(t, stream).GetPullRequests().GetPullRequests(),
		"a new chat has produced nothing yet")

	// A URL with the path a browser was actually on. What is stored is canonical.
	attached, err := f.client.AttachChatPullRequest(ctx, connect.NewRequest(
		&agentv1.AttachChatPullRequestRequest{
			ChatId: chatID, Url: "https://github.com/acme/api/pull/41/files",
		}))
	require.NoError(t, err)
	require.Len(t, attached.Msg.GetPullRequests(), 1)
	pr := attached.Msg.GetPullRequests()[0]
	assert.Equal(t, "https://github.com/acme/api/pull/41", pr.GetUrl())
	assert.Equal(t, "acme", pr.GetOwner())
	assert.Equal(t, "api", pr.GetRepo())
	assert.Equal(t, int32(41), pr.GetNumber())
	assert.Equal(t, store.PullRequestFromHuman, pr.GetSource())

	// Every browser watching the chat is told, not only the one that pressed the button.
	live := nextFrame(t, stream).GetPullRequests()
	require.NotNil(t, live)
	require.Len(t, live.GetPullRequests(), 1)
	assert.Equal(t, "https://github.com/acme/api/pull/41", live.GetPullRequests()[0].GetUrl())

	detached, err := f.client.DetachChatPullRequest(ctx, connect.NewRequest(
		&agentv1.DetachChatPullRequestRequest{
			ChatId: chatID, Url: "https://github.com/acme/api/pull/41",
		}))
	require.NoError(t, err)
	assert.Empty(t, detached.Msg.GetPullRequests())
	assert.Empty(t, nextFrame(t, stream).GetPullRequests().GetPullRequests())
	require.NoError(t, stream.Close())
}

func TestWhatTheChatPullRequestRPCsRefuse(t *testing.T) {
	f := newChatFixture(t)
	ctx := context.Background()
	created, err := f.client.CreateChat(ctx, connect.NewRequest(&agentv1.CreateChatRequest{}))
	require.NoError(t, err)
	chatID := created.Msg.GetChat().GetId()

	// An issue is not a pull request, and neither is a repository or another host.
	for _, url := range []string{
		"", "https://github.com/acme/api/issues/41", "https://github.com/acme/api",
		"https://gitlab.com/acme/api/pull/41", "not a url at all",
	} {
		_, err := f.client.AttachChatPullRequest(ctx, connect.NewRequest(
			&agentv1.AttachChatPullRequestRequest{ChatId: chatID, Url: url}))
		assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err), "should be refused: %q", url)
	}

	_, err = f.client.AttachChatPullRequest(ctx, connect.NewRequest(
		&agentv1.AttachChatPullRequestRequest{Url: "https://github.com/acme/api/pull/41"}))
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err), "chat_id is required")

	// Detaching something that is not linked is not found, the same as a link that was
	// already taken off.
	_, err = f.client.DetachChatPullRequest(ctx, connect.NewRequest(
		&agentv1.DetachChatPullRequestRequest{
			ChatId: chatID, Url: "https://github.com/acme/api/pull/41",
		}))
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))

	// Another login's chat is not found, not forbidden: its existence is not bob's to learn.
	bob := f.clientAs("bob")
	_, err = bob.AttachChatPullRequest(ctx, connect.NewRequest(
		&agentv1.AttachChatPullRequestRequest{
			ChatId: chatID, Url: "https://github.com/acme/api/pull/41",
		}))
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

func TestAChatStreamOpensWithThePullRequestsItAlreadyHas(t *testing.T) {
	f := newChatFixture(t)
	ctx := context.Background()
	created, err := f.client.CreateChat(ctx, connect.NewRequest(&agentv1.CreateChatRequest{}))
	require.NoError(t, err)
	chatID := created.Msg.GetChat().GetId()

	// What a finished turn leaves behind, through the same call the conductor makes.
	require.NoError(t, f.source.LinkPullRequests(ctx, chatID,
		conductor.FindPullRequests("Opened https://github.com/acme/api/pull/41 with the fix.")))

	streamCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	stream := f.streamAs(streamCtx, t, "alice", chatID, 0)
	require.NotNil(t, nextFrame(t, stream).GetStatus())
	require.NotNil(t, nextFrame(t, stream).GetChat())

	opening := nextFrame(t, stream).GetPullRequests()
	require.NotNil(t, opening)
	require.Len(t, opening.GetPullRequests(), 1)
	assert.Equal(t, "https://github.com/acme/api/pull/41", opening.GetPullRequests()[0].GetUrl())
	assert.Equal(t, store.PullRequestFromTurn, opening.GetPullRequests()[0].GetSource(),
		"a reload can still tell a turn's link from a human's")
	require.NoError(t, stream.Close())
}

// subscriberGone reports whether the chat has no live subscribers left. It publishes
// nothing and reads no private state: a chat with a subscriber still holds one.
func subscriberGone(f chatFixture, chatID string) bool {
	// Subscribe/Close on a probe is the only public way to ask, and it is enough: the
	// count includes the probe itself, so one means the stream's is gone.
	probe := f.source.Subscribe(context.Background(), chatID)
	defer probe.Close()
	return f.source.Subscribers(chatID) == 1
}
