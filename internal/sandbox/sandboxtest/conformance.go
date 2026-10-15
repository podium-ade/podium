package sandboxtest

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/podium-ade/podium/internal/sandbox"
)

// Options tunes the conformance suite to what a provider's base image has.
type Options struct {
	// Create is the request the suite starts from; Key and Ports are filled in.
	Create sandbox.CreateRequest
	// ServeCmd is a shell command, formatted with the listen host and the port, that
	// serves the current directory over HTTP in the foreground.
	ServeCmd string
	// Timeout bounds the whole suite.
	Timeout time.Duration
}

const confPort = 7799

// Run is the contract every provider has to meet. It creates one sandbox and deletes it.
func Run(t *testing.T, p sandbox.Provider, opts Options) {
	t.Helper()
	if opts.Timeout == 0 {
		opts.Timeout = 5 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), opts.Timeout)
	defer cancel()

	req := opts.Create
	req.Key = fmt.Sprintf("conformance-%d", time.Now().UnixNano())
	req.Ports = []int{confPort}
	if req.TTL == 0 {
		req.TTL = 15 * time.Minute
	}
	id, err := p.Create(ctx, req)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Cleanup(func() {
		dctx, dcancel := context.WithTimeout(context.Background(), time.Minute)
		defer dcancel()
		if err := p.Delete(dctx, id); err != nil {
			t.Errorf("delete %s: %v", id, err)
		}
		if err := p.Delete(dctx, id); err != nil {
			t.Errorf("deleting %s twice is not idempotent: %v", id, err)
		}
	})

	if again, err := p.Create(ctx, req); err != nil || again != id {
		t.Fatalf("create with the same key: got %q, %v; want %q", again, err, id)
	}
	if err := p.WaitReady(ctx, id); err != nil {
		t.Fatalf("wait ready: %v", err)
	}

	const body = "hello from podium\n"
	if err := p.PutFile(ctx, id, "/tmp/podium-conf/sub/index.html", strings.NewReader(body), int64(len(body))); err != nil {
		t.Fatalf("put file: %v", err)
	}
	res, err := p.Exec(ctx, id, "cat /tmp/podium-conf/sub/index.html; echo err >&2; exit 3", false)
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if res.ExitCode != 3 || res.Stdout != body || strings.TrimSpace(res.Stderr) != "err" {
		t.Fatalf("exec: got %+v", res)
	}

	host := "0.0.0.0"
	if p.Capabilities().ExposeLoopback {
		host = "127.0.0.1"
	}
	serve := "cd /tmp/podium-conf/sub && " + fmt.Sprintf(opts.ServeCmd, host, confPort)
	if _, err := p.Exec(ctx, id, serve, true); err != nil {
		t.Fatalf("exec detached: %v", err)
	}
	ep, err := p.Expose(ctx, id, confPort)
	if err != nil {
		t.Fatalf("expose: %v", err)
	}
	if again, err := p.Expose(ctx, id, confPort); err != nil || again.URL != ep.URL {
		t.Fatalf("expose twice: got %q, %v; want %q", again.URL, err, ep.URL)
	}
	if got := fetch(ctx, t, ep); got != body {
		t.Fatalf("exposed port served %q, want %q", got, body)
	}
	if err := p.ExtendTTL(ctx, id, 20*time.Minute); err != nil {
		t.Fatalf("extend ttl: %v", err)
	}
}

func fetch(ctx context.Context, t *testing.T, ep sandbox.Endpoint) string {
	t.Helper()
	var last error
	for i := 0; i < 60; i++ {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(ep.URL, "/")+"/index.html", nil)
		ep.Apply(req)
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			b, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return string(b)
			}
			err = fmt.Errorf("status %d: %s", resp.StatusCode, b)
		}
		last = err
		time.Sleep(time.Second)
	}
	t.Fatalf("exposed port never answered: %v", last)
	return ""
}
