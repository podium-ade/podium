//go:build modal

package modal

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/podium-ade/podium/internal/sandbox"
	"github.com/podium-ade/podium/internal/sandbox/sandboxtest"
)

// These tests need MODAL_CONFORMANCE=1 and Modal credentials (MODAL_TOKEN_ID and
// MODAL_TOKEN_SECRET, or a ~/.modal.toml profile). Each starts one small sandbox in the
// App MODAL_APP (default podium-sandbox-dev).
func newConformance(t *testing.T) *Provider {
	t.Helper()
	if os.Getenv("MODAL_CONFORMANCE") == "" {
		t.Skip("MODAL_CONFORMANCE is not set")
	}
	app := os.Getenv("MODAL_APP")
	if app == "" {
		app = "podium-sandbox-dev"
	}
	p, err := New(Config{App: app, Runtime: os.Getenv("MODAL_RUNTIME"), CPU: 0.25, MemoryMB: 512})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

var confCreate = sandbox.CreateRequest{Image: "python:3.12-slim"}

func TestConformance(t *testing.T) {
	sandboxtest.Run(t, newConformance(t), sandboxtest.Options{
		Create:   confCreate,
		ServeCmd: "python3 -m http.server --bind %s %d",
	})
}

func TestLargeFileAndTTL(t *testing.T) {
	p := newConformance(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	req := confCreate
	req.Key = fmt.Sprintf("conformance-big-%d", time.Now().UnixNano())
	req.TTL = 10 * time.Minute
	id, err := p.Create(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := p.Delete(context.Background(), id); err != nil {
			t.Errorf("delete: %v", err)
		}
	})
	if err := p.WaitReady(ctx, id); err != nil {
		t.Fatal(err)
	}

	data := make([]byte, 32<<20)
	_, _ = rand.Read(data)
	sum := sha256.Sum256(data)
	start := time.Now()
	if err := p.PutFile(ctx, id, "/opt/podium/bin/agent", bytes.NewReader(data), int64(len(data))); err != nil {
		t.Fatal(err)
	}
	t.Logf("put 32 MiB in %v", time.Since(start))
	res, err := p.Exec(ctx, id, "sha256sum /opt/podium/bin/agent", false)
	if err != nil || !strings.HasPrefix(res.Stdout, hex.EncodeToString(sum[:])) {
		t.Fatalf("%+v %v", res, err)
	}

	if err := p.ExtendTTL(ctx, id, time.Hour); err != nil {
		t.Fatal(err)
	}
	res, _ = p.Exec(ctx, id, "read -r c d < "+ttlFile+"; echo $((d - $(date +%s)))", false)
	if left, _ := strconv.Atoi(strings.TrimSpace(res.Stdout)); left < 3590 || left > 3600 {
		t.Fatalf("seconds left after extending to 1h: %q", res.Stdout)
	}
	if err := p.ExtendTTL(ctx, id, MaxTTL); err == nil {
		t.Fatal("extended past Modal's limit")
	}
	if err := p.ExtendTTL(ctx, id, 3*time.Second); err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := p.Exec(ctx, id, "true", false); err != nil {
			t.Logf("sandbox stopped once its ttl passed: %v", err)
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("the sandbox outlived its ttl")
		case <-time.After(2 * time.Second):
		}
	}
}

// TestDocker needs MODAL_RUNTIME=vm: a sandbox from the default image runs a container.
func TestDocker(t *testing.T) {
	p := newConformance(t)
	if !p.Capabilities().Docker {
		t.Skip("MODAL_RUNTIME is not vm")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	id, err := p.Create(ctx, sandbox.CreateRequest{Key: fmt.Sprintf("conformance-docker-%d", time.Now().UnixNano()), TTL: 10 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := p.Delete(context.Background(), id); err != nil {
			t.Errorf("delete: %v", err)
		}
	})
	if err := p.WaitReady(ctx, id); err != nil {
		t.Fatal(err)
	}
	res, err := p.Exec(ctx, id, "docker run --rm hello-world && mkdir -p /mnt/s && mount -t tmpfs tmpfs /mnt/s && echo tmpfs-ok", false)
	if err != nil || res.ExitCode != 0 || !strings.Contains(res.Stdout, "Hello from Docker!") || !strings.Contains(res.Stdout, "tmpfs-ok") {
		t.Fatalf("%+v %v", res, err)
	}
}

// TestAModalImageByName starts a sandbox from an image Modal holds, published by name with
// Modal's SDK — MODAL_IMAGE, such as the dev runtime examples/modal/publish_image.py makes —
// and checks it is that image: the runtime is there, and its ENTRYPOINT did not replace the
// keep-alive.
func TestAModalImageByName(t *testing.T) {
	name := os.Getenv("MODAL_IMAGE")
	if name == "" {
		t.Skip("MODAL_IMAGE is not set")
	}
	p := newConformance(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	id, err := p.Create(ctx, sandbox.CreateRequest{
		Key: fmt.Sprintf("conformance-named-%d", time.Now().UnixNano()), Image: "modal:" + name, TTL: 5 * time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Delete(context.Background(), id) })
	if err := p.WaitReady(ctx, id); err != nil {
		t.Fatal(err)
	}
	res, err := p.Exec(ctx, id, "test -f /opt/podium-agent/dist/main.js && node --version && go version", false)
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("exec: %v, exit %d: %s%s", err, res.ExitCode, res.Stdout, res.Stderr)
	}
	t.Logf("%s", res.Stdout)
}

// A workspace kept as a directory snapshot comes back in a new sandbox: its files, modes and
// links, on a mount the next task can write to and take over, and what it adds is in the next
// snapshot. A deleted image is not_found, which is what makes a session start over.
func TestDirectorySnapshotRoundTrip(t *testing.T) {
	p := newConformance(t)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	start := func(name string) string {
		t.Helper()
		req := confCreate
		req.Key = fmt.Sprintf("conformance-%s-%d", name, time.Now().UnixNano())
		req.TTL = 10 * time.Minute
		id, err := p.Create(ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = p.Delete(context.Background(), id) })
		if err := p.WaitReady(ctx, id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	sh := func(id, cmd string) string {
		t.Helper()
		res, err := p.Exec(ctx, id, cmd, false)
		if err != nil || res.ExitCode != 0 {
			t.Fatalf("%s: %+v %v", cmd, res, err)
		}
		return strings.TrimSpace(res.Stdout)
	}
	var images []string
	t.Cleanup(func() {
		for _, img := range images {
			if err := p.DeleteSnapshot(context.Background(), img); err != nil {
				t.Errorf("delete snapshot %s: %v", img, err)
			}
		}
	})

	first := start("snap-a")
	sh(first, `useradd -m podium 2>/dev/null || true; install -d -m 0755 -o podium /workspace &&
		su podium -c 'mkdir -p /workspace/src && echo hello > /workspace/src/a.txt && chmod 0640 /workspace/src/a.txt && ln -s src/a.txt /workspace/link'`)
	img, err := p.SnapshotDir(ctx, first, "/workspace", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	images = append(images, img)
	if !strings.HasPrefix(img, "im-") {
		t.Fatalf("image id %q", img)
	}
	_ = p.Delete(ctx, first)

	second := start("snap-b")
	if err := p.MountSnapshot(ctx, second, "/workspace", img); err != nil {
		t.Fatal(err)
	}
	if got := sh(second, "cat /workspace/link; stat -c '%a %U' /workspace/src/a.txt"); got != "hello\n640 podium" && got != "hello\n640 1000" {
		t.Logf("after the mount: %q", got)
		if !strings.HasPrefix(got, "hello\n640") {
			t.Fatalf("the files did not come back: %q", got)
		}
	}
	sh(second, `useradd -m podium 2>/dev/null || true; install -d -m 0755 -o podium /workspace && su podium -c 'echo more > /workspace/b.txt'`)
	again, err := p.SnapshotDir(ctx, second, "/workspace", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	images = append(images, again)

	third := start("snap-c")
	if err := p.MountSnapshot(ctx, third, "/workspace", again); err != nil {
		t.Fatal(err)
	}
	if got := sh(third, "cat /workspace/src/a.txt /workspace/b.txt"); got != "hello\nmore" {
		t.Fatalf("a snapshot of a mounted snapshot lost something: %q", got)
	}

	if err := p.DeleteSnapshot(ctx, img); err != nil {
		t.Fatal(err)
	}
	images = images[1:]
	if err := p.DeleteSnapshot(ctx, img); err != nil {
		t.Fatalf("deleting a deleted image: %v", err)
	}
	err = p.MountSnapshot(ctx, third, "/elsewhere", img)
	var pe *sandbox.Error
	if !errors.As(err, &pe) || pe.Code != "not_found" {
		t.Fatalf("mounting a deleted image: %v", err)
	}
}
