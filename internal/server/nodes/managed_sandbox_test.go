//go:build integration

package nodes_test

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	podiumv1 "github.com/podium-ade/podium/internal/proto/podium/v1"
	"github.com/podium-ade/podium/internal/proto/podium/v1/podiumv1connect"
	"github.com/podium-ade/podium/internal/sandbox"
	"github.com/podium-ade/podium/internal/sandbox/sandboxtest"
	"github.com/podium-ade/podium/internal/server"
)

// withoutSandboxRunners is for a test that drives sandbox nodes of its own: the server's own
// runner for the provider would otherwise take their tasks.
func withoutSandboxRunners(c *server.Config) { c.NoSandboxRunners = true }

var registerBwrap sync.Once

// TestTheServerRunsAConfiguredProvidersSandboxes: configuring a provider on the Sandboxes
// screen is the whole setup. No node is enrolled or started; the server runs the provider's
// sandboxes itself, a task asking for that provider runs in one, and the provider never
// appears among the nodes.
//
// PODIUM_TEST_BWRAP is bubblewrap: the provider here is sandboxtest.Bwrap, so this runs with
// no cloud account. The server binary has to carry podium-sandbox-agent (make
// sandbox-agent-embed).
func TestTheServerRunsAConfiguredProvidersSandboxes(t *testing.T) {
	bin := os.Getenv("PODIUM_TEST_BWRAP")
	if bin == "" {
		t.Skip("PODIUM_TEST_BWRAP is not set")
	}
	dir := t.TempDir()
	registerBwrap.Do(func() {
		sandbox.Register("bwraptest", func(map[string]any) (sandbox.Provider, error) {
			return &sandboxtest.Bwrap{Dir: dir, Bin: bin}, nil
		})
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	h := newHarness(t, func(c *server.Config) { c.SandboxDir = t.TempDir() })
	providers := podiumv1connect.NewSandboxProviderServiceClient(h.http, h.url)

	// Before the provider exists, a task asking for it says what to do about it.
	early := h.createSpec(&podiumv1.TaskSpec{
		Image: "podium-test:any", Command: []string{"true"},
		Sandbox: &podiumv1.TaskSandbox{Provider: "bwraptest"},
	})
	waitFor(t, 15*time.Second, "the reason a task for an unconfigured provider waits", func() bool {
		return strings.Contains(h.getTask(early.GetId()).GetQueuedReason(), "not set up")
	})
	_, _ = h.tasks.CancelTask(ctx, connect.NewRequest(&podiumv1.CancelTaskRequest{TaskId: early.GetId(), Reason: "test"}))

	_, err := providers.SetSandboxProvider(ctx, connect.NewRequest(&podiumv1.SetSandboxProviderRequest{
		Name: "bwraptest", Secret: []byte("unused"),
	}))
	require.NoError(t, err)

	task := h.createSpec(&podiumv1.TaskSpec{
		Image:   "podium-test:any",
		Command: []string{"sh", "-c", "echo run by the server"},
		Sandbox: &podiumv1.TaskSandbox{Provider: "bwraptest"},
	})
	done := h.awaitTaskStatus(task.GetId(), podiumv1.TaskStatus_TASK_STATUS_SUCCEEDED, 60*time.Second)
	assert.NotEmpty(t, done.GetSandboxId(), "it ran in a sandbox the server made")

	var out strings.Builder
	for _, ev := range h.streamEvents(task.GetId(), 0) {
		out.Write(ev.GetLog().GetBytes())
	}
	assert.Contains(t, out.String(), "run by the server")

	for _, n := range h.listNodes() {
		assert.NotContains(t, n.GetLabels(), "sandbox.provider=bwraptest",
			"a provider is not a node: it is shown on the Sandboxes screen")
	}
}
