package agent

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/agent/conductor"
	"github.com/podium-ade/podium/internal/agent/config"
	"github.com/podium-ade/podium/internal/agent/connections"
	"github.com/podium-ade/podium/internal/agent/ghreview"
	agentlinear "github.com/podium-ade/podium/internal/agent/linear"
	"github.com/podium-ade/podium/internal/agent/webhooks"
)

const (
	fakeLinearClientID     = "not-a-real-client-id"
	fakeLinearClientSecret = "not-a-real-client-secret"
)

// fakeLinear is Linear's token endpoint and GraphQL API, as much of them as the check uses.
type fakeLinear struct {
	srv *httptest.Server

	mu sync.Mutex
	// refuse answers the token endpoint with invalid_client.
	refuse bool
	// person makes the viewer a person rather than an app.
	person bool
	grants int
	auths  []string
}

func newFakeLinear(t *testing.T) *fakeLinear {
	t.Helper()
	f := &fakeLinear{}
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		id, secret, _ := r.BasicAuth()
		f.mu.Lock()
		f.grants++
		refuse := f.refuse || id != fakeLinearClientID || secret != fakeLinearClientSecret
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if refuse {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"invalid_client","error_description":"Client authentication failed"}`))
			return
		}
		_, _ = w.Write([]byte(`{"access_token":"lin_oauth_fake","token_type":"Bearer","expires_in":2591999}`))
	})
	mux.HandleFunc("/graphql", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.auths = append(f.auths, r.Header.Get("Authorization"))
		person := f.person
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
			"viewer": map[string]any{
				"id": "app-user-1", "name": "Podium", "displayName": "podium", "app": !person,
			},
			"organization": map[string]any{"id": "org-1", "name": "Acme", "urlKey": "acme"},
		}})
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

// linearConfig is a Linear app pointed at the fake, with a listen address that is free.
func (f *fakeLinear) grantCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.grants
}

func (f *fakeLinear) seenAuths() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.auths...)
}

func (f *fakeLinear) config(t *testing.T) config.Config {
	t.Helper()
	return config.Config{
		LinearURL:           f.srv.URL + "/graphql",
		LinearTokenURL:      f.srv.URL + "/oauth/token",
		LinearClientID:      fakeLinearClientID,
		LinearClientSecret:  fakeLinearClientSecret,
		LinearWebhookSecret: "lin_wh_not-a-real-secret",
		LinearWebhookListen: freeAddr(t),
	}
}

func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())
	return addr
}

func newLive(running config.Config) *liveConnections {
	logger := slog.New(slog.DiscardHandler)
	return &liveConnections{
		logger: logger, webhooks: webhooks.New(logger), running: running,
		linearPlaybook: func() string { return "coder" },
	}
}

// serving mounts a handler on the live connections' webhook server at addr, the way a
// running source does, until stop is called or the test ends. The handler answers 200 with
// the path, so a test can tell it from the Linear app's, which refuses an unsigned body.
func serving(t *testing.T, l *liveConnections, addr, path string) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	h := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(path)) })
	go func() { done <- l.webhooks.Serve(ctx, addr, path, h) }()
	var once sync.Once
	stop = func() {
		once.Do(func() {
			cancel()
			<-done
		})
	}
	t.Cleanup(stop)
	require.Eventually(t, func() bool { return slices.Contains(l.webhooks.Paths(addr), path) },
		5*time.Second, 5*time.Millisecond)
	return stop
}

// running is liveConnections as Run leaves it, on env, with the Linear source env asks for
// started. The conductor is one with no store, which runs a leader's work on the spot.
func running(t *testing.T, env config.Config) *liveConnections {
	t.Helper()
	l := newLive(env)
	l.env = env
	l.conductor = &conductor.Conductor{}
	l.linearPlaybook = func() string { return "coder" }
	l.linearSession = func(context.Context, string) (time.Time, bool) { return time.Time{}, false }
	l.linearCursor = agentlinear.CursorStore{
		Get: func(context.Context) (time.Time, error) { return time.Time{}, nil },
		Put: func(context.Context, time.Time) error { return nil },
	}
	app, poll, err := l.buildLinear(env, nil)
	require.NoError(t, err)
	l.linearApp, l.linearPoll = app, poll
	ctx, cancel := context.WithCancel(context.Background())
	l.start(ctx, nil)
	t.Cleanup(func() {
		cancel()
		l.mu.Lock()
		defer l.mu.Unlock()
		l.stop()
		l.stopLinear()
	})
	return l
}

// savedApp is a Linear app saved on listen, as Settings stores it after the check.
func (f *fakeLinear) savedApp(listen string) *connections.Linear {
	return &connections.Linear{
		ClientID: fakeLinearClientID, ClientSecret: fakeLinearClientSecret,
		WebhookSecret: "lin_wh_not-a-real-secret", WebhookListen: listen,
		AppUserID: "app-user-1", AppUserName: "Podium", OrganizationID: "org-1",
	}
}

// post sends an unsigned delivery to addr and path, and says what answered.
func post(t *testing.T, addr, path string) int {
	t.Helper()
	res, err := http.Post("http://"+addr+path, "application/json", strings.NewReader("{}"))
	require.NoError(t, err)
	_ = res.Body.Close()
	return res.StatusCode
}

func TestCheckLearnsWhoTheLinearAppIs(t *testing.T) {
	f := newFakeLinear(t)
	checked, err := newLive(config.Config{}).Check(t.Context(), f.config(t))
	require.NoError(t, err)
	require.NotNil(t, checked.Linear)
	assert.Equal(t, "app-user-1", checked.Linear.UserID)
	assert.Equal(t, "Podium", checked.Linear.UserName)
	assert.Equal(t, "org-1", checked.Linear.OrganizationID)
	assert.Equal(t, "Acme", checked.Linear.OrganizationName)
	assert.Equal(t, "acme", checked.Linear.OrganizationURLKey)
	assert.Equal(t, []string{"Bearer lin_oauth_fake"}, f.auths)
}

func TestCheckRefusesLinearCredentialsLinearRefuses(t *testing.T) {
	f := newFakeLinear(t)
	f.refuse = true
	_, err := newLive(config.Config{}).Check(t.Context(), f.config(t))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "did not accept the client id and secret")
	assert.Contains(t, err.Error(), "Client authentication failed")
	assert.Contains(t, err.Error(), "Client credentials")
	assert.NotContains(t, err.Error(), fakeLinearClientSecret)
}

func TestCheckRefusesALinearUserThatIsNotAnApp(t *testing.T) {
	f := newFakeLinear(t)
	f.person = true
	_, err := newLive(config.Config{}).Check(t.Context(), f.config(t))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not an app")
}

// The app already running is not tried again, so a save that changes Slack or GitHub
// does not depend on Linear answering.
func TestCheckLeavesTheRunningLinearAppAlone(t *testing.T) {
	f := newFakeLinear(t)
	cfg := f.config(t)
	checked, err := newLive(cfg).Check(t.Context(), cfg)
	require.NoError(t, err)
	assert.Nil(t, checked.Linear)
	assert.Zero(t, f.grants)
}

func TestCheckRefusesALinearListenAddressInUse(t *testing.T) {
	f := newFakeLinear(t)
	held, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = held.Close() })
	cfg := f.config(t)
	cfg.LinearWebhookListen = held.Addr().String()

	_, err = newLive(config.Config{}).Check(t.Context(), cfg)
	require.ErrorContains(t, err, "cannot be listened on")
}

// An address this process already listens on is its own: the Linear app may share the
// GitHub App's, so the address being taken is not a refusal.
func TestCheckAcceptsTheGitHubListenerForLinear(t *testing.T) {
	f := newFakeLinear(t)
	addr := freeAddr(t)
	live := newLive(config.Config{})
	serving(t, live, addr, ghreview.WebhookPath)
	cfg := f.config(t)
	cfg.LinearWebhookListen = addr

	checked, err := live.Check(t.Context(), cfg)
	require.NoError(t, err)
	assert.NotNil(t, checked.Linear)
}

// The same the other way round: a GitHub App moved onto the Linear app's address joins it.
func TestCheckAcceptsTheLinearListenerForGitHub(t *testing.T) {
	addr := freeAddr(t)
	live := newLive(config.Config{})
	serving(t, live, addr, "/webhooks/linear")
	cfg := config.Config{GitHubWebhookSecret: "whsec", GitHubWebhookListen: addr}
	cfg.GitHubAppID, cfg.GitHubAppKey = "1", "pem"
	require.True(t, cfg.GitHubSourceEnabled())
	// The App's key is not tried here: it is the one already running.
	live.running = cfg
	_, err := live.Check(t.Context(), cfg)
	require.NoError(t, err)

	other := freeAddr(t)
	held, err := net.Listen("tcp", other)
	require.NoError(t, err)
	t.Cleanup(func() { _ = held.Close() })
	cfg.GitHubWebhookListen = other
	live.running = cfg
	_, err = live.Check(t.Context(), cfg)
	require.ErrorContains(t, err, "cannot be listened on", "an address somebody else holds is still refused")
}

// A saved app replaces the poller, even with PODIUM_AGENT_LINEAR_API_KEY set: the two would
// start every ticket twice. Clearing the app brings the poller back.
func TestASavedLinearAppTurnsThePollerOffAndClearingItBringsItBack(t *testing.T) {
	f := newFakeLinear(t)
	env := config.Config{
		LinearAPIKey: "lin_api_poller", LinearURL: f.srv.URL + "/graphql",
		LinearTokenURL: f.srv.URL + "/oauth/token", LinearPollInterval: time.Hour,
	}
	l := running(t, env)
	require.NotNil(t, l.linearPoll)
	require.Nil(t, l.linearApp)
	require.Eventually(t, func() bool { return slices.Contains(f.seenAuths(), "lin_api_poller") },
		5*time.Second, 5*time.Millisecond, "the poller signs in with the environment's key")

	addr := freeAddr(t)
	saved := f.savedApp(addr)
	next := connections.Overlay(env, nil, nil, saved)
	require.True(t, next.LinearEnabled(), "the key is still set")
	require.NoError(t, l.apply(t.Context(), next, saved))
	assert.Nil(t, l.linearPoll, "the poller is off")
	require.NotNil(t, l.linearApp)
	assert.Same(t, l.linearApp, l.linearSource())
	require.Eventually(t, func() bool { return l.webhooks.Holds(addr) }, 5*time.Second, 5*time.Millisecond)
	assert.Equal(t, http.StatusUnauthorized, post(t, addr, agentlinear.WebhookPath),
		"the app's own handler, refusing an unsigned delivery")

	require.NoError(t, l.apply(t.Context(), env, nil))
	assert.Nil(t, l.linearApp)
	assert.NotNil(t, l.linearPoll, "the poller is back")
	assert.False(t, l.webhooks.Holds(addr), "and the app's address is let go")
}

func TestWithNoKeyAndNoAppLinearIsOff(t *testing.T) {
	f := newFakeLinear(t)
	env := config.Config{LinearURL: f.srv.URL + "/graphql", LinearTokenURL: f.srv.URL + "/oauth/token"}
	l := running(t, env)
	assert.Nil(t, l.linearSource())

	addr := freeAddr(t)
	saved := f.savedApp(addr)
	require.NoError(t, l.apply(t.Context(), connections.Overlay(env, nil, nil, saved), saved))
	assert.NotNil(t, l.linearApp)
	require.NoError(t, l.apply(t.Context(), env, nil))
	assert.Nil(t, l.linearSource())
	assert.False(t, l.webhooks.Holds(addr))
}

// Every way the two apps' addresses can move, with the GitHub App's listener standing in for
// the GitHub source, which mounts the same way.
func TestTheLinearAppAndTheGitHubAppShareOrSplitTheirListener(t *testing.T) {
	f := newFakeLinear(t)
	env := config.Config{LinearURL: f.srv.URL + "/graphql", LinearTokenURL: f.srv.URL + "/oauth/token"}
	l := running(t, env)
	shared, apart := freeAddr(t), freeAddr(t)
	stopGitHub := serving(t, l, shared, ghreview.WebhookPath)
	// A source mounts from its own goroutine once apply has started it, so what is served
	// is waited for rather than read at once.
	held := func(addr string, want ...string) {
		t.Helper()
		require.Eventually(t, func() bool { return slices.Equal(l.webhooks.Paths(addr), want) },
			5*time.Second, 5*time.Millisecond, "%s serves %v, want %v", addr, l.webhooks.Paths(addr), want)
	}
	ctx := t.Context()
	saveOn := func(addr string) {
		t.Helper()
		saved := f.savedApp(addr)
		require.NoError(t, l.apply(ctx, connections.Overlay(env, nil, nil, saved), saved))
	}

	// Linear added on the GitHub App's address: one listener, both paths.
	saveOn(shared)
	held(shared, ghreview.WebhookPath, agentlinear.WebhookPath)
	assert.Equal(t, http.StatusOK, post(t, shared, ghreview.WebhookPath))
	assert.Equal(t, http.StatusUnauthorized, post(t, shared, agentlinear.WebhookPath))

	// Linear moved to an address of its own: GitHub keeps its listener, untouched.
	saveOn(apart)
	held(shared, ghreview.WebhookPath)
	held(apart, agentlinear.WebhookPath)
	assert.Equal(t, http.StatusNotFound, post(t, shared, agentlinear.WebhookPath))

	// And back again: the addresses become equal.
	saveOn(shared)
	held(shared, ghreview.WebhookPath, agentlinear.WebhookPath)
	assert.False(t, l.webhooks.Holds(apart), "the address Linear left is free")

	// Linear changed in place (a rotated secret) while GitHub runs: same listener.
	rotated := f.savedApp(shared)
	rotated.WebhookSecret = "lin_wh_rotated"
	require.NoError(t, l.apply(ctx, connections.Overlay(env, nil, nil, rotated), rotated))
	held(shared, ghreview.WebhookPath, agentlinear.WebhookPath)

	// GitHub removed while Linear runs: Linear keeps the listener.
	stopGitHub()
	held(shared, agentlinear.WebhookPath)
	assert.Equal(t, http.StatusUnauthorized, post(t, shared, agentlinear.WebhookPath))

	// GitHub added again while Linear runs: it joins Linear's listener.
	serving(t, l, shared, ghreview.WebhookPath)
	held(shared, ghreview.WebhookPath, agentlinear.WebhookPath)

	// Linear removed: GitHub keeps the listener.
	require.NoError(t, l.apply(ctx, env, nil))
	held(shared, ghreview.WebhookPath)
}

// A Linear app with no linear: true playbook is refused at the button, in the poller's words,
// rather than saved and then unable to start.
func TestCheckRefusesALinearAppWithNoPlaybook(t *testing.T) {
	f := newFakeLinear(t)
	l := newLive(config.Config{})
	l.linearPlaybook = func() string { return "" }
	_, err := l.Check(t.Context(), f.config(t))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no playbook sets `linear: true`")
	assert.Zero(t, f.grants, "nothing is asked of Linear for an app that could not run")
}

// Linear revoking the app stops it starting anything. Saving the same app again tries it
// against Linear once more, and a save that clears the revoke starts it again.
func TestARevokedLinearAppIsStartedAgainBySavingIt(t *testing.T) {
	f := newFakeLinear(t)
	env := config.Config{LinearURL: f.srv.URL + "/graphql", LinearTokenURL: f.srv.URL + "/oauth/token"}
	l := running(t, env)
	addr := freeAddr(t)
	saved := f.savedApp(addr)
	next := connections.Overlay(env, nil, nil, saved)
	require.NoError(t, l.apply(t.Context(), next, saved))
	require.Eventually(t, func() bool { return l.webhooks.Holds(addr) }, 5*time.Second, 5*time.Millisecond)

	body, err := json.Marshal(map[string]any{
		"type": "OAuthApp", "action": "revoked", "organizationId": "org-1",
		"webhookTimestamp": time.Now().UnixMilli(),
	})
	require.NoError(t, err)
	mac := hmac.New(sha256.New, []byte(saved.WebhookSecret))
	_, _ = mac.Write(body)
	req, err := http.NewRequest(http.MethodPost, "http://"+addr+agentlinear.WebhookPath, bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Linear-Signature", hex.EncodeToString(mac.Sum(nil)))
	res, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	_ = res.Body.Close()
	require.Equal(t, http.StatusOK, res.StatusCode)
	revokedApp := l.linearApp
	require.True(t, revokedApp.Revoked())

	// The row says revoked: nothing to restart.
	at := time.Now()
	marked := *saved
	marked.RevokedAt = &at
	require.NoError(t, l.apply(t.Context(), next, &marked))
	assert.Same(t, revokedApp, l.linearApp)

	grants := f.grantCount()
	_, err = l.Check(t.Context(), next)
	require.NoError(t, err)
	assert.Greater(t, f.grantCount(), grants, "an unchanged app that was revoked is tried again")

	// Saved again, which clears the revoke: a new source, listening.
	require.NoError(t, l.apply(t.Context(), next, saved))
	require.NotSame(t, revokedApp, l.linearApp)
	assert.False(t, l.linearApp.Revoked())
	require.Eventually(t, func() bool { return l.webhooks.Holds(addr) }, 5*time.Second, 5*time.Millisecond)
}
