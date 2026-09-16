package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeCodex struct {
	srv       *httptest.Server
	approved  atomic.Bool
	polls     atomic.Int32
	refreshes atomic.Int32
}

func newFakeCodex(t *testing.T) *fakeCodex {
	t.Helper()
	f := &fakeCodex{}
	mux := http.NewServeMux()
	mux.HandleFunc(codexUserCodePath, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		var body map[string]string
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		assert.Equal(t, "podium-test", body["client_id"])
		_ = json.NewEncoder(w).Encode(map[string]any{
			"user_code":      "WDJB-MJHT",
			"device_auth_id": "dev-auth-1",
			"expires_in":     600,
			"interval":       5,
		})
	})
	mux.HandleFunc(codexPollPath, func(w http.ResponseWriter, r *http.Request) {
		f.polls.Add(1)
		var body map[string]string
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		assert.Equal(t, "dev-auth-1", body["device_auth_id"])
		assert.Equal(t, "WDJB-MJHT", body["user_code"])
		if !f.approved.Load() {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{
			"authorization_code": "auth-code-1",
			"code_verifier":      "verifier-1",
		})
	})
	mux.HandleFunc(codexTokenPath, func(w http.ResponseWriter, r *http.Request) {
		assert.NoError(t, r.ParseForm())
		switch r.Form.Get("grant_type") {
		case "authorization_code":
			assert.Equal(t, "auth-code-1", r.Form.Get("code"))
			assert.Equal(t, "verifier-1", r.Form.Get("code_verifier"))
			assert.Equal(t, "podium-test", r.Form.Get("client_id"))
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token":  "access-1",
				"refresh_token": "refresh-1",
				"expires_in":    3600,
				"token_type":    "Bearer",
			})
		case refreshGrant:
			f.refreshes.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token":  "access-2",
				"refresh_token": "refresh-2",
				"expires_in":    3600,
			})
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	})
	f.srv = httptest.NewTLSServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeCodex) client(t *testing.T) *codexClient {
	t.Helper()
	c := newCodexClient(f.srv.URL, "podium-test", f.srv.Client())
	require.NotNil(t, c)
	return c
}

func TestNoCodexClientIDMeansNoOAuthAtAll(t *testing.T) {
	assert.Nil(t, newCodexClient("https://auth.openai.com", "", http.DefaultClient))
	assert.Nil(t, newCodexClient("", "app_x", http.DefaultClient))
	assert.NotNil(t, newCodexClient("https://auth.openai.com", "app_x", http.DefaultClient))
}

func TestTheCodexDeviceFlowEndToEnd(t *testing.T) {
	idp := newFakeCodex(t)
	c := idp.client(t)
	ctx := context.Background()

	dev, err := c.startDevice(ctx)
	require.NoError(t, err)
	assert.Equal(t, "WDJB-MJHT", dev.UserCode)
	assert.Equal(t, idp.srv.URL+"/codex/device", dev.VerificationURI)
	assert.Equal(t, "dev-auth-1", dev.DeviceCode)
	assert.Equal(t, 5, dev.Interval)

	flow := &oauthFlow{deviceCode: dev.DeviceCode, userCode: dev.UserCode}
	state, tok, _, err := c.pollFlow(ctx, flow)
	require.NoError(t, err)
	assert.Equal(t, OAuthPending, state, "nobody has approved it yet")
	assert.Nil(t, tok)

	idp.approved.Store(true)
	state, tok, _, err = c.pollFlow(ctx, flow)
	require.NoError(t, err)
	require.Equal(t, OAuthDone, state)
	require.NotNil(t, tok)
	assert.Equal(t, "access-1", tok.AccessToken)
	assert.Equal(t, "refresh-1", tok.RefreshToken)
	assert.Equal(t, int32(2), idp.polls.Load())
	assert.Zero(t, idp.refreshes.Load())
}

func TestCodexRefreshTradesTheTokenIn(t *testing.T) {
	idp := newFakeCodex(t)
	tok, err := idp.client(t).refresh(context.Background(), "refresh-1")
	require.NoError(t, err)
	assert.Equal(t, "access-2", tok.AccessToken)
	assert.Equal(t, "refresh-2", tok.RefreshToken)
}

func TestCodexPollTreats403AsPending(t *testing.T) {
	idp := newFakeCodex(t)
	state, tok, _, err := idp.client(t).pollFlow(context.Background(), &oauthFlow{
		deviceCode: "dev-auth-1", userCode: "WDJB-MJHT",
	})
	require.NoError(t, err)
	assert.Equal(t, OAuthPending, state)
	assert.Nil(t, tok)
}

func TestIntervalOfAcceptsAString(t *testing.T) {
	assert.Equal(t, 7, intervalOf([]byte(`"7"`)))
	assert.Equal(t, 4, intervalOf([]byte(`4`)))
	assert.Equal(t, 5, intervalOf(nil))
}
