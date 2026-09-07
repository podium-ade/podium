//go:build e2e

package e2e_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentv1 "github.com/alvaroibarguen/podium/internal/proto/podium/agent/v1"
	"github.com/alvaroibarguen/podium/internal/proto/podium/agent/v1/agentv1connect"
	"github.com/alvaroibarguen/podium/internal/server"
	"github.com/alvaroibarguen/podium/internal/server/artifacts/fakes3"
)

// copyExampleProfile copies examples/agent into a temporary directory so a test can add or
// rewrite a playbook without touching the repository's own example. The example is what
// docs/agent.md points at and what the plain round trip runs; the copies exist to add the
// dry-run knobs and the extra playbooks a test needs, neither of which a shipped example
// should carry.
func copyExampleProfile(t *testing.T) string {
	t.Helper()
	root, err := repoRoot()
	require.NoError(t, err)
	src := filepath.Join(root, "examples", "agent")
	dst := t.TempDir()

	require.NoError(t, filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o750)
		}
		raw, err := os.ReadFile(path) //nolint:gosec // the repository's own example
		if err != nil {
			return err
		}
		return os.WriteFile(target, raw, 0o600)
	}))
	return dst
}

// chatDefaultPlaybook is the playbook the copied profile makes the web chat's default. Podium
// ships one playbook, so a test about the chat's default has to define a second one itself —
// which is the honest shape anyway: chat_default_playbook only means something when there is
// more than one playbook to choose between.
const chatDefaultPlaybook = "analyst"

// chatProfileDir is the example profile plus a second playbook, defined here, that the chat
// starts on: chat_default_playbook names it, and it runs the plain runtime image in dry run.
//
// The playbook is written by this test rather than shipped, because a shipped one would be a
// guess at somebody's workflow. What it exercises is the machinery: ListPlaybooks reports a
// chat default, and a message naming no playbook runs it.
func chatProfileDir(t *testing.T) string {
	t.Helper()
	dst := copyExampleProfile(t)

	require.NoError(t, os.WriteFile(filepath.Join(dst, "playbooks", chatDefaultPlaybook+".yaml"),
		[]byte("image: "+agentRuntimeImage+`
system_prompt: Answer questions about the data warehouse.
allowed_tools: [bash, read, write]
env:
  PODIUM_AGENT_DRY_RUN: "1"
`), 0o600))

	path := filepath.Join(dst, "profile.yaml")
	raw, err := os.ReadFile(path) //nolint:gosec // this test's own copy
	require.NoError(t, err)
	require.NotContains(t, string(raw), "chat_default_playbook:",
		"examples/agent/profile.yaml sets a chat default again; this copy would fight it")
	require.NoError(t, os.WriteFile(path,
		append(raw, []byte("chat_default_playbook: "+chatDefaultPlaybook+"\n")...), 0o600))
	return dst
}

// The Connect procedures the chat screen calls, through podium-server's proxy.
const (
	createChatPath      = "/podium.agent.v1.AgentService/CreateChat"
	listChatsPath       = "/podium.agent.v1.AgentService/ListChats"
	sendChatMessagePath = "/podium.agent.v1.AgentService/SendChatMessage"
	listPlaybooksPath   = "/podium.agent.v1.AgentService/ListPlaybooks"
)

// agentClientThrough builds the generated AgentService client pointed at podium-server,
// with the dev token a browser would send. Everything below therefore crosses the reverse
// proxy, which is the only path a browser has to the conductor.
func agentClientThrough(h *harness, login string) agentv1connect.AgentServiceClient {
	return agentv1connect.NewAgentServiceClient(http.DefaultClient, h.url(),
		connect.WithInterceptors(connect.UnaryInterceptorFunc(
			func(next connect.UnaryFunc) connect.UnaryFunc {
				return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
					req.Header().Set("Authorization", "Bearer "+devToken)
					// A client-supplied login must be dropped by the proxy and replaced
					// with the server's own word. Sending one is how that is proved.
					if login != "" {
						req.Header().Set("X-Podium-Login", login)
					}
					return next(ctx, req)
				}
			})))
}

// streamChatThrough opens StreamChat through the proxy. The unary interceptor above does not
// apply to a streaming call, so the headers go on the request.
func streamChatThrough(
	ctx context.Context, h *harness, chatID string, fromSeq uint64,
) (*connect.ServerStreamForClient[agentv1.ChatFrame], error) {
	client := agentv1connect.NewAgentServiceClient(http.DefaultClient, h.url())
	req := connect.NewRequest(&agentv1.StreamChatRequest{ChatId: chatID, FromSeq: fromSeq})
	req.Header().Set("Authorization", "Bearer "+devToken)
	return client.StreamChat(ctx, req)
}

// chatRow is one message as the conductor's database holds it.
type chatRow struct {
	Seq         int64
	Role        string
	Text        string
	Attachments string
}

func chatRows(t *testing.T, databaseURL, chatID string) []chatRow {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, databaseURL)
	require.NoError(t, err)
	defer func() { _ = conn.Close(ctx) }()

	rows, err := conn.Query(ctx, `select seq, role, text, attachments::text
		from chat_messages where chat_id = $1 order by seq`, chatID)
	require.NoError(t, err)
	defer rows.Close()

	var out []chatRow
	for rows.Next() {
		var r chatRow
		require.NoError(t, rows.Scan(&r.Seq, &r.Role, &r.Text, &r.Attachments))
		out = append(out, r)
	}
	require.NoError(t, rows.Err())
	return out
}

// chatLogins is every (chat id, login) pair the conductor has recorded.
func chatLogins(t *testing.T, databaseURL string) map[string]string {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, databaseURL)
	require.NoError(t, err)
	defer func() { _ = conn.Close(ctx) }()

	rows, err := conn.Query(ctx, "select id, login from chats")
	require.NoError(t, err)
	defer rows.Close()

	out := map[string]string{}
	for rows.Next() {
		var id, login string
		require.NoError(t, rows.Scan(&id, &login))
		out[id] = login
	}
	require.NoError(t, rows.Err())
	return out
}

// startAgentForChat brings up the conductor on a known address with the chat profile, so
// podium-server can be configured to proxy to it before it starts.
func startAgentForChat(t *testing.T, h *harness, addr string) *agentProc {
	t.Helper()
	a := &agentProc{
		t:           t,
		h:           h,
		addr:        addr,
		databaseURL: newAgentDatabase(t),
		profileDir:  chatProfileDir(t),
	}
	a.start()
	t.Cleanup(a.stop)
	a.awaitReady()
	return a
}

// ---------------------------------------------------------------------------
// TestChatTurnRoundTrip
// ---------------------------------------------------------------------------

// TestChatTurnRoundTrip is story three's machinery, end to end and through the proxy: a
// browser opens a chat, asks a question, and the answer a real agent runtime produced in a
// real container on a real node comes back on the stream and into the database.
//
// Everything a browser does, this test does the same way: the generated AgentService client
// against podium-server, never against the conductor's own listener.
func TestChatTurnRoundTrip(t *testing.T) {
	requireAgentRuntimeImage(t)

	// The turn's own accounting (turn.json) reaches the conductor as an artifact.
	fakeS3 := fakes3.Start(t)
	agentAddr := freeLoopbackAddr(t)
	h := newHarness(t, func(c *server.Config) {
		c.S3 = fakeS3.Config()
		c.AgentURL = "http://" + agentAddr
		c.AgentToken = agentToken
	})
	startNode(t, h)
	// The reserved secret must exist before a turn can be admitted, dry run included.
	setSecret(t, h, anthropicKeySecret, "sk-ant-not-a-real-key")

	agent := startAgentForChat(t, h, agentAddr)
	// A login the proxy must ignore: what reaches the conductor is the server's own word
	// about who is calling, which under the dev transport is "dev".
	client := agentClientThrough(h, "somebody-else")
	ctx := context.Background()

	// The playbook chip's source. chatProfileDir is what makes this profile's chat default the
	// second playbook rather than default_playbook.
	playbooks, err := client.ListPlaybooks(ctx, connect.NewRequest(&agentv1.ListPlaybooksRequest{}))
	require.NoError(t, err)
	require.NotEmpty(t, playbooks.Msg.GetPlaybooks())
	assert.Equal(t, "Podium", playbooks.Msg.GetProfileDisplayName())
	var chatDefault string
	for _, s := range playbooks.Msg.GetPlaybooks() {
		if s.GetChatDefault() {
			chatDefault = s.GetName()
		}
	}
	assert.Equal(t, chatDefaultPlaybook, chatDefault, "profile.yaml: chat_default_playbook")

	created, err := client.CreateChat(ctx, connect.NewRequest(&agentv1.CreateChatRequest{
		Title: "August numbers",
	}))
	require.NoError(t, err)
	chatID := created.Msg.GetChat().GetId()
	require.True(t, strings.HasPrefix(chatID, "chat_"), "ids are prefixed ULIDs: %s", chatID)
	assert.False(t, created.Msg.GetChat().GetTurnRunning())

	// The chat belongs to the login the SERVER asserted, not the one the client sent.
	assert.Equal(t, map[string]string{chatID: "dev"}, chatLogins(t, agent.databaseURL),
		"the proxy must strip a client-supplied X-Podium-Login")

	// Watch the chat the way the browser does, from the beginning.
	streamCtx, cancel := context.WithTimeout(ctx, 4*time.Minute)
	defer cancel()
	stream, err := streamChatThrough(streamCtx, h, chatID, 0)
	require.NoError(t, err)
	defer func() { _ = stream.Close() }()

	frames := make(chan *agentv1.ChatFrame, 64)
	streamDone := make(chan error, 1)
	go func() {
		for stream.Receive() {
			frames <- stream.Msg()
		}
		streamDone <- stream.Err()
	}()

	sent, err := client.SendChatMessage(ctx, connect.NewRequest(&agentv1.SendChatMessageRequest{
		ChatId: chatID, Text: "hello there",
	}))
	require.NoError(t, err)
	assert.Equal(t, uint64(1), sent.Msg.GetMessage().GetSeq())
	assert.Equal(t, "user", sent.Msg.GetMessage().GetRole())

	// A second message while the turn runs is refused by the server, whatever the UI does.
	_, err = client.SendChatMessage(ctx, connect.NewRequest(&agentv1.SendChatMessageRequest{
		ChatId: chatID, Text: "and another thing",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err),
		"one turn at a time per conversation")

	// A task appears, and it is the one the conductor made for this chat.
	sourceKey := "chat:" + chatID
	var taskID string
	waitFor(t, 2*time.Minute, "the chat's task to appear", func() bool {
		rows := turnRows(t, agent.databaseURL, sourceKey)
		if len(rows) != 1 || rows[0].TaskID == "" {
			return false
		}
		taskID = rows[0].TaskID
		return strings.Contains(h.podiumOK("tasks"), taskID)
	}, func() string { return "agent log:\n" + agent.logs() })
	t.Logf("chat turn task: %s", taskID)

	// read the stream until the assistant's answer lands. Frames arrive while the turn is
	// still running, which is the whole point of a server-streaming RPC behind the proxy.
	var (
		userSeen      bool
		assistant     *agentv1.ChatMessage
		sawStarted    bool
		sawTerminal   bool
		progressSeen  int
		frameDeadline = time.After(3 * time.Minute)
	)
	for assistant == nil {
		select {
		case f := <-frames:
			switch {
			case f.GetMessage() != nil:
				msg := f.GetMessage()
				if msg.GetRole() == "user" {
					userSeen = true
					continue
				}
				assistant = msg
			case f.GetProgress() != "":
				progressSeen++
				require.False(t, sawTerminal,
					"a progress frame must never follow the end of the turn it belongs to")
			case f.GetStatus() != nil:
				switch f.GetStatus().GetState() {
				case "started":
					// The stream's opening frame is the turn state, which on a quiet chat
					// is "finished" before any turn exists. Only a turn that actually
					// started can have an end.
					sawStarted, sawTerminal = true, false
				case "finished", "failed":
					sawTerminal = sawStarted
				}
			}
		case err := <-streamDone:
			t.Fatalf("the chat stream ended before the answer arrived: %v\nagent log:\n%s",
				err, agent.logs())
		case <-frameDeadline:
			t.Fatalf("no answer arrived on the chat stream\nagent log:\n%s", agent.logs())
		}
	}
	assert.True(t, userSeen, "the human's own message is replayed on the stream")
	assert.True(t, sawStarted, "the stream says when a turn starts, so the composer disables")
	assert.Equal(t, "dry run: hello there", assistant.GetText(),
		"the runtime's answer must reach the chat verbatim")
	assert.Equal(t, uint64(2), assistant.GetSeq())
	t.Logf("progress frames seen before the answer: %d", progressSeen)

	// The turn row is the record, and the session is keyed on the chat.
	waitFor(t, 2*time.Minute, "the turn to succeed", func() bool {
		rows := turnRows(t, agent.databaseURL, sourceKey)
		return len(rows) == 1 && rows[0].Status == "succeeded"
	}, func() string {
		return fmt.Sprintf("agent log:\n%s\npodium task:\n%s",
			agent.logs(), h.podiumOK("task", "get", taskID))
	})
	rows := turnRows(t, agent.databaseURL, sourceKey)
	require.Len(t, rows, 1)
	assert.Equal(t, "dry run: hello there", rows[0].FinalText)
	require.NotNil(t, rows[0].NumTurns, "turn.json must have been read")
	assert.Equal(t, 0, *rows[0].NumTurns, "a dry run spends no turns")
	assert.Equal(t, "chat", sessionKindOf(t, agent.databaseURL, sourceKey))

	// Both messages are rows, and no progress line is: a reload shows the answer and no
	// trail of thinking.
	stored := chatRows(t, agent.databaseURL, chatID)
	require.Len(t, stored, 2, "one question, one answer, and nothing ephemeral: %+v", stored)
	assert.Equal(t, chatRow{Seq: 1, Role: "user", Text: "hello there", Attachments: "[]"}, stored[0])
	assert.Equal(t, chatRow{Seq: 2, Role: "assistant", Text: "dry run: hello there", Attachments: "[]"}, stored[1])

	// The composer is enabled again, and the list agrees.
	waitFor(t, 30*time.Second, "the chat to report no turn running", func() bool {
		listed, err := client.ListChats(ctx, connect.NewRequest(&agentv1.ListChatsRequest{}))
		if err != nil {
			return false
		}
		return len(listed.Msg.GetChats()) == 1 && !listed.Msg.GetChats()[0].GetTurnRunning()
	}, func() string { return "agent log:\n" + agent.logs() })

	listed, err := client.ListChats(ctx, connect.NewRequest(&agentv1.ListChatsRequest{}))
	require.NoError(t, err)
	require.Len(t, listed.Msg.GetChats(), 1)
	assert.Equal(t, "August numbers", listed.Msg.GetChats()[0].GetTitle())
	assert.Equal(t, chatDefaultPlaybook, listed.Msg.GetChats()[0].GetPlaybook(),
		"a chat remembers the playbook it started with")
	assert.Equal(t, "dry run: hello there", listed.Msg.GetChats()[0].GetPreview())
	require.NotNil(t, listed.Msg.GetChats()[0].GetLastMessageAt())

	// A second question in the same chat now goes through, and its brief carries the first
	// exchange: the transcript is the conversation.
	_, err = client.SendChatMessage(ctx, connect.NewRequest(&agentv1.SendChatMessageRequest{
		ChatId: chatID, Text: "and the month before",
	}))
	require.NoError(t, err)
	waitFor(t, 3*time.Minute, "the second turn to succeed", func() bool {
		rows := turnRows(t, agent.databaseURL, sourceKey)
		return len(rows) == 2 && rows[1].Status == "succeeded"
	}, func() string { return "agent log:\n" + agent.logs() })

	stored = chatRows(t, agent.databaseURL, chatID)
	require.Len(t, stored, 4)
	assert.Equal(t, "dry run: and the month before", stored[3].Text)

	// A replay from a later seq skips what the client already has.
	tailCtx, tailCancel := context.WithTimeout(ctx, 60*time.Second)
	defer tailCancel()
	tail, err := streamChatThrough(tailCtx, h, chatID, 3)
	require.NoError(t, err)
	require.True(t, tail.Receive(), "the stream opens with the turn state: %v", tail.Err())
	require.NotNil(t, tail.Msg().GetStatus(), "the first frame is always the turn state")
	require.True(t, tail.Receive(), "then the chat row: %v", tail.Err())
	require.NotNil(t, tail.Msg().GetChat())
	require.True(t, tail.Receive(), "a replay from seq 3 must deliver seq 4: %v", tail.Err())
	assert.Equal(t, uint64(4), tail.Msg().GetMessage().GetSeq())
	require.NoError(t, tail.Close())

	requireNoPodiumResources(t)
}

// sessionKindOf reads the source_kind the conductor recorded for a session, which is what
// makes a chat turn retain with source:chat provenance (step 19).
func sessionKindOf(t *testing.T, databaseURL, sourceKey string) string {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, databaseURL)
	require.NoError(t, err)
	defer func() { _ = conn.Close(ctx) }()

	var kind string
	require.NoError(t, conn.QueryRow(ctx,
		"select source_kind from sessions where source_key = $1", sourceKey).Scan(&kind))
	return kind
}

// ---------------------------------------------------------------------------
// The proxy's own guarantees, without a node
// ---------------------------------------------------------------------------

// TestChatsArePerLogin is the acceptance item about two logins. The dev transport has one
// identity, so the two logins are asserted where they are actually decided: the proxy sets
// X-Podium-Login from the authenticated identity, and the conductor partitions on it.
func TestChatsArePerLogin(t *testing.T) {
	agentAddr := freeLoopbackAddr(t)
	h := newHarness(t, func(c *server.Config) {
		c.AgentURL = "http://" + agentAddr
		c.AgentToken = agentToken
	})
	agent := startAgentForChat(t, h, agentAddr)
	ctx := context.Background()

	// Through the proxy, as a browser: the chat is "dev"'s, whatever header was sent.
	client := agentClientThrough(h, "alice")
	created, err := client.CreateChat(ctx, connect.NewRequest(&agentv1.CreateChatRequest{Title: "dev's"}))
	require.NoError(t, err)
	devChat := created.Msg.GetChat().GetId()

	// No login at all is refused: a chat has an owner or it does not exist. This is what a
	// direct call to the conductor looks like — one that did not come through the proxy.
	code, body := agent.post(createChatPath, agentToken, `{"title":"nobody's"}`)
	assert.Equal(t, http.StatusUnauthorized, code, body)
	assert.Contains(t, body, "unauthenticated", body)

	// And direct to the conductor with its own bearer AND a second login, as podium-server
	// would if the tailnet transport had authenticated somebody else. This is the only way
	// to present a second login on a control plane whose transport has one identity, and it
	// exercises exactly the partition the acceptance item is about.
	bobChat := createChatAs(t, agent, "bob", "bob's")
	require.NotEqual(t, devChat, bobChat)
	assert.Equal(t, map[string]string{devChat: "dev", bobChat: "bob"},
		chatLogins(t, agent.databaseURL))

	// dev's list holds dev's chat and nothing else.
	listed, err := client.ListChats(ctx, connect.NewRequest(&agentv1.ListChatsRequest{}))
	require.NoError(t, err)
	require.Len(t, listed.Msg.GetChats(), 1)
	assert.Equal(t, devChat, listed.Msg.GetChats()[0].GetId())

	// bob's list holds bob's.
	code, body = agent.postAs(listChatsPath, agentToken, "bob", `{}`)
	require.Equal(t, http.StatusOK, code, body)
	assert.Contains(t, body, bobChat)
	assert.NotContains(t, body, devChat, "another login's chat must never be returned")

	// And knowing the id is not access: bob cannot speak into dev's chat.
	code, body = agent.postAs(sendChatMessagePath, agentToken, "bob",
		fmt.Sprintf(`{"chat_id":%q,"text":"hello"}`, devChat))
	assert.Equal(t, http.StatusNotFound, code, body)
}

// createChatAs opens a chat as one login, straight against the conductor.
func createChatAs(t *testing.T, a *agentProc, login, title string) string {
	t.Helper()
	code, body := a.postAs(createChatPath, agentToken, login, fmt.Sprintf(`{"title":%q}`, title))
	require.Equal(t, http.StatusOK, code, body)
	var res struct {
		Chat struct {
			ID string `json:"id"`
		} `json:"chat"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &res))
	require.NotEmpty(t, res.Chat.ID)
	return res.Chat.ID
}

// TestTheChatIsBehindTheProxysIdentity pins the two things the browser's path depends on:
// the conductor's chat is unreachable without the server's bearer, and a node's identity is
// refused before the proxy forwards anything.
func TestTheChatIsBehindTheProxysIdentity(t *testing.T) {
	agentAddr := freeLoopbackAddr(t)
	h := newHarness(t, func(c *server.Config) {
		c.AgentURL = "http://" + agentAddr
		c.AgentToken = agentToken
	})
	agent := startAgentForChat(t, h, agentAddr)

	code, body := agent.post(createChatPath, "", `{}`)
	assert.Equal(t, http.StatusUnauthorized, code, body)
	code, body = agent.post(listPlaybooksPath, "wrong-token", `{}`)
	assert.Equal(t, http.StatusUnauthorized, code, body)

	// Through the proxy the browser's own credential is the dev token, and the conductor
	// never sees it.
	code, _, body = connectCall(t, h.url(), listPlaybooksPath, `{}`, nil)
	assert.Equal(t, http.StatusOK, code, body)
	assert.Contains(t, body, chatDefaultPlaybook)
}

// postAs is a POST against the conductor carrying both the server's bearer and the login
// the server would have asserted. It is how a second identity is presented on a control
// plane whose dev transport has only one.
func (a *agentProc) postAs(path, token, login, body string) (int, string) {
	a.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.url()+path, strings.NewReader(body))
	require.NoError(a.t, err)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if login != "" {
		req.Header.Set("X-Podium-Login", login)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return -1, err.Error()
	}
	defer func() { _ = res.Body.Close() }()
	raw, err := io.ReadAll(res.Body)
	require.NoError(a.t, err)
	return res.StatusCode, string(raw)
}
