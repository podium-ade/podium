package agent

import (
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/agent/config"
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
	return &liveConnections{logger: slog.New(slog.DiscardHandler), running: running}
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

// The GitHub App's listener is this process's own, and the Linear app may share it, so
// the address being taken is not a refusal.
func TestCheckAcceptsTheGitHubListenerForLinear(t *testing.T) {
	f := newFakeLinear(t)
	held, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = held.Close() })
	running := config.Config{
		GitHubAppID: "1", GitHubAppKey: "pem",
		GitHubWebhookSecret: "whsec", GitHubWebhookListen: held.Addr().String(),
	}
	require.True(t, running.GitHubSourceEnabled())
	cfg := running
	app := f.config(t)
	cfg.LinearURL, cfg.LinearTokenURL = app.LinearURL, app.LinearTokenURL
	cfg.LinearClientID, cfg.LinearClientSecret = app.LinearClientID, app.LinearClientSecret
	cfg.LinearWebhookSecret = app.LinearWebhookSecret
	cfg.LinearWebhookListen = held.Addr().String()

	checked, err := newLive(running).Check(t.Context(), cfg)
	require.NoError(t, err)
	assert.NotNil(t, checked.Linear)
}
