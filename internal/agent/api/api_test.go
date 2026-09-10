package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/agent/conductor"
	"github.com/podium-ade/podium/internal/agent/conductor/fakesource"
)

// echoLogin reports the login the middleware put in the context, so a test can see the
// header was read and stripped of nothing else.
var echoLogin = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	_, _ = w.Write([]byte(Login(r.Context())))
})

// loginCtx is what RequireBearer would have put in the context: the login
// podium-server's proxy asserted.
func loginCtx(login string) context.Context {
	return context.WithValue(context.Background(), loginKey{}, login)
}

func TestRequireBearer(t *testing.T) {
	h := RequireBearer("agenttoken", echoLogin)

	tests := []struct {
		name       string
		header     string
		wantStatus int
	}{
		{"no header", "", http.StatusUnauthorized},
		{"the wrong token", "Bearer nope", http.StatusUnauthorized},
		{"no scheme", "agenttoken", http.StatusUnauthorized},
		{"the wrong scheme", "Basic agenttoken", http.StatusUnauthorized},
		{"a prefix of the token", "Bearer agenttoke", http.StatusUnauthorized},
		{"the token", "Bearer agenttoken", http.StatusOK},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			assert.Equal(t, tc.wantStatus, rec.Code)
			if tc.wantStatus == http.StatusUnauthorized {
				assert.Equal(t, "Bearer", rec.Header().Get("WWW-Authenticate"))
			}
		})
	}
}

// A conductor with no token configured must refuse everything rather than accept "Bearer ".
func TestRequireBearerWithNoTokenRefusesEverything(t *testing.T) {
	h := RequireBearer("", echoLogin)
	for _, header := range []string{"", "Bearer ", "Bearer x"} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Authorization", header)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusUnauthorized, rec.Code, "header %q", header)
	}
}

func TestLoginComesFromTheProxyHeader(t *testing.T) {
	h := RequireBearer("t", echoLogin)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer t")
	req.Header.Set(LoginHeader, "alvaro")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	assert.Equal(t, "alvaro", rec.Body.String())

	// Absent is "unknown", as internal/server/api does.
	req = httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer t")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	assert.Equal(t, "unknown", rec.Body.String())
	assert.Equal(t, "unknown", Login(context.Background()), "a handler reached without the middleware")
}

// ---------------------------------------------------------------------------
// the dev source
// ---------------------------------------------------------------------------

func TestDevSourceInboundBecomesAnEvent(t *testing.T) {
	dev := NewDevSource()
	t.Cleanup(dev.Close)
	srv := httptest.NewServer(dev.Handler())
	t.Cleanup(srv.Close)

	body := `{"channel":"C1","thread":"1.1","author":"alice","text":"hello there",
		"dry_run":true,"dry_run_sleep_ms":250,"dry_run_exit":3}`
	res, err := http.Post(srv.URL+DevInboundPath, "application/json", strings.NewReader(body))
	require.NoError(t, err)
	defer func() { _ = res.Body.Close() }()
	require.Equal(t, http.StatusAccepted, res.StatusCode)

	var accepted map[string]string
	require.NoError(t, json.NewDecoder(res.Body).Decode(&accepted))
	assert.Equal(t, "dev:C1:1.1", accepted["source_key"])
	assert.Equal(t, "C1/1.1", accepted["ref"])

	ev := <-dev.Events()
	assert.Equal(t, conductor.KindDev, ev.SourceKind)
	assert.Equal(t, "dev:C1:1.1", ev.SourceKey)
	assert.Equal(t, "C1/1.1", ev.Ref)
	assert.Equal(t, "alice", ev.Author)
	assert.Equal(t, "hello there", ev.Text)
	// The dev source presents itself as a chat: the runtime's schema knows nothing called
	// "dev".
	assert.Equal(t, conductor.SourceChat, ev.BriefKind)
	// The three test-only knobs step 16 defined, and nothing else.
	assert.Equal(t, map[string]string{
		"PODIUM_AGENT_DRY_RUN":          "1",
		"PODIUM_AGENT_DRY_RUN_SLEEP_MS": "250",
		"PODIUM_AGENT_DRY_RUN_EXIT":     "3",
	}, ev.Env)
}

func TestDevSourceWithoutDryRunAsksForNoEnvironment(t *testing.T) {
	dev := NewDevSource()
	t.Cleanup(dev.Close)
	srv := httptest.NewServer(dev.Handler())
	t.Cleanup(srv.Close)

	res, err := http.Post(srv.URL+DevInboundPath, "application/json",
		strings.NewReader(`{"channel":"C1","thread":"1.1","text":"hi"}`))
	require.NoError(t, err)
	require.NoError(t, res.Body.Close())

	ev := <-dev.Events()
	assert.Empty(t, ev.Env)
	assert.Equal(t, "dev", ev.Author, "an unnamed author is 'dev'")
}

func TestDevSourceRejectsAnIncompleteInbound(t *testing.T) {
	dev := NewDevSource()
	t.Cleanup(dev.Close)
	srv := httptest.NewServer(dev.Handler())
	t.Cleanup(srv.Close)

	for _, body := range []string{`{}`, `{"channel":"C1"}`, `{"thread":"1.1"}`, `not json`} {
		res, err := http.Post(srv.URL+DevInboundPath, "application/json", strings.NewReader(body))
		require.NoError(t, err)
		require.NoError(t, res.Body.Close())
		assert.Equal(t, http.StatusBadRequest, res.StatusCode, "body %q", body)
	}
}

func TestDevSourceOutboundReportsEverythingInOrder(t *testing.T) {
	ctx := context.Background()
	dev := NewDevSource()
	t.Cleanup(dev.Close)
	srv := httptest.NewServer(dev.Handler())
	t.Cleanup(srv.Close)

	require.NoError(t, dev.React(ctx, "C1/1.1", conductor.ReactionWorking))
	id, err := dev.Post(ctx, "C1/1.1", conductor.Outbound{Type: conductor.OutProgress, Text: conductor.Placeholder})
	require.NoError(t, err)
	require.NoError(t, dev.Edit(ctx, "C1/1.1", id, conductor.Outbound{Type: conductor.OutProgress, Text: "⏳ reading"}))
	_, err = dev.Post(ctx, "C1/1.1", conductor.Outbound{Type: conductor.OutFinal, Text: "dry run: hello"})
	require.NoError(t, err)
	require.NoError(t, dev.React(ctx, "C1/1.1", conductor.ReactionDone))

	records := fetchRecords(t, srv.URL+DevOutboundPath)
	require.Len(t, records, 5)
	assert.Equal(t, fakesource.ActionReact, records[0].Action)
	assert.Equal(t, string(conductor.ReactionWorking), records[0].Reaction)
	assert.Equal(t, conductor.Placeholder, records[1].Text)
	assert.Equal(t, fakesource.ActionEdit, records[2].Action)
	assert.Equal(t, id, records[2].MessageID)
	assert.Equal(t, conductor.OutFinal, records[3].Type)
	assert.Equal(t, string(conductor.ReactionDone), records[4].Reaction)

	// since= is exclusive, which is what lets a test poll without re-reading history.
	assert.Len(t, fetchRecords(t, srv.URL+DevOutboundPath+"?since=3"), 2)
	assert.Empty(t, fetchRecords(t, srv.URL+DevOutboundPath+"?since=99"))

	// The transcript is the injected messages plus the bot's finals — never its
	// placeholder or its progress.
	entries, err := dev.FetchTranscript(ctx, "C1/1.1")
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, conductor.RoleAssistant, entries[0].Role)
	assert.Equal(t, "dry run: hello", entries[0].Text)
}

func fetchRecords(t *testing.T, url string) []fakesource.Record {
	t.Helper()
	res, err := http.Get(url) //nolint:noctx // a test's own httptest server
	require.NoError(t, err)
	defer func() { _ = res.Body.Close() }()
	require.Equal(t, http.StatusOK, res.StatusCode)
	var body struct {
		Records []fakesource.Record `json:"records"`
	}
	require.NoError(t, json.NewDecoder(res.Body).Decode(&body))
	return body.Records
}
