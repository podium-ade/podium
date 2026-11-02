//go:build integration

package conductor_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/agent/chathost"
	"github.com/podium-ade/podium/internal/agent/conductor"
	"github.com/podium-ade/podium/internal/agent/conductor/fakesource"
	"github.com/podium-ade/podium/internal/agent/store"
)

// fakeAnthropic is Anthropic's Messages API, streamed. A user message "CALL <code>" is
// answered with an `execute` tool call running that code; once a tool result is in the
// conversation, the answer is that result.
type fakeAnthropic struct {
	mu   sync.Mutex
	keys []string
	// delay is how long every answer takes, as a model's would.
	delay time.Duration
	// arrived is when each request reached the model.
	arrived []time.Time
}

func (f *fakeAnthropic) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.keys = append(f.keys, r.Header.Get("x-api-key")+"|"+r.Header.Get("Authorization"))
	f.arrived = append(f.arrived, time.Now())
	f.mu.Unlock()
	var req struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	var user, result string
	for _, m := range req.Messages {
		var text string
		if json.Unmarshal(m.Content, &text) != nil {
			var blocks []struct {
				Type    string          `json:"type"`
				Text    string          `json:"text"`
				Content json.RawMessage `json:"content"`
			}
			_ = json.Unmarshal(m.Content, &blocks)
			for _, b := range blocks {
				switch b.Type {
				case "text":
					text += b.Text
				case "tool_result":
					result = string(b.Content)
				}
			}
		}
		if m.Role == "user" && text != "" {
			user = text
		}
	}
	if strings.Contains(user, "SLOW") && result == "" {
		time.Sleep(2 * time.Second)
	}
	time.Sleep(f.delay)
	w.Header().Set("Content-Type", "text/event-stream")
	send := func(event string, v any) {
		b, _ := json.Marshal(v)
		_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b)
		w.(http.Flusher).Flush()
	}
	send("message_start", map[string]any{"type": "message_start", "message": map[string]any{
		"id": "msg_1", "type": "message", "role": "assistant", "model": "claude-test", "content": []any{},
		"stop_reason": nil, "usage": map[string]any{"input_tokens": 10, "output_tokens": 1}}})
	stop := "end_turn"
	if code, ok := strings.CutPrefix(user, "CALL "); ok && result == "" {
		args, _ := json.Marshal(map[string]string{"code": code})
		send("content_block_start", map[string]any{"type": "content_block_start", "index": 0,
			"content_block": map[string]any{"type": "tool_use", "id": "toolu_1", "name": "execute", "input": map[string]any{}}})
		send("content_block_delta", map[string]any{"type": "content_block_delta", "index": 0,
			"delta": map[string]any{"type": "input_json_delta", "partial_json": string(args)}})
		stop = "tool_use"
	} else {
		answer := "answer to: " + user
		if result != "" {
			answer = "result: " + result
		}
		send("content_block_start", map[string]any{"type": "content_block_start", "index": 0,
			"content_block": map[string]any{"type": "text", "text": ""}})
		send("content_block_delta", map[string]any{"type": "content_block_delta", "index": 0,
			"delta": map[string]any{"type": "text_delta", "text": answer}})
	}
	send("content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
	send("message_delta", map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": stop},
		"usage": map[string]any{"output_tokens": 3}})
	send("message_stop", map[string]any{"type": "message_stop"})
}

func (f *fakeAnthropic) seen() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.keys...)
}

// startChatHost runs a real chat host for a test, and the server its gateway and tools are
// on, whose conductor is filled in once there is one.
func startChatHost(t *testing.T) (*chathost.Host, *httptest.Server, *atomic.Pointer[conductor.Conductor]) {
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

	var cond atomic.Pointer[conductor.Conductor]
	surface := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c := cond.Load()
		if c == nil {
			http.Error(w, "no conductor yet", http.StatusServiceUnavailable)
			return
		}
		switch {
		case strings.HasPrefix(r.URL.Path, "/chat-host/gateway/"):
			c.ChatHostGateway().ServeHTTP(w, r)
		case strings.HasPrefix(r.URL.Path, "/chat-host/mcp/"):
			c.ChatHostMCP(nil).ServeHTTP(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(surface.Close)
	return h, surface, &cond
}

// TestAChatHostTurnDelegatesThroughTheConductorsTools: a conversation answered on the chat
// host, with no runtime process. The model's only way to act is `execute`; its delegation
// reaches the conductor's tool server for its own conversation and becomes a task; its model
// requests reach the provider through the gateway with the conductor's credential, never the
// placeholder opencode was given.
func TestAChatHostTurnDelegatesThroughTheConductorsTools(t *testing.T) {
	h, surface, holder := startChatHost(t)
	model := &fakeAnthropic{}
	modelSrv := httptest.NewServer(model)
	t.Cleanup(modelSrv.Close)

	st := newStore(t)
	fake := newFakePodium(t)
	src := fakesource.New(conductor.KindDev)
	t.Cleanup(src.Close)

	host := hostRuntime(t, "sk-test")
	host.TurnURL = "http://unused.invalid"
	r := startWith(t, st, fake, src, func(o *conductor.Options) {
		o.Host = host
		o.ChatHost = h
		o.ChatHostURL = surface.URL
		o.AnthropicBaseURL = modelSrv.URL
		o.ChatHostRender = func(_ context.Context, _ string) (string, error) { return "You are the test assistant.", nil }
	})
	holder.Store(r.cond)

	ev := inbound("C1/20.1", `CALL return await tools.podium.delegate({playbook: "coder", instruction: "fix the build"})`)
	require.NoError(t, src.Send(context.Background(), ev))
	waitFor(t, 90*time.Second, "the chat-host turn to finish", func() bool {
		return turnStatus(st, ev.SourceKey) == store.TurnSucceeded
	})

	require.Len(t, fake.Specs(), 1, "the delegation became a task, and the turn itself made none")
	finals := posts(src.Records(), conductor.OutFinal)
	require.NotEmpty(t, finals)
	assert.Contains(t, finals[len(finals)-1].Text, "Delegated to `coder`")

	keys := model.seen()
	require.NotEmpty(t, keys)
	for _, k := range keys {
		assert.Equal(t, "sk-test-for-anthropic|", k, "the gateway's credential, and only it")
	}
}

// TestAQuietConversationIsSummarisedAndTheSummaryReplacesItsHistory: a conversation long
// enough to be worth it goes quiet, the summary job writes its summary on the chat host, and
// from then on a turn is handed the summary and the latest messages rather than all of them.
func TestAQuietConversationIsSummarisedAndTheSummaryReplacesItsHistory(t *testing.T) {
	h, surface, holder := startChatHost(t)
	model := &fakeAnthropic{}
	modelSrv := httptest.NewServer(model)
	t.Cleanup(modelSrv.Close)

	st := newStore(t)
	fake := newFakePodium(t)
	src := fakesource.New(conductor.KindDev)
	t.Cleanup(src.Close)
	host := hostRuntime(t, "sk-test")
	r := startWith(t, st, fake, src, func(o *conductor.Options) {
		o.Host = host
		o.ChatHost = h
		o.ChatHostURL = surface.URL
		o.AnthropicBaseURL = modelSrv.URL
		o.ChatHostRender = func(_ context.Context, _ string) (string, error) { return "You are the test assistant.", nil }
	})
	holder.Store(r.cond)

	ref := "C1/30.1"
	for i := range 8 {
		ev := inbound(ref, fmt.Sprintf("message %d", i))
		before := len(posts(src.Records(), conductor.OutFinal))
		require.NoError(t, src.Send(context.Background(), ev))
		waitFor(t, 60*time.Second, "the turn to answer", func() bool {
			return len(posts(src.Records(), conductor.OutFinal)) > before && turnStatus(st, ev.SourceKey) == store.TurnSucceeded
		})
	}
	sess, err := st.GetSessionByKey(context.Background(), inbound(ref, "").SourceKey)
	require.NoError(t, err)
	entries, err := src.FetchTranscript(context.Background(), ref)
	require.NoError(t, err)
	require.Len(t, entries, 16)

	r.cond.SummariseNowForTest(context.Background())

	handed := r.cond.WithSummaryForTest(context.Background(), sess.ID, entries)
	require.Len(t, handed, 1+8, "the summary, then the eight latest messages word for word")
	assert.Equal(t, "summary", handed[0].Author)
	assert.Contains(t, handed[0].Text, "answer to:", "what the model wrote as the summary")
	assert.Equal(t, entries[8:], handed[1:])
}

// TestAMessageDuringAChatHostTurnIsAnsweredInIt: a message that arrives while the turn is
// answering goes into that turn, which answers it too — two answers, one turn.
func TestAMessageDuringAChatHostTurnIsAnsweredInIt(t *testing.T) {
	h, surface, holder := startChatHost(t)
	model := &fakeAnthropic{}
	modelSrv := httptest.NewServer(model)
	t.Cleanup(modelSrv.Close)

	st := newStore(t)
	fake := newFakePodium(t)
	src := fakesource.New(conductor.KindDev)
	t.Cleanup(src.Close)
	r := startWith(t, st, fake, src, func(o *conductor.Options) {
		o.Host = hostRuntime(t, "sk-test")
		o.ChatHost = h
		o.ChatHostURL = surface.URL
		o.AnthropicBaseURL = modelSrv.URL
		o.ChatHostRender = func(_ context.Context, _ string) (string, error) { return "You are the test assistant.", nil }
	})
	holder.Store(r.cond)

	first := inbound("C1/50.1", "SLOW first question")
	require.NoError(t, src.Send(context.Background(), first))
	waitFor(t, 30*time.Second, "the first answer to be under way", func() bool { return len(model.seen()) > 0 })
	require.NoError(t, src.Send(context.Background(), inbound("C1/50.1", "and a second one")))

	waitFor(t, 60*time.Second, "both answers", func() bool {
		return len(posts(src.Records(), conductor.OutFinal)) == 2 && turnStatus(st, first.SourceKey) == store.TurnSucceeded
	})
	finals := posts(src.Records(), conductor.OutFinal)
	assert.Contains(t, finals[0].Text, "SLOW first question")
	assert.Contains(t, finals[1].Text, "and a second one")
	sess, err := st.GetSessionByKey(context.Background(), first.SourceKey)
	require.NoError(t, err)
	turns, err := st.ListTurns(context.Background(), sess.ID, 10)
	require.NoError(t, err)
	assert.Len(t, turns, 1, "the second message was answered inside the first turn")
}

// TestThreeHundredChatHostTurnsInFlight is the chat host's sizing check: 300 conversations
// answering at once on one host, through the conductor, its gateway and the chat host, against
// a model that takes 3s to answer. The 95th-percentile turn is held within 2s of that.
// PODIUM_TEST_CHATHOST_LOAD=1 runs it; it is too heavy for every run.
func TestThreeHundredChatHostTurnsInFlight(t *testing.T) {
	if os.Getenv("PODIUM_TEST_CHATHOST_LOAD") == "" {
		t.Skip("PODIUM_TEST_CHATHOST_LOAD is not set")
	}
	const n, modelTime = 300, 3 * time.Second
	h, surface, holder := startChatHost(t)
	model := &fakeAnthropic{delay: modelTime}
	modelSrv := httptest.NewServer(model)
	t.Cleanup(modelSrv.Close)
	st := newStore(t)
	fake := newFakePodium(t)
	src := fakesource.New(conductor.KindDev)
	t.Cleanup(src.Close)
	r := startWith(t, st, fake, src, func(o *conductor.Options) {
		o.Host = hostRuntime(t, "sk-test")
		o.ChatHost = h
		o.ChatHostURL = surface.URL
		o.AnthropicBaseURL = modelSrv.URL
		o.ChatHostRender = func(_ context.Context, _ string) (string, error) { return "You are the test assistant.", nil }
		if path := os.Getenv("PODIUM_TEST_CHATHOST_LOAD_LOG"); path != "" {
			f, err := os.Create(path)
			require.NoError(t, err)
			t.Cleanup(func() { _ = f.Close() })
			o.Logger = slog.New(slog.NewTextHandler(f, &slog.HandlerOptions{Level: slog.LevelWarn}))
		}
	})
	holder.Store(r.cond)

	ref := func(i int) string { return fmt.Sprintf("L%d/1.1", i) }
	finals := func() map[string]int {
		out := map[string]int{}
		for _, rec := range posts(src.Records(), conductor.OutFinal) {
			out[rec.Ref]++
		}
		return out
	}
	round := func(k int) []time.Duration {
		start := time.Now()
		for i := range n {
			require.NoError(t, src.Send(context.Background(), inbound(ref(i), fmt.Sprintf("question %d", k))))
		}
		took := make([]time.Duration, n)
		done := 0
		for deadline := time.Now().Add(3 * time.Minute); done < n; time.Sleep(50 * time.Millisecond) {
			if time.Now().After(deadline) {
				fails := map[string]int{}
				for _, rec := range posts(src.Records(), conductor.OutFailure) {
					fails[rec.Text]++
				}
				statuses := map[string]int{}
				for i := range n {
					statuses[turnStatus(st, inbound(ref(i), "").SourceKey)]++
				}
				t.Fatalf("only %d of %d turns answered; failures %v; turn statuses %v", done, n, fails, statuses)
			}
			got := finals()
			for i := range n {
				if took[i] == 0 && got[ref(i)] >= k {
					took[i] = time.Since(start)
					done++
				}
			}
		}
		sort.Slice(took, func(a, b int) bool { return took[a] < took[b] })
		return took
	}
	warm := round(1) // every conversation's location booted
	t.Logf("first turns (locations booting): p50 %s p95 %s max %s", warm[n/2], warm[n*95/100], warm[n-1])
	before := len(model.seen())
	roundStart := time.Now()
	took := round(2)
	model.mu.Lock()
	var arr []time.Duration
	for _, at := range model.arrived[before:] {
		arr = append(arr, at.Sub(roundStart))
	}
	model.mu.Unlock()
	sort.Slice(arr, func(a, b int) bool { return arr[a] < arr[b] })
	t.Logf("model requests arrived: first %s p50 %s p95 %s last %s", arr[0], arr[len(arr)/2], arr[len(arr)*95/100], arr[len(arr)-1])
	t.Logf("model requests in the measured round: %d for %d turns", len(model.seen())-before, n)
	p95 := took[n*95/100]
	t.Logf("%d turns in flight: p50 %s p95 %s max %s", n, took[n/2], p95, took[n-1])
	assert.LessOrEqual(t, p95, modelTime+2*time.Second, "p95 within 2s of the model's own latency")
}
