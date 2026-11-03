//go:build modal

package sandboxexec

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/podium-ade/podium/internal/node/docker"
	"github.com/podium-ade/podium/internal/sandbox/modal"
	"github.com/podium-ade/podium/pkg/spec"
)

// TestModalSessionKeptAsADirectorySnapshot runs two tasks of one session on Modal, on two
// images whose task account gets a different uid, so the second finds the first's files on a
// mounted image it has to take over before it can write to them. Same environment as
// TestModalImageProcess.
func TestModalSessionKeptAsADirectorySnapshot(t *testing.T) {
	agent := os.Getenv("BOAT_TEST_AGENT")
	if os.Getenv("MODAL_CONFORMANCE") == "" || agent == "" {
		t.Skip("MODAL_CONFORMANCE and BOAT_TEST_AGENT are needed")
	}
	p, err := modal.New(modal.Config{App: "podium-sandbox-dev", CPU: 0.25, MemoryMB: 512})
	if err != nil {
		t.Fatal(err)
	}
	e, err := New(Options{Provider: p, DataDir: t.TempDir(), Agent: func() ([]byte, error) { return os.ReadFile(agent) },
		Logger: slog.New(slog.NewTextHandler(os.Stderr, nil))})
	if err != nil {
		t.Fatal(err)
	}
	cp := &controlPlane{ttl: 2 * time.Hour}
	t.Cleanup(func() {
		for _, img := range cp.puts {
			if img != "" {
				_ = p.DeleteSnapshot(context.Background(), img)
			}
		}
	})
	run := func(id, image, script string) string {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
		defer cancel()
		sp := spec.TaskSpec{Image: image, Command: []string{"sh", "-c", script}, WorkspaceSession: "s1"}
		sp.ApplyDefaults()
		ch := make(chan docker.Event, 256)
		var c collector
		done := make(chan struct{})
		go func() { c.run(ch); close(done) }()
		res, err := e.Run(ctx, docker.Request{
			TaskID: id, LeaseID: "lease-" + id + time.Now().Format("150405.000"), Spec: sp, Workspace: cp.hooks(false),
		}, ch)
		close(ch)
		<-done
		_ = e.Teardown(context.Background(), id, false)
		if err != nil || res.ExitCode != 0 {
			t.Fatalf("%s: exit %d, %v; stdout %q stderr %q", id, res.ExitCode, err, c.logs(docker.StreamStdout), c.logs(docker.StreamStderr))
		}
		return c.logs(docker.StreamStdout)
	}

	out := run("modal-snap-1", "python:3.12-slim", `id -u; echo first > /workspace/note.txt; mkdir -p /workspace/d && echo x > /workspace/d/f`)
	t.Logf("first: %q", out)
	if len(cp.puts) != 1 || !strings.HasPrefix(cp.puts[0], "im-") {
		t.Fatalf("the workspace was not kept as an image: %v", cp.puts)
	}
	out = run("modal-snap-2", "ubuntu:24.04", `id -u; cat /workspace/note.txt; echo second >> /workspace/note.txt && echo y > /workspace/d/g && cat /workspace/note.txt`)
	t.Logf("second: %q", out)
	if !strings.Contains(out, "first\nfirst\nsecond") {
		t.Fatalf("the second task did not get the first's workspace to write to: %q", out)
	}
	if cp.restores != 1 {
		t.Errorf("restored a tar %d times; only the first task, which had no image, should have", cp.restores)
	}
	if len(cp.puts) != 2 {
		t.Errorf("puts %v", cp.puts)
	}
}
