//go:build boat

package sandboxexec

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/podium-ade/podium/internal/node/docker"
	"github.com/podium-ade/podium/internal/sandbox/boat"
	"github.com/podium-ade/podium/pkg/spec"
)

// These tests need BOAT_API_KEY_FILE, and BOAT_TEST_AGENT pointing at a linux/amd64
// podium-sandbox-agent built after `make runner-embed`. Each starts one boat sandbox.
func newBoatExecutor(t *testing.T) *Executor {
	t.Helper()
	keyFile, agent := os.Getenv("BOAT_API_KEY_FILE"), os.Getenv("BOAT_TEST_AGENT")
	if keyFile == "" || agent == "" {
		t.Skip("BOAT_API_KEY_FILE and BOAT_TEST_AGENT are needed")
	}
	p, err := boat.New(boat.Config{APIKeyFile: keyFile, DefaultType: "small"})
	if err != nil {
		t.Fatal(err)
	}
	e, err := New(Options{Provider: p, DataDir: t.TempDir(), Agent: func() ([]byte, error) { return os.ReadFile(agent) },
		Logger: slog.New(slog.NewTextHandler(os.Stderr, nil))})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestBoatDockerStrategy(t *testing.T) {
	e := newBoatExecutor(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	sp := spec.TaskSpec{
		Image: "alpine:3",
		Command: []string{"sh", "-c", "echo out; echo $TOKEN; cat /podium/secrets/f; " +
			"mkdir -p /workspace/.podium/artifacts && echo art > /workspace/.podium/artifacts/a.txt; " +
			"cat /workspace/seed.txt; echo saved > /workspace/new.txt; echo err >&2; exit 5"},
		Secrets:          []spec.SecretRef{{Name: "t", Target: "env", Key: "TOKEN"}, {Name: "f", Target: "file", Key: "/podium/secrets/f"}},
		WorkspaceSession: "s1",
	}
	sp.ApplyDefaults()
	var saved []byte
	ws := &docker.Workspace{
		Restore: func(context.Context) (io.ReadCloser, bool, error) {
			return io.NopCloser(seedTar(t)), true, nil
		},
		Save: func(_ context.Context, path string, _ int64) error {
			b, err := os.ReadFile(path)
			saved = b
			return err
		},
	}
	up := &memUploader{got: map[string]string{}}
	ch := make(chan docker.Event, 64)
	var c collector
	done := make(chan struct{})
	go func() { c.run(ch); close(done) }()
	res, err := e.Run(ctx, docker.Request{
		TaskID: "boat-docker-1", LeaseID: "lease-" + time.Now().Format("150405.000"), Spec: sp,
		Secrets:   []docker.Secret{{Name: "t", Target: "env", Key: "TOKEN", Value: []byte("tok-1")}, {Name: "f", Target: "file", Key: "/podium/secrets/f", Value: []byte("file-1\n")}},
		Artifacts: up, Workspace: ws,
	}, ch)
	close(ch)
	<-done
	t.Cleanup(func() { _ = e.Teardown(context.Background(), "boat-docker-1", false) })
	if err != nil {
		t.Fatalf("run: %v; events %v", err, c.kinds())
	}
	if res.ExitCode != 5 {
		t.Errorf("exit %d", res.ExitCode)
	}
	if got := c.logs(docker.StreamStdout); got != "out\ntok-1\nfile-1\nseed\n" {
		t.Errorf("stdout %q", got)
	}
	if got := c.logs(docker.StreamStderr); got != "err\n" {
		t.Errorf("stderr %q", got)
	}
	if up.got["a.txt"] != "art\n" {
		t.Errorf("artifacts %v", up.got)
	}
	if !strings.Contains(string(saved), "new.txt") {
		t.Errorf("saved workspace has no new.txt (%d bytes)", len(saved))
	}
	t.Logf("events: %v", c.kinds())
}

func TestBoatPreviewAndCancel(t *testing.T) {
	e := newBoatExecutor(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	sp := spec.TaskSpec{
		Image:   "python:3.12-alpine",
		Command: []string{"sh", "-c", "echo $PODIUM_URL_WEB; cd /tmp && echo hi > index.html && exec python3 -m http.server 8000"},
		Expose:  &spec.Expose{Via: spec.ExposeViaSandbox, TTL: spec.Duration(10 * time.Minute), Ports: map[string]spec.ExposedPort{"web": {Port: 8000}}},
	}
	sp.ApplyDefaults()
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
		res, err := e.Run(ctx, docker.Request{TaskID: "boat-preview-1", LeaseID: "lease-p-" + time.Now().Format("150405.000"),
			Spec: sp, Preview: &docker.Preview{Via: spec.ExposeViaSandbox}}, ch)
		out <- result{res, err}
	}()
	t.Cleanup(func() { _ = e.Teardown(context.Background(), "boat-preview-1", false) })

	var url string
	for deadline := time.Now().Add(5 * time.Minute); url == "" && time.Now().Before(deadline); time.Sleep(time.Second) {
		url = strings.TrimSpace(c.logs(docker.StreamStdout))
	}
	if !strings.HasPrefix(url, "https://") {
		t.Fatalf("PODIUM_URL_WEB = %q; events %v", url, c.kinds())
	}
	// boat trades the link's token for a cookie on a redirect, the way a browser expects.
	jar, _ := cookiejar.New(nil)
	browser := &http.Client{Jar: jar}
	var body string
	for i := 0; i < 60 && body != "hi\n"; i++ {
		if resp, err := browser.Get(url); err == nil {
			b, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			body = string(b)
		}
		time.Sleep(time.Second)
	}
	if body != "hi\n" {
		t.Fatalf("preview served %q", body)
	}
	start := time.Now()
	e.Cancel("boat-preview-1")
	r := <-out
	close(ch)
	<-done
	if r.err != nil {
		t.Fatalf("run: %v", r.err)
	}
	t.Logf("cancel took %s, exit %d, held %v, events %v", time.Since(start), r.res.ExitCode, r.res.Held, c.kinds())
}

// TestBoatSnapshotProcess needs BOAT_TEST_SNAPSHOT, a named snapshot made with noEnv.
func TestBoatSnapshotProcess(t *testing.T) {
	snap := os.Getenv("BOAT_TEST_SNAPSHOT")
	if snap == "" {
		t.Skip("BOAT_TEST_SNAPSHOT is not set")
	}
	e := newBoatExecutor(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	sp := spec.TaskSpec{
		Sandbox: &spec.Sandbox{Snapshot: snap},
		Command: []string{"sh", "-c", "id -un; echo $TOKEN; cat /podium/secrets/f; cat /workspace/seed.txt; pwd; " +
			"sudo -n true 2>/dev/null || echo nosudo; docker ps >/dev/null 2>&1 || echo nodocker; " +
			"test -r /home/user/.bashrc || echo nohome; env | grep -c ASCII_TOKEN; " +
			"mkdir -p /workspace/.podium/artifacts && echo art > /workspace/.podium/artifacts/a.txt; " +
			"echo saved > /workspace/new.txt; echo err >&2; exit 4"},
		Secrets:          []spec.SecretRef{{Name: "t", Target: "env", Key: "TOKEN"}, {Name: "f", Target: "file", Key: "/podium/secrets/f"}},
		WorkspaceSession: "s1",
	}
	sp.ApplyDefaults()
	var saved []byte
	ws := &docker.Workspace{
		Restore: func(context.Context) (io.ReadCloser, bool, error) { return io.NopCloser(seedTar(t)), true, nil },
		Save: func(_ context.Context, path string, _ int64) error {
			b, err := os.ReadFile(path)
			saved = b
			return err
		},
	}
	up := &memUploader{got: map[string]string{}}
	ch := make(chan docker.Event, 64)
	var c collector
	done := make(chan struct{})
	go func() { c.run(ch); close(done) }()
	res, err := e.Run(ctx, docker.Request{
		TaskID: "boat-process-1", LeaseID: "lease-s-" + time.Now().Format("150405.000"), Spec: sp,
		Secrets:   []docker.Secret{{Name: "t", Target: "env", Key: "TOKEN", Value: []byte("tok-1")}, {Name: "f", Target: "file", Key: "/podium/secrets/f", Value: []byte("file-1\n")}},
		Artifacts: up, Workspace: ws,
	}, ch)
	close(ch)
	<-done
	t.Cleanup(func() { _ = e.Teardown(context.Background(), "boat-process-1", false) })
	if err != nil {
		t.Fatalf("run: %v; events %v", err, c.kinds())
	}
	want := "podium\ntok-1\nfile-1\nseed\n/workspace\nnosudo\nnodocker\nnohome\n0\n"
	if got := c.logs(docker.StreamStdout); res.ExitCode != 4 || got != want {
		t.Errorf("exit %d, stdout %q, want %q", res.ExitCode, got, want)
	}
	if up.got["a.txt"] != "art\n" {
		t.Errorf("artifacts %v", up.got)
	}
	if !strings.Contains(string(saved), "new.txt") {
		t.Errorf("saved workspace has no new.txt (%d bytes)", len(saved))
	}
	t.Logf("events: %v", c.kinds())
}

// TestBoatAdopt runs a task, drops the executor mid-run the way a dying daemon would, and
// adopts the run from a new executor on the same data dir.
func TestBoatAdopt(t *testing.T) {
	e1 := newBoatExecutor(t)
	sp := spec.TaskSpec{Image: "alpine:3", Command: []string{"sh", "-c", "for i in 1 2 3 4 5 6 7 8; do echo line$i; sleep 2; done; exit 3"}}
	sp.ApplyDefaults()
	ctx1, kill := context.WithCancel(context.Background())
	ch1 := make(chan docker.Event, 64)
	var c1 collector
	go c1.run(ch1)
	go func() {
		_, _ = e1.Run(ctx1, docker.Request{TaskID: "boat-adopt-1", LeaseID: "lease-a-" + time.Now().Format("150405.000"), Spec: sp}, ch1)
	}()
	t.Cleanup(func() { _ = e1.Teardown(context.Background(), "boat-adopt-1", false) })
	for deadline := time.Now().Add(5 * time.Minute); !strings.Contains(c1.logs(docker.StreamStdout), "line2\n"); time.Sleep(200 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("never saw line2; events %v", c1.kinds())
		}
	}
	kill()
	seen := c1.logs(docker.StreamStdout)

	e2, err := New(Options{Provider: e1.p, DataDir: e1.dir, Agent: e1.agent, Logger: e1.log})
	if err != nil {
		t.Fatal(err)
	}
	owned, err := e2.ListOwned(context.Background())
	if err != nil || len(owned) != 1 || owned[0].TaskID != "boat-adopt-1" {
		t.Fatalf("owned %+v %v", owned, err)
	}
	ch2 := make(chan docker.Event, 64)
	var c2 collector
	done := make(chan struct{})
	go func() { c2.run(ch2); close(done) }()
	res, err := e2.Adopt(context.Background(), docker.AdoptRequest{TaskID: "boat-adopt-1", LeaseID: owned[0].LeaseID, FromSeq: 100, SkipStdout: int64(len(seen))}, ch2)
	close(ch2)
	<-done
	if err != nil {
		t.Fatalf("adopt: %v; events %v", err, c2.kinds())
	}
	if all := seen + c2.logs(docker.StreamStdout); all != "line1\nline2\nline3\nline4\nline5\nline6\nline7\nline8\n" || res.ExitCode != 3 {
		t.Fatalf("exit %d; before %q after %q", res.ExitCode, seen, c2.logs(docker.StreamStdout))
	}
	t.Logf("before adopt %q; adopted events %v", seen, c2.kinds())
}

// TestBoatSnapshotSidecar runs a snapshot task whose sidecar runs on the VM's own Docker.
func TestBoatSnapshotSidecar(t *testing.T) {
	snap := os.Getenv("BOAT_TEST_SNAPSHOT")
	if snap == "" {
		t.Skip("BOAT_TEST_SNAPSHOT is not set")
	}
	e := newBoatExecutor(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	sp := spec.TaskSpec{
		Sandbox: &spec.Sandbox{Snapshot: snap},
		Command: []string{"sh", "-c", "curl -s http://web/ && getent hosts web | wc -l"},
		Sidecars: map[string]spec.Sidecar{
			"web": {Image: "nginx:alpine", Readiness: spec.Readiness{HTTPPath: "/", HTTPPort: 80}},
		},
	}
	sp.ApplyDefaults()
	ch := make(chan docker.Event, 64)
	var c collector
	done := make(chan struct{})
	go func() { c.run(ch); close(done) }()
	res, err := e.Run(ctx, docker.Request{TaskID: "boat-sidecar-1", LeaseID: "lease-sc-" + time.Now().Format("150405.000"), Spec: sp}, ch)
	close(ch)
	<-done
	t.Cleanup(func() { _ = e.Teardown(context.Background(), "boat-sidecar-1", false) })
	if err != nil {
		t.Fatalf("run: %v; events %v", err, c.kinds())
	}
	if got := c.logs(docker.StreamStdout); res.ExitCode != 0 || !strings.Contains(got, "Welcome to nginx") {
		t.Fatalf("exit %d, stdout %q; events %v", res.ExitCode, got, c.kinds())
	}
	t.Logf("events: %v", c.kinds())
}
