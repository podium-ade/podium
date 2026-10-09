//go:build integration

package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/agent/connections"
	agentv1 "github.com/podium-ade/podium/internal/proto/podium/agent/v1"
)

const githubRedirect = "https://podium.example.ts.net/agent/github/callback"

// fakeGitHub answers the token exchange and /user. expiring makes it issue the eight-hour
// tokens a GitHub App gives by default.
func fakeGitHub(t *testing.T, expiring bool) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /login/oauth/access_token", func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseForm())
		if r.Form.Get("code") != "good-code" || r.Form.Get("client_secret") != "csec" ||
			r.Form.Get("code_verifier") == "" || r.Form.Get("redirect_uri") != githubRedirect {
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "bad_verification_code"})
			return
		}
		out := map[string]any{"access_token": "ghu_user_token", "token_type": "bearer"}
		if expiring {
			out["expires_in"] = 28800
			out["refresh_token"] = "ghr_refresh"
		}
		_ = json.NewEncoder(w).Encode(out)
	})
	mux.HandleFunc("GET /user", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer ghu_user_token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": 4242, "login": "ada-gh", "name": "Ada L"})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func newGitHubAccountFixture(t *testing.T, expiring bool) settingsFixture {
	t.Helper()
	gh := fakeGitHub(t, expiring)
	f := newSettingsFixture(t, "https://example.invalid", gh.Client())
	f.svc.githubURL = gh.URL
	f.svc.githubAPIURL = gh.URL
	require.NoError(t, f.svc.store.PutSetting(t.Context(), connections.GitHubKey, connections.GitHub{
		AppID: "1", PrivateKey: "pem", ClientID: "Iv23abc", ClientSecret: "csec",
	}))
	return f
}

func startGitHub(t *testing.T, f settingsFixture, login string) *agentv1.StartGitHubOAuthResponse {
	t.Helper()
	res, err := f.svc.StartGitHubOAuth(loginCtx(login), connect.NewRequest(&agentv1.StartGitHubOAuthRequest{
		RedirectUri: githubRedirect,
	}))
	require.NoError(t, err)
	return res.Msg
}

func TestConnectingGitHubStoresTheTokenAsThePersonsSecret(t *testing.T) {
	f := newGitHubAccountFixture(t, false)

	before, err := f.svc.GetGitHubAccount(loginCtx("ada@acme.com"), connect.NewRequest(&agentv1.GetGitHubAccountRequest{}))
	require.NoError(t, err)
	assert.True(t, before.Msg.GetAccount().GetAvailable())
	assert.False(t, before.Msg.GetAccount().GetConnected())

	start := startGitHub(t, f, "ada@acme.com")
	u, err := url.Parse(start.GetAuthorizeUrl())
	require.NoError(t, err)
	assert.Equal(t, "/login/oauth/authorize", u.Path)
	assert.Equal(t, "Iv23abc", u.Query().Get("client_id"))
	assert.Equal(t, "S256", u.Query().Get("code_challenge_method"))
	assert.Equal(t, start.GetState(), u.Query().Get("state"))
	assert.NotContains(t, start.GetAuthorizeUrl(), "csec")

	done, err := f.svc.CompleteGitHubOAuth(loginCtx("ada@acme.com"), connect.NewRequest(&agentv1.CompleteGitHubOAuthRequest{
		FlowId: start.GetFlowId(), State: start.GetState(), Code: "good-code",
	}))
	require.NoError(t, err)
	acct := done.Msg.GetAccount()
	assert.True(t, acct.GetConnected())
	assert.Equal(t, "ada-gh", acct.GetGithubLogin())
	assert.Equal(t, int64(4242), acct.GetGithubId())
	assert.Equal(t, "ghu_user_token", string(f.secrets.set["github.token"]))
	assert.Equal(t, "ada@acme.com", f.secrets.owners["github.token"])
	assert.NotContains(t, done.Msg.String(), "ghu_user_token")
	assert.NotContains(t, f.log.String(), "ghu_user_token")
	assert.NotContains(t, f.log.String(), "good-code")

	// Bob sees nothing of Ada's.
	bob, err := f.svc.GetGitHubAccount(loginCtx("bob@acme.com"), connect.NewRequest(&agentv1.GetGitHubAccountRequest{}))
	require.NoError(t, err)
	assert.False(t, bob.Msg.GetAccount().GetConnected())

	off, err := f.svc.DisconnectGitHubAccount(loginCtx("ada@acme.com"), connect.NewRequest(&agentv1.DisconnectGitHubAccountRequest{}))
	require.NoError(t, err)
	assert.False(t, off.Msg.GetAccount().GetConnected())
	assert.NotContains(t, f.secrets.set, "github.token")
	_, err = f.svc.DisconnectGitHubAccount(loginCtx("ada@acme.com"), connect.NewRequest(&agentv1.DisconnectGitHubAccountRequest{}))
	require.NoError(t, err, "disconnecting twice is not an error")
}

func TestAGitHubFlowBelongsToThePersonWhoStartedIt(t *testing.T) {
	f := newGitHubAccountFixture(t, false)
	start := startGitHub(t, f, "ada@acme.com")

	_, err := f.svc.CompleteGitHubOAuth(loginCtx("bob@acme.com"), connect.NewRequest(&agentv1.CompleteGitHubOAuthRequest{
		FlowId: start.GetFlowId(), State: start.GetState(), Code: "good-code",
	}))
	assert.Equal(t, connect.CodeDeadlineExceeded, connect.CodeOf(err))
	assert.Empty(t, f.secrets.set)

	_, err = f.svc.CompleteGitHubOAuth(loginCtx("ada@acme.com"), connect.NewRequest(&agentv1.CompleteGitHubOAuthRequest{
		FlowId: start.GetFlowId(), State: "wrong", Code: "good-code",
	}))
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	assert.Empty(t, f.secrets.set)
}

func TestConnectingGitHubRefusesExpiringTokens(t *testing.T) {
	f := newGitHubAccountFixture(t, true)
	start := startGitHub(t, f, "ada@acme.com")
	_, err := f.svc.CompleteGitHubOAuth(loginCtx("ada@acme.com"), connect.NewRequest(&agentv1.CompleteGitHubOAuthRequest{
		FlowId: start.GetFlowId(), State: start.GetState(), Code: "good-code",
	}))
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	assert.ErrorContains(t, err, "token expiration")
	assert.Empty(t, f.secrets.set)
}

func TestConnectingGitHubNeedsAPersonAndAClient(t *testing.T) {
	f := newGitHubAccountFixture(t, false)
	_, err := f.svc.StartGitHubOAuth(loginCtx("local"), connect.NewRequest(&agentv1.StartGitHubOAuthRequest{
		RedirectUri: githubRedirect,
	}))
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))

	_, err = f.svc.StartGitHubOAuth(loginCtx("ada@acme.com"), connect.NewRequest(&agentv1.StartGitHubOAuthRequest{
		RedirectUri: "https://podium.example.ts.net/elsewhere",
	}))
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	require.NoError(t, f.svc.store.PutSetting(t.Context(), connections.GitHubKey, connections.GitHub{
		AppID: "1", PrivateKey: "pem",
	}))
	_, err = f.svc.StartGitHubOAuth(loginCtx("ada@acme.com"), connect.NewRequest(&agentv1.StartGitHubOAuthRequest{
		RedirectUri: githubRedirect,
	}))
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
}
