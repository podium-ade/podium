package sandboxagent

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/podium-ade/podium/internal/sandboxagent/wire"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()
	s, err := New("tok", t.TempDir(), nil, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func serve(s *Server, method, path string, body io.Reader, hdr ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, body)
	req.Header.Set("Authorization", "Bearer tok")
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

// offerAsync offers tr and waits until the agent lists it.
func offerAsync(t *testing.T, s *Server, tr wire.Transfer, path string) <-chan error {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		_, err := s.transfers.offer(context.Background(), tr, path)
		done <- err
	}()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if pending, _ := s.transfers.snapshot(); len(pending) == 1 {
			return done
		}
	}
	t.Fatal("the transfer was never offered")
	return nil
}

type breaksAfter struct{ r io.Reader }

func (b breaksAfter) Read(p []byte) (int, error) {
	n, err := b.r.Read(p)
	if errors.Is(err, io.EOF) {
		return n, errors.New("connection reset")
	}
	return n, err
}

func TestARestoreResumesFromWhatArrived(t *testing.T) {
	s := newTestServer(t)
	path := filepath.Join(s.dir, "transfers", "workspace-restore.tar")
	done := offerAsync(t, s, wire.Transfer{Kind: wire.TransferWorkspaceRestore}, path)

	if rec := serve(s, http.MethodPut, "/transfers/1", breaksAfter{strings.NewReader("hello")}); rec.Code != http.StatusInternalServerError {
		t.Fatalf("broken PUT: %d", rec.Code)
	}
	if rec := serve(s, http.MethodGet, "/transfers/1/offset", nil); rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"offset":5}` {
		t.Fatalf("offset: %d %s", rec.Code, rec.Body)
	}
	if rec := serve(s, http.MethodPut, "/transfers/1?offset=7", strings.NewReader("x")); rec.Code != http.StatusConflict {
		t.Fatalf("resuming past what arrived: %d", rec.Code)
	}
	// Resuming before the end drops the tail the broken attempt left.
	if rec := serve(s, http.MethodPut, "/transfers/1?offset=3", strings.NewReader("lo world")); rec.Code != http.StatusNoContent {
		t.Fatalf("resumed PUT: %d %s", rec.Code, rec.Body)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); string(got) != "hello world" {
		t.Fatalf("staged %q", got)
	}
}

func TestAnOlderAttemptCannotWriteOnceANewerOneStarts(t *testing.T) {
	p := &pendingTransfer{path: filepath.Join(t.TempDir(), "restore.tar")}
	old, err := p.resume(0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = old.Close() }()
	if _, err := old.Write([]byte("abc")); err != nil {
		t.Fatal(err)
	}
	next, err := p.resume(3)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.Write([]byte("zzz")); !errors.Is(err, errSuperseded) {
		t.Fatalf("old attempt wrote: %v", err)
	}
	if _, err := next.Write([]byte("def")); err != nil {
		t.Fatal(err)
	}
	_ = next.Close()
	if got, _ := os.ReadFile(p.path); string(got) != "abcdef" {
		t.Fatalf("staged %q", got)
	}
}

func TestADownloadAnswersARange(t *testing.T) {
	s := newTestServer(t)
	path := filepath.Join(t.TempDir(), "ws.tar")
	if err := os.WriteFile(path, []byte("hello world"), 0o600); err != nil {
		t.Fatal(err)
	}
	offerAsync(t, s, wire.Transfer{Kind: wire.TransferWorkspaceSave, Size: 11}, path)
	rec := serve(s, http.MethodGet, "/transfers/1", nil, "Range", "bytes=6-")
	if rec.Code != http.StatusPartialContent || rec.Body.String() != "world" {
		t.Fatalf("range: %d %q", rec.Code, rec.Body)
	}
}
