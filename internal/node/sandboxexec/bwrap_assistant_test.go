package sandboxexec

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/node/docker"
	"github.com/podium-ade/podium/internal/sandbox/sandboxtest"
	"github.com/podium-ade/podium/pkg/spec"
)

// TestTheAssistantRunsInASandboxAndAPooledOneAnswersSooner runs the real agent runtime and its
// harness as the assistant would run on a provider: in a sandbox, with the assistant's tools, its
// model a stub that records when the first request arrives. It measures that wait twice — from a
// pooled sandbox and from one made for the task — which is the number a placement is chosen by.
//
// PODIUM_TEST_RUNTIME is the built runtime's dist/main.js; the bwrap and agent variables are
// TestASandboxNodeRunsATaskFromItsPoolEndToEnd's.
func TestTheAssistantRunsInASandboxAndAPooledOneAnswersSooner(t *testing.T) {
	bin, agent, runtime := os.Getenv("PODIUM_TEST_BWRAP"), os.Getenv("PODIUM_TEST_SANDBOX_AGENT"), os.Getenv("PODIUM_TEST_RUNTIME")
	if bin == "" || agent == "" || runtime == "" {
		t.Skip("set PODIUM_TEST_BWRAP, PODIUM_TEST_SANDBOX_AGENT and PODIUM_TEST_RUNTIME")
	}
	// One model per run, by path: a run whose harness keeps retrying must not be mistaken for
	// the next run's first request.
	var firstsMu sync.Mutex
	firsts := map[string]chan time.Time{}
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/"), "/", 2)[0]
		firstsMu.Lock()
		ch := firsts[id]
		firstsMu.Unlock()
		if ch != nil {
			select {
			case ch <- time.Now():
			default:
			}
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(model.Close)

	node, err := exec.LookPath("node")
	require.NoError(t, err)
	node, _ = filepath.EvalSymlinks(node)
	prefix, _ := exec.Command("npm", "prefix", "-g").Output()
	p := &sandboxtest.Bwrap{Dir: t.TempDir(), Bin: bin, Ro: []string{
		filepath.Dir(filepath.Dir(node)), filepath.Dir(filepath.Dir(runtime)), strings.TrimSpace(string(prefix)),
	}}
	e, err := New(Options{
		Provider: p, DataDir: t.TempDir(), Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Agent: func() ([]byte, error) { return os.ReadFile(agent) },
		Pool:  PoolOptions{Size: 1, Image: "podium-agent-runtime:assistant"},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = e.Close() })
	for deadline := time.Now().Add(30 * time.Second); e.PoolReady() != 1; time.Sleep(50 * time.Millisecond) {
		require.True(t, time.Now().Before(deadline), "the pool never filled")
	}

	briefFor := func(id string) map[string]any {
		return map[string]any{
			"version": 1, "session_id": "sess_1", "turn_id": "turn_1",
			"source":     map[string]any{"kind": "chat", "ref": "chat_1"},
			"profile":    map[string]any{"name": "podium", "display_name": "Podium", "system_prompt": "be brief", "model": "gpt-5"},
			"playbook":   map[string]any{"name": "assistant", "system_prompt": "", "allowed_tools": []string{"webfetch", "todoread", "todowrite"}},
			"transcript": []any{}, "transcript_truncated": false, "instruction": "hello",
			"provider": map[string]any{"id": "openai", "api_key_env": "OPENAI_API_KEY", "base_url": model.URL + "/" + id + "/v1"},
		}
	}

	wait := func(id, image string) time.Duration {
		first := make(chan time.Time, 1)
		firstsMu.Lock()
		firsts[id] = first
		firstsMu.Unlock()
		raw, err := json.Marshal(briefFor(id))
		require.NoError(t, err)
		sp := spec.TaskSpec{
			Image: image,
			// The runtime image has opencode on the PATH a process task gets; this machine has
			// it where npm put it, so the command says where.
			Command: []string{"sh", "-c", "export PATH=" + filepath.Dir(node) + ":" +
				strings.TrimSpace(string(prefix)) + "/bin:$PATH; exec node " + runtime},
			Env: map[string]string{
				"PODIUM_AGENT_TURN": base64.StdEncoding.EncodeToString(raw),
				"OPENAI_API_KEY":    "sk-test",
			},
		}
		sp.ApplyDefaults()
		ch := make(chan docker.Event, 512)
		var logs strings.Builder
		var mu sync.Mutex
		go func() {
			for ev := range ch {
				mu.Lock()
				if pl, ok := ev.Payload.(docker.LogPayload); ok {
					logs.Write(pl.Bytes)
				} else if pl, ok := ev.Payload.(docker.ErrorPayload); ok {
					logs.WriteString("ERROR " + pl.Message + "\n")
				}
				mu.Unlock()
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		start := time.Now()
		runErr := make(chan error, 1)
		go func() {
			_, err := e.Run(ctx, docker.Request{TaskID: id, LeaseID: "lease-" + id, Spec: sp}, ch)
			runErr <- err
		}()
		t.Cleanup(func() { _ = e.Teardown(context.Background(), id, false) })
		select {
		case at := <-first:
			return at.Sub(start)
		case err := <-runErr:
			time.Sleep(200 * time.Millisecond)
			mu.Lock()
			defer mu.Unlock()
			t.Fatalf("%s ended before asking its model anything: %v\n%s", id, err, logs.String())
		case <-time.After(90 * time.Second):
			mu.Lock()
			defer mu.Unlock()
			t.Fatalf("%s: the assistant never asked its model anything\n%s", id, logs.String())
		}
		return 0
	}

	pooled := wait("assistant-pooled", "podium-agent-runtime:assistant")
	cold := wait("assistant-cold", "podium-agent-runtime:other")
	t.Logf("first model request: pooled sandbox %s, sandbox made for the task %s", pooled.Round(10*time.Millisecond), cold.Round(10*time.Millisecond))
	require.Positive(t, pooled)
	assert.Less(t, pooled, cold, "a pooled sandbox has already been made, readied and given its agent")
}
