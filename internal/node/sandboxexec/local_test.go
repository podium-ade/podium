//go:build sandbox

package sandboxexec

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/podium-ade/podium/internal/node/docker"
	"github.com/podium-ade/podium/internal/sandbox/sandboxtest"
	"github.com/podium-ade/podium/pkg/spec"
)

// TestLocalDindSidecar runs an image task with a sidecar in a Docker-in-Docker sandbox on the
// local engine. LOCAL_TEST_AGENT is podium-sandbox-agent built for the engine's linux arch.
func TestLocalDindSidecar(t *testing.T) {
	agent := os.Getenv("LOCAL_TEST_AGENT")
	if agent == "" {
		t.Skip("LOCAL_TEST_AGENT is not set")
	}
	e, err := New(Options{Provider: &sandboxtest.Local{Docker: true}, DataDir: t.TempDir(),
		Agent: func() ([]byte, error) { return os.ReadFile(agent) }, Logger: slog.New(slog.NewTextHandler(os.Stderr, nil))})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	sp := spec.TaskSpec{
		Image:   "redis:7-alpine",
		Command: []string{"sh", "-c", "redis-cli -h cache set k from-the-task && redis-cli -h cache get k"},
		Sidecars: map[string]spec.Sidecar{
			"cache": {Image: "redis:7-alpine", Readiness: spec.Readiness{TCPPort: 6379}},
		},
	}
	sp.ApplyDefaults()
	ch := make(chan docker.Event, 64)
	var c collector
	done := make(chan struct{})
	go func() { c.run(ch); close(done) }()
	res, err := e.Run(ctx, docker.Request{TaskID: "local-dind-1", LeaseID: "lease-l-" + time.Now().Format("150405.000"), Spec: sp}, ch)
	close(ch)
	<-done
	t.Cleanup(func() { _ = e.Teardown(context.Background(), "local-dind-1", false) })
	if err != nil {
		t.Fatalf("run: %v; events %v", err, c.kinds())
	}
	if got := c.logs(docker.StreamStdout); res.ExitCode != 0 || !strings.Contains(got, "OK\nfrom-the-task\n") {
		t.Fatalf("exit %d, stdout %q; events %v", res.ExitCode, got, c.kinds())
	}
	t.Logf("events: %v", c.kinds())
}
