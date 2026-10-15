//go:build modal

package sandboxexec

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/podium-ade/podium/internal/node/docker"
	"github.com/podium-ade/podium/internal/sandbox/modal"
	"github.com/podium-ade/podium/pkg/spec"
)

// TestModalImageProcess needs MODAL_CONFORMANCE=1 with Modal credentials, and BOAT_TEST_AGENT
// pointing at a linux/amd64 podium-sandbox-agent. On Modal the task's image is the sandbox,
// so the agent runs the command directly in it.
func TestModalImageProcess(t *testing.T) {
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
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	sp := spec.TaskSpec{
		Image: "python:3.12-slim",
		Command: []string{"sh", "-c", "id -un; echo $TOKEN; cat /podium/secrets/f; cat /workspace/seed.txt; " +
			"mkdir -p /workspace/.podium/artifacts && echo art > /workspace/.podium/artifacts/a.txt; " +
			"echo saved > /workspace/new.txt; echo $PODIUM_URL_WEB > /tmp/url; cd /tmp && echo hi > index.html && " +
			"exec python3 -m http.server 8000"},
		Secrets:          []spec.SecretRef{{Name: "t", Target: "env", Key: "TOKEN"}, {Name: "f", Target: "file", Key: "/podium/secrets/f"}},
		WorkspaceSession: "s1",
		Expose:           &spec.Expose{Via: spec.ExposeViaSandbox, TTL: spec.Duration(10 * time.Minute), Ports: map[string]spec.ExposedPort{"web": {Port: 8000}}},
		Timeout:          spec.Duration(20 * time.Minute),
	}
	sp.ApplyDefaults()
	up := &memUploader{got: map[string]string{}}
	ch := make(chan docker.Event, 64)
	var c collector
	done := make(chan struct{})
	go func() { c.run(ch); close(done) }()
	type result struct {
		res docker.Result
		err error
	}
	out := make(chan result, 1)
	go func() {
		res, err := e.Run(ctx, docker.Request{
			TaskID: "modal-process-1", LeaseID: "lease-m-" + time.Now().Format("150405.000"), Spec: sp,
			Secrets:   []docker.Secret{{Name: "t", Target: "env", Key: "TOKEN", Value: []byte("tok-1")}, {Name: "f", Target: "file", Key: "/podium/secrets/f", Value: []byte("file-1\n")}},
			Artifacts: up, Workspace: &docker.Workspace{Restore: func(context.Context) (io.ReadCloser, bool, error) { return io.NopCloser(seedTar(t)), true, nil }},
			Preview: &docker.Preview{Via: spec.ExposeViaSandbox},
		}, ch)
		out <- result{res, err}
	}()
	t.Cleanup(func() { _ = e.Teardown(context.Background(), "modal-process-1", false) })

	for deadline := time.Now().Add(5 * time.Minute); !strings.Contains(c.logs(docker.StreamStdout), "seed\n"); time.Sleep(time.Second) {
		if time.Now().After(deadline) {
			t.Fatalf("no output; events %v", c.kinds())
		}
	}
	if got := c.logs(docker.StreamStdout); got != "podium\ntok-1\nfile-1\nseed\n" {
		t.Errorf("stdout %q", got)
	}
	rec, err := e.load("modal-process-1")
	if err != nil {
		t.Fatal(err)
	}
	ep, err := p.Expose(ctx, rec.SandboxID, 8000)
	if err != nil {
		t.Fatal(err)
	}
	var body string
	for i := 0; i < 60 && body != "hi\n"; i++ {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(ep.URL, "/")+"/index.html", nil)
		ep.Apply(req)
		if resp, err := http.DefaultClient.Do(req); err == nil {
			b, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			body = string(b)
		}
		time.Sleep(time.Second)
	}
	if body != "hi\n" {
		t.Errorf("preview served %q", body)
	}
	e.Cancel("modal-process-1")
	r := <-out
	close(ch)
	<-done
	if r.err != nil {
		t.Fatalf("run: %v; events %v", r.err, c.kinds())
	}
	if up.got["a.txt"] != "art\n" {
		t.Errorf("artifacts %v", up.got)
	}
	t.Logf("exit %d; events %v", r.res.ExitCode, c.kinds())
}
