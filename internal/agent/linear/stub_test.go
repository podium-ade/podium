package linear

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// stub is a Linear GraphQL endpoint scripted by operation name. Every response body below
// is the shape Linear's schema declares: nothing here invents a field, and the two that
// matter most — a nullable comment `user` and a `WorkflowState.type` that is a plain
// string — are exercised as such.
type stub struct {
	t   *testing.T
	srv *httptest.Server

	mu       sync.Mutex
	handlers map[string]func(vars map[string]any) stubReply
	calls    []stubCall
	auths    []string
	// uploads records every PUT that reached the pretend asset store.
	uploads []stubUpload
	// uploadStatus is what that PUT answers.
	uploadStatus int
}

type stubCall struct {
	Op   string
	Vars map[string]any
}

type stubUpload struct {
	Body    []byte
	Headers http.Header
}

// stubReply is one scripted answer: data, or an error with a code and a status.
type stubReply struct {
	Data    any
	Message string
	Code    string
	Status  int
}

func newStub(t *testing.T) *stub {
	t.Helper()
	s := &stub{t: t, handlers: map[string]func(map[string]any) stubReply{}, uploadStatus: http.StatusOK}
	mux := http.NewServeMux()
	mux.HandleFunc("/graphql", s.graphql)
	mux.HandleFunc("/upload", s.upload)
	s.srv = httptest.NewServer(mux)
	t.Cleanup(s.srv.Close)
	return s
}

func (s *stub) endpoint() string  { return s.srv.URL + "/graphql" }
func (s *stub) uploadURL() string { return s.srv.URL + "/upload" }

// on scripts one operation. The last call wins, so a test can rescript between ticks.
func (s *stub) on(op string, fn func(vars map[string]any) stubReply) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.handlers[op] = fn
}

// reply is on for an operation whose answer does not depend on its variables.
func (s *stub) reply(op string, data any) {
	s.on(op, func(map[string]any) stubReply { return stubReply{Data: data} })
}

func (s *stub) graphql(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	var req struct {
		Query         string         `json:"query"`
		Variables     map[string]any `json:"variables"`
		OperationName string         `json:"operationName"`
	}
	require.NoError(s.t, json.Unmarshal(raw, &req))

	s.mu.Lock()
	s.calls = append(s.calls, stubCall{Op: req.OperationName, Vars: req.Variables})
	s.auths = append(s.auths, r.Header.Get("Authorization"))
	fn := s.handlers[req.OperationName]
	s.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	if fn == nil {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"errors":[{"message":"the stub has no script for ` +
			req.OperationName + `","extensions":{"code":"INTERNAL_SERVER_ERROR"}}]}`))
		return
	}
	out := fn(req.Variables)
	if out.Message != "" {
		status := out.Status
		if status == 0 {
			status = http.StatusOK
		}
		w.WriteHeader(status)
		body := map[string]any{"errors": []map[string]any{{
			"message":    out.Message,
			"extensions": map[string]any{"code": out.Code},
		}}}
		_ = json.NewEncoder(w).Encode(body)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"data": out.Data})
}

func (s *stub) upload(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	s.mu.Lock()
	s.uploads = append(s.uploads, stubUpload{Body: body, Headers: r.Header.Clone()})
	status := s.uploadStatus
	s.mu.Unlock()
	w.WriteHeader(status)
}

func (s *stub) seen() []stubCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]stubCall(nil), s.calls...)
}

func (s *stub) seenAuths() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.auths...)
}

func (s *stub) seenUploads() []stubUpload {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]stubUpload(nil), s.uploads...)
}

// callsOf is every call to one operation, in order.
func (s *stub) callsOf(op string) []stubCall {
	var out []stubCall
	for _, c := range s.seen() {
		if c.Op == op {
			out = append(out, c)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// fixtures
// ---------------------------------------------------------------------------

// fakeAPIKey is an obvious fixture. Linear's real keys are `lin_api_…`, which is
// deliberately NOT validated anywhere in this package: the prefix is third-party lore, not
// something Linear documents.
const fakeAPIKey = "not-a-real-linear-key"

const (
	botUserID   = "user-bot-0001"
	humanUserID = "user-human-0002"
	teamID      = "team-eng-0001"
)

func iso(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

// issueNode is one node of the poll query's page.
func issueNode(id, identifier, title, stateType string, updatedAt time.Time, comments ...map[string]any) map[string]any {
	if comments == nil {
		comments = []map[string]any{}
	}
	return map[string]any{
		"id":          id,
		"identifier":  identifier,
		"title":       title,
		"description": "the description of " + identifier,
		"url":         "https://linear.app/acme/issue/" + identifier,
		"createdAt":   iso(updatedAt.Add(-time.Hour)),
		"updatedAt":   iso(updatedAt),
		"state":       map[string]any{"id": "state-1", "name": "Todo", "type": stateType},
		"team":        map[string]any{"id": teamID, "key": "ENG"},
		"assignee":    map[string]any{"id": botUserID, "name": "podium", "displayName": "Podium", "isMe": true},
		"comments":    map[string]any{"nodes": comments},
	}
}

// humanComment is a comment written by a person.
func humanComment(id, body string, at time.Time) map[string]any {
	return map[string]any{
		"id":         id,
		"body":       body,
		"createdAt":  iso(at),
		"parentId":   nil,
		"quotedText": nil,
		"user": map[string]any{
			"id": humanUserID, "name": "alice", "displayName": "Alice", "isMe": false,
		},
		"botActor": nil,
	}
}

// selfComment is a comment written by the bot user through this very API key: Linear sets
// isMe on it.
func selfComment(id, body string, at time.Time) map[string]any {
	return map[string]any{
		"id":         id,
		"body":       body,
		"createdAt":  iso(at),
		"parentId":   nil,
		"quotedText": nil,
		"user": map[string]any{
			"id": botUserID, "name": "podium", "displayName": "Podium", "isMe": true,
		},
		"botActor": nil,
	}
}

// botComment is a comment with NO user at all — what Linear returns for an integration or
// an agent without a user association. It is the case a naive implementation gets wrong.
func botComment(id, body string, at time.Time) map[string]any {
	return map[string]any{
		"id":         id,
		"body":       body,
		"createdAt":  iso(at),
		"parentId":   nil,
		"quotedText": nil,
		"user":       nil,
		"botActor":   map[string]any{"id": "bot-1", "name": "Linear Asks", "userDisplayName": "Asks"},
	}
}

func page(hasNext bool, cursor string, nodes ...map[string]any) map[string]any {
	if nodes == nil {
		nodes = []map[string]any{}
	}
	return map[string]any{
		"issues": map[string]any{
			"pageInfo": map[string]any{"hasNextPage": hasNext, "endCursor": cursor},
			"nodes":    nodes,
		},
	}
}

func viewerData() map[string]any {
	return map[string]any{"viewer": map[string]any{
		"id": botUserID, "name": "podium", "displayName": "Podium", "isMe": true,
	}}
}
