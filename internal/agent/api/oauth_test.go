package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeIDP is a device-authorisation issuer: discovery, a device code, a token endpoint that
// answers authorization_pending until it is told not to, and a refresh grant.
type fakeIDP struct {
	srv *httptest.Server
	// approved flips the token endpoint from pending to authorised.
	approved atomic.Bool
	// tokenErr, when set, is the OAuth error the token endpoint answers with instead.
	tokenErr atomic.Value // string
	// polls counts what reached the token endpoint under the device grant.
	polls atomic.Int32
	// refreshes counts refresh-grant calls, and lastRefresh is the token they carried.
	refreshes   atomic.Int32
	lastRefresh atomic.Value // string
	// deviceEndpoint and tokenEndpoint override what discovery advertises, for the tests
	// that check a hostile document is refused.
	deviceEndpoint string
	tokenEndpoint  string
	// noDeviceGrant drops device_authorization_endpoint from the document.
	noDeviceGrant bool
}

func newFakeIDP(t *testing.T) *fakeIDP {
	t.Helper()
	idp := &fakeIDP{}
	mux := http.NewServeMux()
	mux.HandleFunc(discoveryPath, func(w http.ResponseWriter, _ *http.Request) {
		doc := map[string]string{
			"issuer":                        idp.srv.URL,
			"device_authorization_endpoint": idp.srv.URL + "/oauth2/device",
			"token_endpoint":                idp.srv.URL + "/oauth2/token",
		}
		if idp.deviceEndpoint != "" {
			doc["device_authorization_endpoint"] = idp.deviceEndpoint
		}
		if idp.tokenEndpoint != "" {
			doc["token_endpoint"] = idp.tokenEndpoint
		}
		if idp.noDeviceGrant {
			delete(doc, "device_authorization_endpoint")
		}
		_ = json.NewEncoder(w).Encode(doc)
	})
	mux.HandleFunc("/oauth2/device", func(w http.ResponseWriter, r *http.Request) {
		assert.NoError(t, r.ParseForm())
		assert.Equal(t, "podium-test", r.Form.Get("client_id"))
		assert.Contains(t, r.Form.Get("scope"), "offline_access")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"device_code":               "dev-code-1",
			"user_code":                 "WDJB-MJHT",
			"verification_uri":          "https://accounts.example.test/device",
			"verification_uri_complete": "https://accounts.example.test/device?user_code=WDJB-MJHT",
			"expires_in":                600,
			"interval":                  5,
		})
	})
	mux.HandleFunc("/oauth2/token", func(w http.ResponseWriter, r *http.Request) {
		assert.NoError(t, r.ParseForm())
		if r.Form.Get("grant_type") == refreshGrant {
			idp.refreshes.Add(1)
			idp.lastRefresh.Store(r.Form.Get("refresh_token"))
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token":  "access-2",
				"refresh_token": "refresh-2",
				"expires_in":    3600,
			})
			return
		}
		idp.polls.Add(1)
		assert.Equal(t, deviceGrant, r.Form.Get("grant_type"))
		assert.Equal(t, "dev-code-1", r.Form.Get("device_code"))
		if e, _ := idp.tokenErr.Load().(string); e != "" {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"error": e, "error_description": "the provider's own words about " + e,
			})
			return
		}
		if !idp.approved.Load() {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "authorization_pending"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "access-1",
			"refresh_token": "refresh-1",
			"id_token":      idToken(`{"email":"someone@example.test"}`),
			"expires_in":    3600,
			"token_type":    "Bearer",
		})
	})
	// TLS, because discovery refuses a plaintext endpoint and this fake has to be able to
	// advertise a usable one. srv.Client() trusts the certificate.
	idp.srv = httptest.NewTLSServer(mux)
	t.Cleanup(idp.srv.Close)
	return idp
}

// idToken is a JWT with a payload and nothing else that matters. The signature is not
// checked — see the note on account() — so it is not signed.
func idToken(payload string) string {
	enc := base64.RawURLEncoding.EncodeToString
	return enc([]byte(`{"alg":"none"}`)) + "." + enc([]byte(payload)) + ".sig"
}

func (f *fakeIDP) client(t *testing.T) *oauthClient {
	t.Helper()
	c := newOAuthClient(f.srv.URL, "podium-test", "openid offline_access", f.srv.Client())
	require.NotNil(t, c)
	return c
}

// An install with no client id offers API keys only. That is a configuration, not a failure,
// and it has to be distinguishable from a client that is merely broken.
func TestNoClientIDMeansNoOAuthAtAll(t *testing.T) {
	assert.Nil(t, newOAuthClient("https://auth.example.test", "", "", http.DefaultClient))
	assert.Nil(t, newOAuthClient("", "podium-test", "", http.DefaultClient))
	assert.NotNil(t, newOAuthClient("https://auth.example.test", "podium-test", "", http.DefaultClient))
}

func TestTheDeviceFlowEndToEnd(t *testing.T) {
	idp := newFakeIDP(t)
	c := idp.client(t)
	ctx := context.Background()

	dev, err := c.startDevice(ctx)
	require.NoError(t, err)
	assert.Equal(t, "WDJB-MJHT", dev.UserCode)
	assert.Equal(t, "https://accounts.example.test/device", dev.VerificationURI)
	assert.Equal(t, 5, dev.Interval)

	state, tok, _, err := c.pollDevice(ctx, dev.DeviceCode)
	require.NoError(t, err)
	assert.Equal(t, OAuthPending, state, "nobody has approved it yet")
	assert.Nil(t, tok)

	idp.approved.Store(true)
	state, tok, _, err = c.pollDevice(ctx, dev.DeviceCode)
	require.NoError(t, err)
	require.Equal(t, OAuthDone, state)
	require.NotNil(t, tok)
	assert.Equal(t, "access-1", tok.AccessToken)
	assert.Equal(t, "refresh-1", tok.RefreshToken)
	assert.Equal(t, "someone@example.test", account(tok))
	assert.Equal(t, int32(2), idp.polls.Load(), "one poll per call and no retry loop in here")
	assert.Zero(t, idp.refreshes.Load(), "a fresh sign-in refreshes nothing")
}

// The three "keep waiting" errors of RFC 8628 are not failures, and everything else that
// ends a flow has to arrive with the provider's own sentence attached.
func TestPollDeviceMapsTheProvidersErrors(t *testing.T) {
	tests := []struct {
		oauthError string
		want       string
	}{
		{"authorization_pending", OAuthPending},
		{"slow_down", OAuthSlowDown},
		{"access_denied", OAuthDenied},
		{"expired_token", OAuthExpired},
		// An unregistered client, or a subscription tier the OAuth surface does not allow.
		// It ends the flow, and only the provider's words say which.
		{"unauthorized_client", OAuthDenied},
	}
	for _, tc := range tests {
		t.Run(tc.oauthError, func(t *testing.T) {
			idp := newFakeIDP(t)
			idp.tokenErr.Store(tc.oauthError)
			state, tok, detail, err := idp.client(t).pollDevice(context.Background(), "dev-code-1")
			require.NoError(t, err)
			assert.Equal(t, tc.want, state)
			assert.Nil(t, tok)
			if tc.want != OAuthPending && tc.want != OAuthSlowDown {
				assert.Contains(t, detail, tc.oauthError)
			}
		})
	}
}

func TestRefreshTradesTheTokenIn(t *testing.T) {
	idp := newFakeIDP(t)
	tok, err := idp.client(t).refresh(context.Background(), "refresh-1")
	require.NoError(t, err)
	assert.Equal(t, "access-2", tok.AccessToken)
	assert.Equal(t, "refresh-2", tok.RefreshToken, "a rotated refresh token has to be kept")
	assert.Equal(t, "refresh-1", idp.lastRefresh.Load())
}

// A revoked refresh token is the provider saying no, and the background pass has to be able
// to tell that apart from a network failure: one is worth retrying and the other is not.
func TestRefreshReportsARefusalRatherThanReturningNothing(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, discoveryPath) {
			_ = json.NewEncoder(w).Encode(map[string]string{
				"device_authorization_endpoint": "https://" + r.Host + "/d",
				"token_endpoint":                "https://" + r.Host + "/t",
			})
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"refresh token revoked"}`))
	}))
	defer srv.Close()

	c := newOAuthClient(srv.URL, "podium-test", "", srv.Client())
	require.NotNil(t, c)
	_, err := c.refresh(context.Background(), "refresh-1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "refresh token revoked")
}

// Discovery is the one place a hostile answer could redirect a credential, so every endpoint
// it names is checked back against the issuer's own host before anything is POSTed to it.
func TestDiscoveryRefusesAnEndpointOffTheIssuersSite(t *testing.T) {
	idp := newFakeIDP(t)
	idp.tokenEndpoint = "https://evil.test/oauth2/token"
	_, err := idp.client(t).startDevice(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "token_endpoint")
	assert.Contains(t, err.Error(), "evil.test")
}

func TestDiscoveryRefusesAPlaintextEndpoint(t *testing.T) {
	idp := newFakeIDP(t)
	idp.deviceEndpoint = "http://" + strings.TrimPrefix(idp.srv.URL, "https://") + "/oauth2/device"
	_, err := idp.client(t).startDevice(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is not https")
}

func TestDiscoverySaysSoWhenTheIssuerHasNoDeviceGrant(t *testing.T) {
	idp := newFakeIDP(t)
	idp.noDeviceGrant = true
	_, err := idp.client(t).startDevice(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "device_authorization_endpoint")
}

func TestSameSite(t *testing.T) {
	tests := []struct {
		issuer, endpoint string
		ok               bool
	}{
		{"https://auth.x.ai", "https://auth.x.ai/oauth2/token", true},
		// A sibling subdomain of the same operator is the case this is meant to accept.
		{"https://auth.x.ai", "https://api.x.ai/oauth2/token", true},
		{"https://auth.x.ai", "https://x.ai/oauth2/token", true},
		{"https://auth.x.ai", "https://x.ai.evil.test/oauth2/token", false},
		{"https://auth.x.ai", "https://evil.test/oauth2/token", false},
		{"https://auth.x.ai", "http://auth.x.ai/oauth2/token", false},
		{"https://auth.x.ai", "not a url at all", false},
	}
	for _, tc := range tests {
		t.Run(tc.endpoint, func(t *testing.T) {
			err := sameSite(tc.issuer, tc.endpoint)
			if tc.ok {
				assert.NoError(t, err)
			} else {
				assert.Error(t, err)
			}
		})
	}
}

func TestDiscoveryIsCachedSoAPollIsOneRequest(t *testing.T) {
	idp := newFakeIDP(t)
	c := idp.client(t)
	ctx := context.Background()
	first, err := c.discover(ctx)
	require.NoError(t, err)
	second, err := c.discover(ctx)
	require.NoError(t, err)
	assert.Same(t, first, second)
}

// The claim is only ever shown on a page, so the first one that has anything in it wins and
// a token with none is not an error.
func TestAccountPrefersAnEmailAndToleratesAnythingElse(t *testing.T) {
	assert.Equal(t, "a@b.test", account(&tokenResponse{IDToken: idToken(`{"email":"a@b.test","sub":"x"}`)}))
	assert.Equal(t, "someone", account(&tokenResponse{IDToken: idToken(`{"preferred_username":"someone"}`)}))
	assert.Equal(t, "sub-1", account(&tokenResponse{IDToken: idToken(`{"sub":"sub-1"}`)}))
	assert.Empty(t, account(&tokenResponse{}))
	assert.Empty(t, account(&tokenResponse{IDToken: "not.a.jwt.at.all"}))
	assert.Empty(t, account(&tokenResponse{IDToken: idToken(`not json`)}))
}
