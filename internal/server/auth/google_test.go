package auth

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStartURLRequiresWorkspaceConfig(t *testing.T) {
	t.Parallel()
	f := &Flow{}
	_, err := f.startURL(httptest.NewRequest(http.MethodGet, "http://localhost:8080"+StartPath, nil), "")
	require.Error(t, err)
}

func TestStartURLIncludesPKCEAndOptionalHD(t *testing.T) {
	t.Parallel()
	f := &Flow{Google: Google{
		ClientID:     "cid",
		ClientSecret: "csecret",
		AuthURL:      "https://accounts.google.com/o/oauth2/v2/auth",
		PublicURL:    "https://podium.acme.com",
	}}
	req := httptest.NewRequest(http.MethodGet, "https://podium.acme.com"+StartPath, nil)
	loc, err := f.startURL(req, "acme.com")
	require.NoError(t, err)
	u, err := url.Parse(loc)
	require.NoError(t, err)
	q := u.Query()
	require.Equal(t, "cid", q.Get("client_id"))
	require.Equal(t, "https://podium.acme.com/auth/google/callback", q.Get("redirect_uri"))
	require.Equal(t, "S256", q.Get("code_challenge_method"))
	require.NotEmpty(t, q.Get("code_challenge"))
	require.NotEmpty(t, q.Get("state"))
	require.Equal(t, "acme.com", q.Get("hd"))
	require.Contains(t, q.Get("scope"), "email")
}

func TestExchangeAcceptsWorkspaceUserinfo(t *testing.T) {
	t.Parallel()
	token, userinfo := googleServers(t, http.StatusOK, `{"access_token":"atok"}`, http.StatusOK, `{
		"email": "Alice@Acme.com",
		"email_verified": true,
		"hd": "acme.com",
		"name": "Alice"
	}`)
	f := &Flow{Google: Google{
		ClientID:     "cid",
		ClientSecret: "sec",
		TokenURL:     token,
		UserInfoURL:  userinfo,
	}}
	got, err := f.exchange(t.Context(), "code", "verifier", "https://podium.acme.com/auth/google/callback")
	require.NoError(t, err)
	require.Equal(t, "alice@acme.com", got.Email)
	require.Equal(t, "acme.com", got.HD)
	require.Equal(t, "Alice", got.Name)
}

func TestExchangeRejectsConsumerAccounts(t *testing.T) {
	t.Parallel()
	token, userinfo := googleServers(t, http.StatusOK, `{"access_token":"atok"}`, http.StatusOK, `{
		"email": "alice@gmail.com",
		"email_verified": true,
		"name": "Alice"
	}`)
	f := &Flow{Google: Google{ClientID: "c", ClientSecret: "s", TokenURL: token, UserInfoURL: userinfo}}
	_, err := f.exchange(t.Context(), "c", "v", "https://x/callback")
	require.ErrorIs(t, err, errNotWorkspace)
}

func TestExchangeRejectsUnverifiedEmail(t *testing.T) {
	t.Parallel()
	token, userinfo := googleServers(t, http.StatusOK, `{"access_token":"atok"}`, http.StatusOK, `{
		"email": "alice@acme.com",
		"email_verified": false,
		"hd": "acme.com"
	}`)
	f := &Flow{Google: Google{ClientID: "c", ClientSecret: "s", TokenURL: token, UserInfoURL: userinfo}}
	_, err := f.exchange(t.Context(), "c", "v", "https://x/callback")
	require.ErrorIs(t, err, errUnverified)
}

func TestExchangeRejectsTokenHTTPError(t *testing.T) {
	t.Parallel()
	token, userinfo := googleServers(t, http.StatusBadRequest, `{"error":"invalid_grant"}`, http.StatusOK, `{}`)
	f := &Flow{Google: Google{ClientID: "c", ClientSecret: "s", TokenURL: token, UserInfoURL: userinfo}}
	_, err := f.exchange(t.Context(), "c", "v", "https://x/callback")
	require.Error(t, err)
}

func TestStartThenTakePending(t *testing.T) {
	t.Parallel()
	f := &Flow{Google: Google{ClientID: "c", ClientSecret: "s", AuthURL: "https://example.test/auth"}}
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080"+StartPath, nil)
	loc, err := f.startURL(req, "")
	require.NoError(t, err)
	u, err := url.Parse(loc)
	require.NoError(t, err)
	state := u.Query().Get("state")
	p, ok := f.takePending(state)
	require.True(t, ok)
	require.Equal(t, "http://127.0.0.1:8080/auth/google/callback", p.redirectURI)
	require.NotEmpty(t, p.verifier)
	_, ok = f.takePending(state)
	require.False(t, ok, "state is single-use")
}

func googleServers(t *testing.T, tokenStatus int, tokenBody string, infoStatus int, infoBody string) (tokenURL, userInfoURL string) {
	t.Helper()
	token := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		b, _ := io.ReadAll(r.Body)
		require.Contains(t, string(b), "code=")
		w.WriteHeader(tokenStatus)
		_, _ = w.Write([]byte(tokenBody))
	}))
	t.Cleanup(token.Close)
	info := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(infoStatus)
		_, _ = w.Write([]byte(infoBody))
	}))
	t.Cleanup(info.Close)
	return token.URL, info.URL
}

func TestStatusJSONShape(t *testing.T) {
	t.Parallel()
	h := NewHandler(&Flow{Google: Google{ClientID: "c", ClientSecret: "s"}}, nil)
	mux := http.NewServeMux()
	h.Register(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, StatusPath, nil))
	require.Equal(t, http.StatusOK, rec.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Equal(t, true, body["google"])
	require.Equal(t, false, body["claimed"])
	require.Equal(t, "", body["hosted_domain"])
}

func TestStart404WhenDisabled(t *testing.T) {
	t.Parallel()
	h := NewHandler(&Flow{}, nil)
	mux := http.NewServeMux()
	h.Register(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, StartPath, nil))
	require.Equal(t, http.StatusNotFound, rec.Code)
}

func TestCallbackDenied(t *testing.T) {
	t.Parallel()
	h := NewHandler(&Flow{Google: Google{ClientID: "c", ClientSecret: "s", PublicURL: "http://podium.test"}}, nil)
	mux := http.NewServeMux()
	h.Register(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, CallbackPath+"?error=access_denied", nil))
	require.Equal(t, http.StatusFound, rec.Code)
	require.Equal(t, "http://podium.test/?auth_error=denied", rec.Header().Get("Location"))
}

func TestOriginOfPrefersPublicURL(t *testing.T) {
	t.Parallel()
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080/", nil)
	req.Host = "127.0.0.1:8080"
	require.Equal(t, "https://podium.acme.com", originOf(req, "https://podium.acme.com/"))
	require.Equal(t, "http://127.0.0.1:8080", originOf(req, ""))
}
