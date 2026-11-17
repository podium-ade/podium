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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/agent/chathost"
)

// fakeAnthropic is Anthropic's Messages API as Claude Code calls it: every request is
// recorded, and every message is answered "plain answer".
type fakeAnthropic struct {
	mu   sync.Mutex
	reqs []anthropicRequest
}

type anthropicRequest struct {
	Path, Auth, APIKey, Model string
	Stream                    bool
}

func (f *fakeAnthropic) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	f.mu.Lock()
	f.reqs = append(f.reqs, anthropicRequest{Path: r.URL.Path, Auth: r.Header.Get("Authorization"),
		APIKey: r.Header.Get("x-api-key"), Model: body.Model, Stream: body.Stream})
	f.mu.Unlock()
	if !strings.HasSuffix(r.URL.Path, "/v1/messages") {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"input_tokens": 10}`)
		return
	}
	usage := map[string]any{"input_tokens": 10, "output_tokens": 3}
	if !body.Stream {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "msg_1", "type": "message", "role": "assistant",
			"model": body.Model, "content": []any{map[string]any{"type": "text", "text": "plain answer"}},
			"stop_reason": "end_turn", "usage": usage})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	send := func(event string, v any) {
		b, _ := json.Marshal(v)
		_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b)
		w.(http.Flusher).Flush()
	}
	send("message_start", map[string]any{"type": "message_start", "message": map[string]any{
		"id": "msg_1", "type": "message", "role": "assistant", "model": body.Model, "content": []any{},
		"stop_reason": nil, "stop_sequence": nil, "usage": map[string]any{"input_tokens": 10, "output_tokens": 1}}})
	send("content_block_start", map[string]any{"type": "content_block_start", "index": 0,
		"content_block": map[string]any{"type": "text", "text": ""}})
	send("content_block_delta", map[string]any{"type": "content_block_delta", "index": 0,
		"delta": map[string]any{"type": "text_delta", "text": "plain answer"}})
	send("content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
	send("message_delta", map[string]any{"type": "message_delta",
		"delta": map[string]any{"stop_reason": "end_turn", "stop_sequence": nil}, "usage": map[string]any{"output_tokens": 3}})
	send("message_stop", map[string]any{"type": "message_stop"})
}

func (f *fakeAnthropic) all() []anthropicRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]anthropicRequest(nil), f.reqs...)
}

// TestAClaudeAgentConversationRunsClaudeCodeThroughTheGateway: a location naming opencode's
// built-in claude-agent provider runs Claude Code, whose model requests go to the location's
// base URL with the location's placeholder as a bearer — the shape the gateway replaces.
func TestAClaudeAgentConversationRunsClaudeCodeThroughTheGateway(t *testing.T) {
	bin, claude := os.Getenv("PODIUM_TEST_OPENCODE2"), os.Getenv("PODIUM_TEST_CLAUDE")
	if bin == "" || claude == "" {
		t.Skip("PODIUM_TEST_OPENCODE2 and PODIUM_TEST_CLAUDE are not both set: an opencode 2 and a Claude Code binary")
	}
	h, err := chathost.New(chathost.Options{Binary: bin, Claude: claude, Dir: t.TempDir(), Processes: 1,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = h.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	ready, stop := context.WithTimeout(ctx, time.Minute)
	defer stop()
	require.NoError(t, h.Ready(ready))

	model := &fakeAnthropic{}
	srv := httptest.NewServer(model)
	t.Cleanup(srv.Close)

	require.NoError(t, h.Open(ctx, chathost.Location{Name: "conv-claude", Providers: []chathost.Provider{{
		ID: "claude-agent", Builtin: true, BaseURL: srv.URL + "/gw/tok", AuthToken: "podium",
		Models: []string{"claude-opus-5-5"},
	}}}))
	turn, stopTurn := context.WithTimeout(ctx, 3*time.Minute)
	defer stopTurn()
	res, err := h.Answer(turn, chathost.Turn{
		Location: "conv-claude", Model: chathost.Model{Provider: "claude-agent", ID: "claude-opus-5-5"},
		System: "SYSTEM", Prompt: "hello",
	})
	require.NoError(t, err)
	t.Logf("outcome=%s error=%q text=%q requests=%+v", res.Outcome, res.Error, res.Text, model.all())
	assert.Equal(t, "succeeded", res.Outcome)
	assert.Contains(t, res.Text, "plain answer")

	reqs := model.all()
	require.NotEmpty(t, reqs, "Claude Code reached the location's base URL")
	for _, r := range reqs {
		assert.True(t, strings.HasPrefix(r.Path, "/gw/tok/"), r.Path)
		assert.Equal(t, "Bearer podium", r.Auth, "the placeholder is a bearer, for the gateway to replace")
		assert.Empty(t, r.APIKey)
	}
}
