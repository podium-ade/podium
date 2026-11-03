package sandboxexec

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/podium-ade/podium/internal/node/docker"
	"github.com/podium-ade/podium/internal/sandbox"
	"github.com/podium-ade/podium/internal/sandboxagent/wire"
)

// flakyAgent stands in for a sandbox agent behind a link that drops the connection after
// cut bytes of each of the first breaks requests.
type flakyAgent struct {
	data   []byte
	cut    int
	breaks int

	mu       sync.Mutex
	staged   []byte
	requests int
	offsets  []int
}

func (a *flakyAgent) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	if r.URL.Path == "/transfers/1/offset" {
		defer a.mu.Unlock()
		_ = json.NewEncoder(w).Encode(wire.TransferOffset{Offset: int64(len(a.staged))})
		return
	}
	a.requests++
	broken := a.requests <= a.breaks
	a.mu.Unlock()
	switch r.Method {
	case http.MethodPut:
		off, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		buf := make([]byte, a.cut)
		n, _ := io.ReadFull(r.Body, buf)
		a.mu.Lock()
		a.offsets = append(a.offsets, off)
		a.staged = append(a.staged[:off], buf[:n]...)
		a.mu.Unlock()
		if broken {
			hangUp(w)
			return
		}
		rest, _ := io.ReadAll(r.Body)
		a.mu.Lock()
		a.staged = append(a.staged, rest...)
		a.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	case http.MethodGet:
		if broken {
			w.Header().Set("Content-Length", strconv.Itoa(len(a.data)))
			_, _ = w.Write(a.data[:a.cut])
			w.(http.Flusher).Flush()
			hangUp(w)
			return
		}
		http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(a.data))
	}
}

func hangUp(w http.ResponseWriter) {
	conn, _, err := w.(http.Hijacker).Hijack()
	if err == nil {
		_ = conn.Close()
	}
}

func testClient(t *testing.T, h http.Handler) *agentClient {
	t.Helper()
	resumePause = 0
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &agentClient{ep: sandbox.Endpoint{URL: srv.URL}, token: "tok", hc: srv.Client(), log: slog.New(slog.DiscardHandler)}
}

func TestARestoreThatBreaksResumesWhereItStopped(t *testing.T) {
	data := bytes.Repeat([]byte("0123456789"), 100_000)
	a := &flakyAgent{cut: 300_000, breaks: 2}
	c := testClient(t, a)
	if err := c.put(context.Background(), "1", bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a.staged, data) {
		t.Fatalf("staged %d bytes, want %d", len(a.staged), len(data))
	}
	if want := []int{0, 300_000, 600_000}; !slices.Equal(a.offsets, want) {
		t.Fatalf("attempts started at %v, want %v", a.offsets, want)
	}
}

func TestARestoreThatNeverGetsFurtherGivesUp(t *testing.T) {
	a := &flakyAgent{cut: 0, breaks: 1 << 30}
	c := testClient(t, a)
	if err := c.put(context.Background(), "1", bytes.NewReader([]byte("hello"))); err == nil {
		t.Fatal("put succeeded over a link that delivers nothing")
	}
	if a.requests != resumeAttempts {
		t.Fatalf("%d requests before giving up", a.requests)
	}
}

func TestADownloadThatBreaksResumesWithARange(t *testing.T) {
	data := bytes.Repeat([]byte("abcdefghij"), 100_000)
	a := &flakyAgent{data: data, cut: 250_000, breaks: 1}
	c := testClient(t, a)
	body, err := c.fetch(context.Background(), "1")
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(body)
	_ = body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("read %d bytes, want %d", len(got), len(data))
	}
}

func TestAdoptFilterSkipsWhatTheServerHas(t *testing.T) {
	f := &adoptFilter{skipStdout: 5, skipStderr: 0, seq: 4}
	log := func(stream, s string, offset int64) docker.Event {
		return docker.Event{Kind: docker.KindLog, Payload: docker.LogPayload{Stream: stream, Bytes: []byte(s), Offset: offset}}
	}
	for _, tc := range []struct {
		ev   docker.Event
		keep bool
		text string
	}{
		{log(docker.StreamStdout, "abc", 3), false, ""},
		{log(docker.StreamStdout, "defg", 7), true, "fg"},
		{log(docker.StreamStdout, "hi", 9), true, "hi"},
		{log(docker.StreamStderr, "e", 1), true, "e"},
		{docker.Event{Kind: docker.KindLog, Payload: docker.LogPayload{Stream: docker.StreamSidecar, Bytes: []byte("x"), Offset: 1}}, false, ""},
		{docker.Event{Seq: 3, Kind: docker.KindArtifact}, false, ""},
		{docker.Event{Seq: 5, Kind: docker.KindArtifact}, true, ""},
		{docker.Event{Seq: 2, Kind: docker.KindFinished}, true, ""},
	} {
		ev := tc.ev
		if got := f.keep(&ev); got != tc.keep {
			t.Errorf("%v: keep %v, want %v", tc.ev, got, tc.keep)
			continue
		}
		if p, ok := ev.Payload.(docker.LogPayload); ok && tc.keep && string(p.Bytes) != tc.text {
			t.Errorf("%v: kept %q, want %q", tc.ev, p.Bytes, tc.text)
		}
	}
}

func TestRegistryForPicksTheImagesOwnRegistry(t *testing.T) {
	creds := []docker.RegistryCredential{
		{Host: "ghcr.io", Username: "octo", Password: []byte("ghp")},
		{Host: "us-docker.pkg.dev", Username: "_json_key", Password: []byte("{}")},
	}
	got := registryFor("us-docker.pkg.dev/acme/images/app:1", creds)
	if got == nil || got.Username != "_json_key" || got.Password != "{}" {
		t.Fatalf("got %+v", got)
	}
	if got := registryFor("alpine:3", creds); got != nil {
		t.Fatalf("a public image gets no login, got %+v", got)
	}
	if got := registryFor("", creds); got != nil {
		t.Fatalf("a snapshot task gets no login, got %+v", got)
	}
}
