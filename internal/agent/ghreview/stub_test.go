package ghreview

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
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
	reviews        []map[string]any
	reviewEdits    []map[string]any
	edits          []map[string]any
	issueComments  []Comment
	reviewComments []Comment
	listedReviews  []Review
	rejectComments bool
	dropReview     bool
	failPull       bool
	installID      int64
	install404     bool
	nextComment    int64
	app            App
	// threadRoots is who wrote an inline review comment, by id, for GET pulls/comments/{id}.
	threadRoots map[int64]User
	threadReads int
	// reactions is every reaction posted, as "<path> <content>".
	reactions []string
	// roles is a login's role_name on acme/repo. A login not in it has none.
	roles     map[string]string
	roleReads int
	// fork makes the pull request's head another repository; canModify is whether its author
	// lets maintainers push there.
	fork      bool
	canModify bool
	// threads is the pull request's review threads as GraphQL lists them, and resolved the
	// thread ids a mutation resolved.
	threads  []stubThread
	resolved []string
}

type stubThread struct {
	ID       string
	Root     int64
	Resolved bool
}

func newStub(t *testing.T) *stub {
	t.Helper()
	s := &stub{
		t:           t,
		installID:   42,
		nextComment: 100,
		threadRoots: map[int64]User{},
		roles:       map[string]string{},
		app:         App{ID: 1, Slug: "podium", Name: "Podium"},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/app", s.getApp)
	mux.HandleFunc("/app/installations/", s.accessToken)
	mux.HandleFunc("/repos/", s.repos)
	mux.HandleFunc("/graphql", s.graphql)
	s.srv = httptest.NewServer(mux)
	t.Cleanup(s.srv.Close)
	return s
}

func (s *stub) url() string { return s.srv.URL }

// graphql answers the review-thread query, and the resolve mutation by recording its id.
func (s *stub) graphql(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.Contains(req.Query, "resolveReviewThread") {
		id, _ := req.Variables["id"].(string)
		s.resolved = append(s.resolved, id)
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{}})
		return
	}
	nodes := make([]map[string]any, 0, len(s.threads))
	for _, t := range s.threads {
		nodes = append(nodes, map[string]any{
			"id": t.ID, "isResolved": t.Resolved,
			"comments": map[string]any{"nodes": []map[string]any{{"databaseId": t.Root}}},
		})
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
		"repository": map[string]any{"pullRequest": map[string]any{"reviewThreads": map[string]any{
			"nodes": nodes, "pageInfo": map[string]any{"hasNextPage": false},
		}}},
	}})
}

func (s *stub) postedReactions() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.reactions...)
}

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
	case strings.HasSuffix(path, "/reactions") && r.Method == http.MethodPost:
		raw, _ := io.ReadAll(r.Body)
		var body map[string]string
		_ = json.Unmarshal(raw, &body)
		s.mu.Lock()
		s.reactions = append(s.reactions, strings.TrimPrefix(path, "/repos/acme/repo")+" "+body["content"])
		s.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
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
	case strings.Contains(path, "/pulls/") && strings.HasSuffix(strings.Split(path, "?")[0], "/reviews") && r.Method == http.MethodPost:
		s.writeReview(w, r)
	case strings.Contains(path, "/pulls/") && strings.Contains(path, "/reviews/") && r.Method == http.MethodPut:
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		s.mu.Lock()
		s.reviewEdits = append(s.reviewEdits, body)
		s.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	case strings.Contains(path, "/pulls/") && strings.HasSuffix(strings.Split(path, "?")[0], "/reviews") && r.Method == http.MethodGet:
		s.mu.Lock()
		defer s.mu.Unlock()
		_ = json.NewEncoder(w).Encode(s.listedReviews)
	case r.Method == http.MethodGet && strings.Contains(path, "/pulls/comments/"):
		id, _ := strconv.ParseInt(path[strings.LastIndex(path, "/")+1:], 10, 64)
		s.mu.Lock()
		s.threadReads++
		user, ok := s.threadRoots[id]
		s.mu.Unlock()
		if !ok {
			http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(Comment{ID: id, Body: "root", User: user})
	case r.Method == http.MethodGet && strings.Contains(path, "/collaborators/") && strings.HasSuffix(path, "/permission"):
		login := strings.TrimSuffix(path[strings.Index(path, "/collaborators/")+len("/collaborators/"):], "/permission")
		s.mu.Lock()
		s.roleReads++
		role := s.roles[login]
		s.mu.Unlock()
		permission := role
		if role == "" {
			permission = "none"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"permission": permission, "role_name": role})
	case r.Method == http.MethodGet && isPullPath(path):
		s.mu.Lock()
		fail := s.failPull
		head := "acme/repo"
		if s.fork {
			head = "alice/repo"
		}
		canModify := s.canModify
		s.mu.Unlock()
		if fail {
			http.Error(w, `{"message":"unavailable"}`, http.StatusBadGateway)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"title":         "Fix the review",
			"commits":       2,
			"additions":     40,
			"deletions":     5,
			"changed_files": 3,
			"head": map[string]any{
				"ref": "agent/review", "sha": "0123456789abcdef",
				"repo": map[string]any{"full_name": head},
			},
			"base":                  map[string]any{"ref": "dev", "repo": map[string]any{"full_name": "acme/repo"}},
			"maintainer_can_modify": canModify,
		})
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

func (s *stub) writeReview(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	s.mu.Lock()
	drop := s.dropReview
	reject := s.rejectComments
	s.dropReview = false
	s.mu.Unlock()
	if drop {
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			_ = conn.Close()
		}
		return
	}
	if reject {
		if comments, ok := body["comments"].([]any); ok && len(comments) > 0 {
			s.mu.Lock()
			s.reviews = append(s.reviews, body)
			s.mu.Unlock()
			http.Error(w, `{"message":"line must be part of the diff"}`, http.StatusUnprocessableEntity)
			return
		}
	}
	id := atomic.AddInt64(&s.nextComment, 1)
	s.mu.Lock()
	s.reviews = append(s.reviews, body)
	s.mu.Unlock()
	_ = json.NewEncoder(w).Encode(Review{
		ID:          id,
		Body:        asString(body["body"]),
		SubmittedAt: time.Now().UTC(),
		User:        User{Login: "podium[bot]", Type: "Bot"},
	})
}

func (s *stub) threadRoot(id int64, u User) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.threadRoots[id] = u
}

func (s *stub) threadRootReads() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.threadReads
}

func isPullPath(path string) bool {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) != 5 || parts[0] != "repos" || parts[3] != "pulls" {
		return false
	}
	_, err := strconv.Atoi(parts[4])
	return err == nil
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

func (s *stub) postedIssueEdits() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.edits))
	for _, p := range s.edits {
		out = append(out, asString(p["body"]))
	}
	return out
}

func (s *stub) postedReviews() []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]map[string]any(nil), s.reviews...)
}

func (s *stub) postedReviewEdits() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.reviewEdits))
	for _, p := range s.reviewEdits {
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
