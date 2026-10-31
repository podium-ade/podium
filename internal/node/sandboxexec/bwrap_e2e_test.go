package sandboxexec

import (
	"context"
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

	"github.com/podium-ade/podium/internal/node/docker"
	"github.com/podium-ade/podium/internal/sandbox/sandboxtest"
	"github.com/podium-ade/podium/pkg/spec"
)

// TestASandboxNodeRunsATaskFromItsPoolEndToEnd is the whole sandbox path with no Docker engine
// and no cloud account: the executor, its pool, the real podium-sandbox-agent and podium-runner,
// on a provider whose sandboxes are bubblewrap namespaces. A task of the pool's shape runs in a
// sandbox that was ready before it was asked for, reaches the conductor's turn surface from
// inside it, and reports its output and its exit; a task of another shape gets its own.
//
// PODIUM_TEST_BWRAP is bubblewrap, PODIUM_TEST_SANDBOX_AGENT podium-sandbox-agent built with
// podium-runner embedded (make runner-embed, then go build ./cmd/podium-sandbox-agent).
func TestASandboxNodeRunsATaskFromItsPoolEndToEnd(t *testing.T) {
	bin, agent := os.Getenv("PODIUM_TEST_BWRAP"), os.Getenv("PODIUM_TEST_SANDBOX_AGENT")
	if bin == "" || agent == "" {
		t.Skip("set PODIUM_TEST_BWRAP and PODIUM_TEST_SANDBOX_AGENT to run the sandbox path end to end")
	}
	// The conductor's turn surface, as PODIUM_AGENT_SANDBOX_URL points a sandbox at it.
	surface := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "surface reached at "+r.URL.Path)
	}))
	t.Cleanup(surface.Close)

	p := &sandboxtest.Bwrap{Dir: t.TempDir(), Bin: bin}
	e, err := New(Options{
		Provider: p, DataDir: t.TempDir(), Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Agent: func() ([]byte, error) { return os.ReadFile(agent) },
		Pool:  PoolOptions{Size: 1, Image: "podium-agent-runtime:test"},
	})
	require.NoError(t, err)
	deadline := time.Now().Add(30 * time.Second)
	for e.PoolReady() != 1 {
		require.True(t, time.Now().Before(deadline), "the pool never filled")
		time.Sleep(50 * time.Millisecond)
	}
	pooled := p.IDs()

	saidBy := map[string][]string{}
	run := func(id, image, script string) (docker.Result, string, string) {
		sp := spec.TaskSpec{
			Image:   image,
			Command: []string{"sh", "-c", script},
			Env:     map[string]string{"SURFACE": surface.URL},
		}
		sp.ApplyDefaults()
		ch := make(chan docker.Event, 256)
		var mu sync.Mutex
		var stdout strings.Builder
		var sandboxID string
		var said []string
		done := make(chan struct{})
		go func() {
			defer close(done)
			for ev := range ch {
				mu.Lock()
				switch pl := ev.Payload.(type) {
				case docker.LogPayload:
					if pl.Stream == docker.StreamStdout {
						stdout.Write(pl.Bytes)
					}
				case docker.SandboxPayload:
					sandboxID = pl.ID
				case docker.MessagePayload:
					said = append(said, pl.Text)
				}
				mu.Unlock()
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		res, err := e.Run(ctx, docker.Request{TaskID: id, LeaseID: "lease-" + id, Spec: sp}, ch)
		close(ch)
		<-done
		require.NoError(t, err, "stdout so far: %s", stdout.String())
		t.Cleanup(func() { _ = e.Teardown(context.Background(), id, false) })
		mu.Lock()
		defer mu.Unlock()
		saidBy[id] = said
		return res, stdout.String(), sandboxID
	}

	res, out, sbID := run("t-pooled", "podium-agent-runtime:test",
		`echo hello from the sandbox; wget -qO- "$SURFACE/podium.agent.v1.TurnService/Delegate"; echo; /podium/runner message --type final - <<EOF
said through the runner
EOF`)
	assert.Equal(t, 0, res.ExitCode)
	assert.Contains(t, out, "hello from the sandbox")
	assert.Contains(t, out, "surface reached at /podium.agent.v1.TurnService/Delegate",
		"a task in a sandbox reaches the turn surface at the URL it was given")
	assert.Contains(t, pooled, sbID, "the task ran in the sandbox that was ready before it asked")
	require.Len(t, saidBy["t-pooled"], 1, "what the task said through podium-runner reached the node")
	assert.Equal(t, "said through the runner", strings.TrimSpace(saidBy["t-pooled"][0]))

	for e.PoolReady() != 1 {
		require.True(t, time.Now().Before(deadline.Add(30*time.Second)), "the pool never refilled")
		time.Sleep(50 * time.Millisecond)
	}
	before := p.Count()
	res, out, otherID := run("t-own", "alpine:3", `echo a sandbox of its own`)
	assert.Equal(t, 0, res.ExitCode)
	assert.Contains(t, out, "a sandbox of its own")
	assert.NotEqual(t, sbID, otherID)
	assert.Equal(t, before+1, p.Count(), "a task of another shape made its own sandbox")
	assert.Equal(t, 1, e.PoolReady(), "and left the pool alone")

	waiting := len(p.IDs())
	require.NoError(t, e.Close())
	assert.Equal(t, waiting-1, p.Count(), "closing the node deletes the sandbox still waiting in the pool")
}
