//go:build integration

package chathost_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/agent/chathost"
	"github.com/podium-ade/podium/internal/agent/conductor"
)

// fakeModel is an OpenAI-compatible endpoint. A user message "CALL <code>" is answered with an
// `execute` call running that code; once a tool result is in the conversation, the answer is
// that result. Every request is recorded.
type fakeModel struct {
	mu   sync.Mutex
	reqs []modelRequest
}

type modelRequest struct {
	Auth    string
	Session string
	System  string
	Tools   []string
	Results []string
}

func (f *fakeModel) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"id": "m1"}}})
		return
	}
	var req struct {
		Messages []struct {
			Role    string `json:"role"`
			Content any    `json:"content"`
		} `json:"messages"`
		Tools []struct {
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		} `json:"tools"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	rec := modelRequest{Auth: r.Header.Get("Authorization"), Session: r.Header.Get("x-opencode-session-id")}
	user := ""
	for _, m := range req.Messages {
		text, _ := m.Content.(string)
		if text == "" {
			b, _ := json.Marshal(m.Content)
			text = string(b)
		}
		switch m.Role {
		case "system":
			rec.System += text
		case "user":
			user = text
		case "tool":
			rec.Results = append(rec.Results, text)
		}
	}
	for _, t := range req.Tools {
		rec.Tools = append(rec.Tools, t.Function.Name)
	}
	f.mu.Lock()
	f.reqs = append(f.reqs, rec)
	f.mu.Unlock()

	w.Header().Set("Content-Type", "text/event-stream")
	flush := func(v any) {
		b, _ := json.Marshal(v)
		_, _ = fmt.Fprintf(w, "data: %s\n\n", b)
		w.(http.Flusher).Flush()
	}
	chunk := func(delta map[string]any, finish any) map[string]any {
		return map[string]any{"id": "c", "object": "chat.completion.chunk", "model": "m1",
			"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}},
			"usage":   map[string]any{"prompt_tokens": 10, "completion_tokens": 3, "total_tokens": 13}}
	}
	if cmd, ok := strings.CutPrefix(user, "SHELL "); ok && len(rec.Results) == 0 {
		args, _ := json.Marshal(map[string]string{"command": cmd})
		flush(chunk(map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{
			"index": 0, "id": "call_1", "type": "function",
			"function": map[string]any{"name": "shell", "arguments": string(args)}}}}, nil))
		flush(chunk(map[string]any{}, "tool_calls"))
	} else if code, ok := strings.CutPrefix(user, "CALL "); ok && len(rec.Results) == 0 {
		args, _ := json.Marshal(map[string]string{"code": code})
		flush(chunk(map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{
			"index": 0, "id": "call_1", "type": "function",
			"function": map[string]any{"name": "execute", "arguments": string(args)}}}}, nil))
		flush(chunk(map[string]any{}, "tool_calls"))
	} else {
		answer := "plain answer"
		if n := len(rec.Results); n > 0 {
			answer = "result: " + rec.Results[n-1]
		}
		flush(chunk(map[string]any{"role": "assistant", "content": answer}, nil))
		flush(chunk(map[string]any{}, "stop"))
	}
	_, _ = io.WriteString(w, "data: [DONE]\n\n")
}

func (f *fakeModel) all() []modelRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]modelRequest(nil), f.reqs...)
}

// fakeMCP serves one tool, echo, at any path, and records which path each call reached.
type fakeMCP struct {
	mu    sync.Mutex
	calls []string
}

func (f *fakeMCP) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if len(req.ID) == 0 {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	var result any
	switch req.Method {
	case "initialize":
		result = map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{"tools": map[string]any{}},
			"serverInfo": map[string]any{"name": "fake", "version": "1"}}
	case "tools/list":
		result = map[string]any{"tools": []any{map[string]any{"name": "echo", "description": "Echo.",
			"inputSchema": map[string]any{"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string"}}}}}}
	case "tools/call":
		f.mu.Lock()
		f.calls = append(f.calls, r.URL.Path)
		f.mu.Unlock()
		result = map[string]any{"content": []any{map[string]any{"type": "text", "text": "echo from " + r.URL.Path}}}
	default:
		result = map[string]any{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
}

func startHost(t *testing.T) (*chathost.Host, context.Context) {
	t.Helper()
	bin := os.Getenv("PODIUM_TEST_OPENCODE2")
	if bin == "" {
		t.Skip("PODIUM_TEST_OPENCODE2 is not set: the path of an opencode 2 binary")
	}
	h, err := chathost.New(chathost.Options{Binary: bin, Dir: t.TempDir(),
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = h.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	ready, stop := context.WithTimeout(ctx, time.Minute)
	defer stop()
	require.NoError(t, h.Ready(ready))
	return h, ctx
}

// TestEachConversationSpendsItsOwnCredentialAndReachesItsOwnTools: two conversations on one
// host, each a location naming its own key and its own MCP URL. Each one's model requests carry
// its key, and its tool call reaches its URL, through `execute`.
func TestEachConversationSpendsItsOwnCredentialAndReachesItsOwnTools(t *testing.T) {
	h, ctx := startHost(t)
	model := &fakeModel{}
	modelSrv := httptest.NewServer(model)
	t.Cleanup(modelSrv.Close)
	mcpFake := &fakeMCP{}
	mcpSrv := httptest.NewServer(mcpFake)
	t.Cleanup(mcpSrv.Close)

	for _, who := range []string{"alice", "bob"} {
		require.NoError(t, h.Open(ctx, chathost.Location{
			Name: "conv-" + who,
			Providers: []chathost.Provider{{ID: "fake", Package: "@opencode/ai/providers/openai-compatible",
				BaseURL: modelSrv.URL + "/v1", APIKey: "key-" + who, Models: []string{"m1"}}},
			MCP: []chathost.MCPServer{{Name: "podium", URL: mcpSrv.URL + "/mcp/" + who}},
		}))
	}
	for _, who := range []string{"alice", "bob"} {
		res, err := h.Answer(ctx, chathost.Turn{
			Location: "conv-" + who, Model: chathost.Model{Provider: "fake", ID: "m1"},
			System: "SYSTEM-" + who, Prompt: `CALL return await tools.podium.echo({text: "hi"})`,
		})
		require.NoError(t, err)
		assert.Contains(t, res.Text, "echo from /mcp/"+who, who)
		assert.Equal(t, "succeeded", res.Outcome)
		assert.Equal(t, 2, res.Steps)
	}

	for _, r := range model.all() {
		who := strings.TrimPrefix(r.Auth, "Bearer key-")
		assert.Contains(t, r.System, "SYSTEM-"+who, "each conversation's own system prompt")
		assert.NotContains(t, r.System, "potential user work", "not opencode's coding agent")
		for _, tool := range r.Tools {
			assert.Contains(t, []string{"execute", "skill"}, tool, "no tool but execute is offered")
		}
	}
	assert.ElementsMatch(t, []string{"/mcp/alice", "/mcp/bob"}, mcpFake.calls)
}

// TestTheModelHasNoNetworkAndCannotMoveItsSession: what `execute` must not reach.
func TestTheModelHasNoNetworkAndCannotMoveItsSession(t *testing.T) {
	h, ctx := startHost(t)
	modelSrv := httptest.NewServer(&fakeModel{})
	t.Cleanup(modelSrv.Close)
	require.NoError(t, h.Open(ctx, chathost.Location{Name: "conv",
		Providers: []chathost.Provider{{ID: "fake", Package: "@opencode/ai/providers/openai-compatible",
			BaseURL: modelSrv.URL + "/v1", APIKey: "k", Models: []string{"m1"}}}}))
	for code, want := range map[string]string{
		`return (await fetch("` + modelSrv.URL + `")).status`:           "Permission denied",
		`return await tools.opencode.session_move({directory: "/tmp"})`: "Unknown tool",
		`return await tools.opencode.worktree_create({name: "x"})`:      "Unknown tool",
	} {
		res, err := h.Answer(ctx, chathost.Turn{Location: "conv", Model: chathost.Model{Provider: "fake", ID: "m1"},
			System: "s", Prompt: "CALL " + code})
		require.NoError(t, err)
		assert.Contains(t, res.Text, want, code)
	}
}

// TestTheChatHostRunsInsideTheFence: the service started under the conductor's bubblewrap
// fence — the system read-only, its own directory the only writable path — still answers a
// conversation. PODIUM_TEST_BWRAP is the bwrap binary.
func TestTheChatHostRunsInsideTheFence(t *testing.T) {
	bin, bwrap := os.Getenv("PODIUM_TEST_OPENCODE2"), os.Getenv("PODIUM_TEST_BWRAP")
	if bin == "" || bwrap == "" {
		t.Skip("PODIUM_TEST_OPENCODE2 and PODIUM_TEST_BWRAP are needed")
	}
	fence := (&conductor.HostRuntime{Isolation: conductor.IsolationBwrap, Bwrap: bwrap}).FenceService()
	require.NotNil(t, fence)
	h, err := chathost.New(chathost.Options{Binary: bin, Dir: t.TempDir(), Wrap: fence,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = h.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	ready, stop := context.WithTimeout(ctx, time.Minute)
	defer stop()
	require.NoError(t, h.Ready(ready))

	modelSrv := httptest.NewServer(&fakeModel{})
	t.Cleanup(modelSrv.Close)
	require.NoError(t, h.Open(ctx, chathost.Location{Name: "conv",
		Providers: []chathost.Provider{{ID: "fake", Package: "@opencode/ai/providers/openai-compatible",
			BaseURL: modelSrv.URL + "/v1", APIKey: "k", Models: []string{"m1"}}}}))
	res, err := h.Answer(ctx, chathost.Turn{Location: "conv", Model: chathost.Model{Provider: "fake", ID: "m1"},
		System: "s", Prompt: "hello"})
	require.NoError(t, err)
	assert.Equal(t, "plain answer", res.Text)
}

// TestAShellIsRefusedAndTheFenceHoldsWithoutTheRules: a prompt-injected "run a shell command"
// is refused by the permission rules; and with the rules gone, the fence alone still shows
// the command neither the conductor's files nor any process outside the chat host.
func TestAShellIsRefusedAndTheFenceHoldsWithoutTheRules(t *testing.T) {
	bin, bwrap := os.Getenv("PODIUM_TEST_OPENCODE2"), os.Getenv("PODIUM_TEST_BWRAP")
	if bin == "" || bwrap == "" {
		t.Skip("PODIUM_TEST_OPENCODE2 and PODIUM_TEST_BWRAP are needed")
	}
	// What the conductor has that a turn must never see: its configuration, and itself.
	confDir := t.TempDir()
	conf := filepath.Join(confDir, "conductor.env")
	require.NoError(t, os.WriteFile(conf, []byte("PODIUM_AGENT_TOKEN=CONDUCTOR-SECRET\n"), 0o600))
	home, _ := os.UserHomeDir()
	probe := fmt.Sprintf("cat %s; test -e /proc/%d && echo SEES-CONDUCTOR-PID; test -e %s && echo SEES-HOME; echo procs=$(ls /proc | grep -c '^[0-9]'); echo done",
		conf, os.Getpid(), home)

	fence := (&conductor.HostRuntime{Isolation: conductor.IsolationBwrap, Bwrap: bwrap}).FenceService()
	for _, tc := range []struct {
		name   string
		denied []string
	}{{"with the rules", nil}, {"without the rules", []string{}}} {
		t.Run(tc.name, func(t *testing.T) {
			h, err := chathost.New(chathost.Options{Binary: bin, Dir: t.TempDir(), Wrap: fence, Denied: tc.denied,
				Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
			require.NoError(t, err)
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			go func() { defer close(done); _ = h.Run(ctx) }()
			t.Cleanup(func() { cancel(); <-done })
			ready, stop := context.WithTimeout(ctx, time.Minute)
			defer stop()
			require.NoError(t, h.Ready(ready))
			modelSrv := httptest.NewServer(&fakeModel{})
			t.Cleanup(modelSrv.Close)
			require.NoError(t, h.Open(ctx, chathost.Location{Name: "conv",
				Providers: []chathost.Provider{{ID: "fake", Package: "@opencode/ai/providers/openai-compatible",
					BaseURL: modelSrv.URL + "/v1", APIKey: "k", Models: []string{"m1"}}}}))
			res, err := h.Answer(ctx, chathost.Turn{Location: "conv", Model: chathost.Model{Provider: "fake", ID: "m1"},
				System: "s", Prompt: "SHELL " + probe})
			require.NoError(t, err)
			out := res.Text
			assert.NotContains(t, out, "CONDUCTOR-SECRET")
			assert.NotContains(t, out, "SEES-CONDUCTOR-PID")
			assert.NotContains(t, out, "SEES-HOME")
			if tc.denied == nil {
				assert.NotContains(t, out, "done", "the shell never ran")
				return
			}
			require.Contains(t, out, "done", "without the rules the shell ran, inside the fence")
			assert.Contains(t, out, "No such file", "the conductor's configuration is not there to read")
			var procs int
			_, err = fmt.Sscanf(out[strings.Index(out, "procs="):], "procs=%d", &procs)
			require.NoError(t, err)
			entries, err := os.ReadDir("/proc")
			require.NoError(t, err)
			outside := 0
			for _, e := range entries {
				if e.Name()[0] >= '0' && e.Name()[0] <= '9' {
					outside++
				}
			}
			assert.Less(t, procs, outside, "a process namespace of its own: only the chat host's processes")
			t.Logf("fenced shell said: %s", out)
		})
	}
}

// TestAConversationReopensAfterItWasClosed: a conversation quiet for a while is closed and its
// directory removed. opencode keeps the location alive and its file watcher reloads it with no
// config — no MCP servers. Reopening writes the config again, under a new turn's token, and the
// location must pick it up: it did not, and the turn waited out 30s for tools that were never
// coming.
func TestAConversationReopensAfterItWasClosed(t *testing.T) {
	h, ctx := startHost(t)
	modelSrv := httptest.NewServer(&fakeModel{})
	t.Cleanup(modelSrv.Close)
	mcpFake := &fakeMCP{}
	mcpSrv := httptest.NewServer(mcpFake)
	t.Cleanup(mcpSrv.Close)
	loc := func(token string) chathost.Location {
		return chathost.Location{Name: "conv",
			Providers: []chathost.Provider{{ID: "fake", Package: "@opencode/ai/providers/openai-compatible",
				BaseURL: modelSrv.URL + "/v1", APIKey: "k", Models: []string{"m1"}}},
			MCP: []chathost.MCPServer{{Name: "podium", URL: mcpSrv.URL + "/mcp/" + token}}}
	}

	require.NoError(t, h.Open(ctx, loc("first")))
	require.NoError(t, h.Close("conv"))
	// opencode's watcher sees the directory go and reloads the location empty.
	time.Sleep(3 * time.Second)

	open, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	require.NoError(t, h.Open(open, loc("second")), "reopening connects the new turn's tools")
	res, err := h.Answer(ctx, chathost.Turn{Location: "conv", Model: chathost.Model{Provider: "fake", ID: "m1"},
		System: "s", Prompt: `CALL return await tools.podium.echo({text: "hi"})`})
	require.NoError(t, err)
	assert.Contains(t, res.Text, "echo from /mcp/second")
}
