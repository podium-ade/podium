//go:build modal

package modal

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
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
