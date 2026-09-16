package ghreview

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type stub struct {
	t   *testing.T
	srv *httptest.Server

	mu             sync.Mutex
	issuePosts     []map[string]any
	reviewPosts    []map[string]any
	edits          []map[string]any
	issueComments  []Comment
	reviewComments []Comment
	reviews        []Review
	installID      int64
	install404     bool
	nextComment    int64
	app            App
}

func newStub(t *testing.T) *stub {
	t.Helper()
	s := &stub{
		t:           t,
		installID:   42,
		nextComment: 100,
		app:         App{ID: 1, Slug: "podium", Name: "Podium"},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/app", s.getApp)
	mux.HandleFunc("/app/installations/", s.accessToken)
	mux.HandleFunc("/repos/", s.repos)
	s.srv = httptest.NewServer(mux)
	t.Cleanup(s.srv.Close)
	return s
}

func (s *stub) url() string { return s.srv.URL }

func (s *stub) getApp(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/app" {
		http.NotFound(w, r)
		return
	}
	_ = json.NewEncoder(w).Encode(s.app)
}

func (s *stub) accessToken(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/access_tokens") {
		http.NotFound(w, r)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"token":      "ghs_test",
		"expires_at": time.Now().Add(time.Hour).Format(time.RFC3339),
	})
}

func (s *stub) repos(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	switch {
	case strings.HasSuffix(path, "/installation"):
		s.mu.Lock()
		missing := s.install404
		id := s.installID
		s.mu.Unlock()
		if missing {
			http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": id})
	case strings.Contains(path, "/issues/") && strings.HasSuffix(path, "/comments") && r.Method == http.MethodPost:
		s.writeComment(w, r, &s.issuePosts)
	case strings.Contains(path, "/issues/comments/") && r.Method == http.MethodPatch:
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		s.mu.Lock()
		s.edits = append(s.edits, body)
		s.mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(raw)
	case strings.Contains(path, "/pulls/") && strings.HasSuffix(strings.Split(path, "?")[0], "/comments") && r.Method == http.MethodPost:
		s.writeComment(w, r, &s.reviewPosts)
	case strings.Contains(path, "/issues/") && strings.Contains(path, "/comments") && r.Method == http.MethodGet:
		s.mu.Lock()
		defer s.mu.Unlock()
		_ = json.NewEncoder(w).Encode(s.issueComments)
	case strings.Contains(path, "/pulls/") && strings.HasSuffix(strings.Split(path, "?")[0], "/comments") && r.Method == http.MethodGet:
		s.mu.Lock()
		defer s.mu.Unlock()
		_ = json.NewEncoder(w).Encode(s.reviewComments)
	case strings.Contains(path, "/pulls/") && strings.HasSuffix(strings.Split(path, "?")[0], "/reviews") && r.Method == http.MethodGet:
		s.mu.Lock()
		defer s.mu.Unlock()
		_ = json.NewEncoder(w).Encode(s.reviews)
	default:
		http.NotFound(w, r)
	}
}

func (s *stub) writeComment(w http.ResponseWriter, r *http.Request, into *[]map[string]any) {
	raw, _ := io.ReadAll(r.Body)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	id := atomic.AddInt64(&s.nextComment, 1)
	s.mu.Lock()
	*into = append(*into, body)
	s.mu.Unlock()
	_ = json.NewEncoder(w).Encode(Comment{
		ID:        id,
		Body:      asString(body["body"]),
		CreatedAt: time.Now().UTC(),
		User:      User{Login: "podium[bot]", Type: "Bot"},
	})
}

func asString(v any) string {
	s, _ := v.(string)
	return s
}

func testKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 1024)
	require.NoError(t, err)
	return k
}

func (s *stub) postedIssueBodies() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.issuePosts))
	for _, p := range s.issuePosts {
		out = append(out, asString(p["body"]))
	}
	return out
}

func (s *stub) postedReviewInReplyTo() []int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]int64, 0, len(s.reviewPosts))
	for _, p := range s.reviewPosts {
		switch n := p["in_reply_to"].(type) {
		case float64:
			out = append(out, int64(n))
		case json.Number:
			v, _ := n.Int64()
			out = append(out, v)
		}
	}
	return out
}
