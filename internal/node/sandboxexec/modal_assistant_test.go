//go:build modal

package sandboxexec

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/node/docker"
	"github.com/podium-ade/podium/internal/sandbox/modal"
	"github.com/podium-ade/podium/pkg/spec"
)

// modalRuntimeImage is the agent runtime the assistant runs in on a provider.
const modalRuntimeImage = "ghcr.io/podium-ade/podium-agent-runtime:0.7.3"

// TestTheAssistantOnModalFromThePoolAndFromScratch places the assistant on Modal the way a
// sandbox node does: a pool of one sandbox of the runtime image, and a turn's task of that
// shape. It times, from the node being handed the task, the runtime's first word on stderr and
// its first message through podium-runner — once from the pooled sandbox and once from one made
// for the task — and checks the message reached the node, which is what a process task on Modal
// could not do before the runner kept its socket path.
//
// MODAL_CONFORMANCE=1 with Modal credentials, and BOAT_TEST_AGENT a linux/amd64
// podium-sandbox-agent with podium-runner embedded.
func TestTheAssistantOnModalFromThePoolAndFromScratch(t *testing.T) {
	agent := os.Getenv("BOAT_TEST_AGENT")
	if os.Getenv("MODAL_CONFORMANCE") == "" || agent == "" {
		t.Skip("MODAL_CONFORMANCE and BOAT_TEST_AGENT are needed")
	}
	p, err := modal.New(modal.Config{App: "podium-sandbox-dev", CPU: 0.5, MemoryMB: 1024})
	require.NoError(t, err)
	e, err := New(Options{
		Provider: p, DataDir: t.TempDir(), Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Agent: func() ([]byte, error) { return os.ReadFile(agent) },
		Pool:  PoolOptions{Size: 1, Image: modalRuntimeImage, TTL: 20 * time.Minute},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = e.Close() })
	fillStart := time.Now()
	for deadline := time.Now().Add(10 * time.Minute); e.PoolReady() != 1; time.Sleep(time.Second) {
		require.True(t, time.Now().Before(deadline), "the pool never filled")
	}
	t.Logf("pool filled in %s (includes Modal building the image the first time)", time.Since(fillStart).Round(time.Second))

	// The model is a public URL that is not one, so the turn fails fast after its first request
	// and says so through the runner. Modal cannot reach this machine to record the request.
	brief := map[string]any{
		"version": 1, "session_id": "sess_m", "turn_id": "turn_m",
		"source":     map[string]any{"kind": "chat", "ref": "chat_m"},
		"profile":    map[string]any{"name": "podium", "display_name": "Podium", "system_prompt": "be brief", "model": "gpt-5"},
		"playbook":   map[string]any{"name": "assistant", "system_prompt": "answer", "allowed_tools": []string{"webfetch", "todoread", "todowrite"}},
		"transcript": []any{}, "transcript_truncated": false, "instruction": "hello",
		"provider": map[string]any{"id": "openai", "api_key_env": "OPENAI_API_KEY", "base_url": "https://example.com/v1"},
	}
	raw, err := json.Marshal(brief)
	require.NoError(t, err)

	type timing struct{ started, firstWord, firstMessage time.Duration }
	turn := func(id, image string, resources spec.Resources) timing {
		sp := spec.TaskSpec{
			Image:     image,
			Command:   []string{"node", "/opt/podium-agent/dist/main.js"},
			Env:       map[string]string{"PODIUM_AGENT_TURN": base64.StdEncoding.EncodeToString(raw), "OPENAI_API_KEY": "sk-test"},
			Resources: resources,
			Timeout:   spec.Duration(10 * time.Minute),
		}
		sp.ApplyDefaults()
		ch := make(chan docker.Event, 512)
		var mu sync.Mutex
		var tm timing
		var said []string
		start := time.Now()
		done := make(chan struct{})
		go func() {
			defer close(done)
			for ev := range ch {
				mu.Lock()
				switch pl := ev.Payload.(type) {
				case docker.LogPayload:
					if tm.firstWord == 0 && strings.Contains(string(pl.Bytes), "starting") {
						tm.firstWord = time.Since(start)
					}
				case docker.MessagePayload:
					if tm.firstMessage == 0 {
						tm.firstMessage = time.Since(start)
					}
					said = append(said, pl.Type)
				}
				if ev.Kind == docker.KindStarted && tm.started == 0 {
					tm.started = time.Since(start)
				}
				mu.Unlock()
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		res, err := e.Run(ctx, docker.Request{TaskID: id, LeaseID: "lease-" + id + "-" + time.Now().Format("150405"), Spec: sp}, ch)
		close(ch)
		<-done
		t.Cleanup(func() { _ = e.Teardown(context.Background(), id, false) })
		require.NoError(t, err)
		mu.Lock()
		defer mu.Unlock()
		require.NotEmpty(t, said, "%s: the runtime's messages never reached the node (exit %d)", id, res.ExitCode)
		assert.Contains(t, said, "final")
		return tm
	}

	pooled := turn("modal-assistant-pooled", modalRuntimeImage, spec.Resources{})
	// The same image, sized differently so it is not the pool's shape: made for the task, with
	// the image already built on Modal by the pool.
	made := turn("modal-assistant-made", modalRuntimeImage, spec.Resources{CPU: 1, MemoryMB: 2048})
	t.Logf("pooled sandbox: command started %s, runtime's first word %s, first message %s",
		pooled.started.Round(10*time.Millisecond), pooled.firstWord.Round(10*time.Millisecond), pooled.firstMessage.Round(10*time.Millisecond))
	t.Logf("made for the task: command started %s, runtime's first word %s, first message %s",
		made.started.Round(10*time.Millisecond), made.firstWord.Round(10*time.Millisecond), made.firstMessage.Round(10*time.Millisecond))
	assert.Less(t, pooled.started, made.started, "a pooled sandbox starts its task sooner")
}
