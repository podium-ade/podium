package api

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeClaude is Anthropic's token endpoint and profile, as Claude Code talks to them.
type fakeClaude struct {
	srv *httptest.Server

	mu        sync.Mutex
	exchanged map[string]string
	refreshed map[string]string
	profiles  []http.Header
}

func newFakeClaude(t *testing.T) *fakeClaude {
	t.Helper()
	f := &fakeClaude{}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
		var body map[string]string
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		f.mu.Lock()
		defer f.mu.Unlock()
		switch body["grant_type"] {
		case "authorization_code":
			f.exchanged = body
			if body["code"] != "good-code" {
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"error": map[string]string{"type": "invalid_grant", "message": "Invalid authorization code"},
				})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token":  "sk-ant-oat01-access-1",
				"refresh_token": "sk-ant-ort01-refresh-1",
				"expires_in":    28800,
				"scope":         "user:profile user:inference",
				"account":       map[string]string{"uuid": "u-1", "email_address": "alice@example.test"},
			})
		case "refresh_token":
			f.refreshed = body
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token":  "sk-ant-oat01-access-2",
				"refresh_token": "sk-ant-ort01-refresh-2",
				"expires_in":    28800,
			})
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	})
	mux.HandleFunc("/api/oauth/profile", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.profiles = append(f.profiles, r.Header.Clone())
		f.mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer sk-ant-oat01-access-1" {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"type":  "error",
				"error": map[string]string{"type": "authentication_error", "message": "Invalid bearer token"},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"account": map[string]string{"email": "alice@example.test"}})
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeClaude) client(t *testing.T) *claudeClient {
	t.Helper()
	c := newClaudeClient("claude-test", "https://claude.example.test/oauth/authorize",
		f.srv.URL+"/v1/oauth/token", "https://platform.example.test/oauth/code/callback",
		"user:profile user:inference", f.srv.Client())
	require.NotNil(t, c)
	return c
}

func TestNoClaudeClientIDMeansNoSignIn(t *testing.T) {
	assert.Nil(t, newClaudeClient("", "https://a", "https://t", "https://r", "", http.DefaultClient))
	assert.Nil(t, newClaudeClient("id", "", "https://t", "https://r", "", http.DefaultClient))
	assert.NotNil(t, newClaudeClient("id", "https://a", "https://t", "https://r", "", http.DefaultClient))
}

// The authorize link is everything the browser is given: the challenge, never the verifier.
func TestTheClaudeAuthorizeLinkCarriesAChallengeAndNotTheVerifier(t *testing.T) {
	c := newFakeClaude(t).client(t)
	dev, err := c.startDevice(context.Background())
	require.NoError(t, err)
	assert.True(t, dev.CodePaste)
	assert.Empty(t, dev.UserCode)

	u, err := url.Parse(dev.VerificationURI)
	require.NoError(t, err)
	q := u.Query()
	assert.Equal(t, "claude.example.test", u.Host)
	assert.Equal(t, "true", q.Get("code"))
	assert.Equal(t, "claude-test", q.Get("client_id"))
	assert.Equal(t, "code", q.Get("response_type"))
	assert.Equal(t, "https://platform.example.test/oauth/code/callback", q.Get("redirect_uri"))
	assert.Equal(t, "user:profile user:inference", q.Get("scope"))
	assert.Equal(t, "S256", q.Get("code_challenge_method"))
	assert.Equal(t, dev.State, q.Get("state"))

	sum := sha256.Sum256([]byte(dev.DeviceCode))
	assert.Equal(t, base64.RawURLEncoding.EncodeToString(sum[:]), q.Get("code_challenge"))
	assert.NotContains(t, dev.VerificationURI, dev.DeviceCode, "the verifier never leaves the conductor")
}

func TestAClaudeFlowWithNoCodeIsPendingWithoutAskingAnthropic(t *testing.T) {
	f := newFakeClaude(t)
	state, tok, _, err := f.client(t).pollFlow(context.Background(), &oauthFlow{deviceCode: "v", state: "s"})
	require.NoError(t, err)
	assert.Equal(t, OAuthPending, state)
	assert.Nil(t, tok)
	assert.Nil(t, f.exchanged)
}

func TestThePastedClaudeCodeIsExchangedWithTheVerifier(t *testing.T) {
	for name, pasted := range map[string]string{
		"code#state":   "good-code#st-1",
		"callback URL": "https://platform.example.test/oauth/code/callback?code=good-code&state=st-1",
		"bare code":    "  good-code  ",
	} {
		t.Run(name, func(t *testing.T) {
			f := newFakeClaude(t)
			state, tok, _, err := f.client(t).pollFlow(context.Background(), &oauthFlow{
				deviceCode: "verifier-1", state: "st-1", code: pasted, codePaste: true,
			})
			require.NoError(t, err)
			require.Equal(t, OAuthDone, state)
			assert.Equal(t, "sk-ant-oat01-access-1", tok.AccessToken)
			assert.Equal(t, "sk-ant-ort01-refresh-1", tok.RefreshToken)
			assert.Equal(t, "alice@example.test", account(tok))
			assert.Equal(t, map[string]string{
				"grant_type":    "authorization_code",
				"code":          "good-code",
				"redirect_uri":  "https://platform.example.test/oauth/code/callback",
				"client_id":     "claude-test",
				"code_verifier": "verifier-1",
				"state":         "st-1",
			}, f.exchanged)
		})
	}
}

func TestACodeFromAnotherSignInIsRefusedBeforeAnthropicIsAsked(t *testing.T) {
	f := newFakeClaude(t)
	state, _, detail, err := f.client(t).pollFlow(context.Background(), &oauthFlow{
		deviceCode: "verifier-1", state: "st-1", code: "good-code#someone-elses",
	})
	require.NoError(t, err)
	assert.Equal(t, OAuthDenied, state)
	assert.Contains(t, detail, "different sign-in")
	assert.Nil(t, f.exchanged)
}

func TestAWrongClaudeCodeIsDeniedInAnthropicsWords(t *testing.T) {
	f := newFakeClaude(t)
	state, tok, detail, err := f.client(t).pollFlow(context.Background(), &oauthFlow{
		deviceCode: "verifier-1", state: "st-1", code: "typo#st-1",
	})
	require.NoError(t, err)
	assert.Equal(t, OAuthDenied, state)
	assert.Nil(t, tok)
	assert.Equal(t, "Invalid authorization code", detail)
}

func TestClaudeRefreshTradesTheTokenIn(t *testing.T) {
	f := newFakeClaude(t)
	tok, err := f.client(t).refresh(context.Background(), "sk-ant-ort01-refresh-1")
	require.NoError(t, err)
	assert.Equal(t, "sk-ant-oat01-access-2", tok.AccessToken)
	assert.Equal(t, "sk-ant-ort01-refresh-2", tok.RefreshToken)
	assert.Equal(t, "sk-ant-ort01-refresh-1", f.refreshed["refresh_token"])
	assert.Equal(t, "claude-test", f.refreshed["client_id"])
}

func TestAClaudeTokenIsValidatedByReadingItsProfile(t *testing.T) {
	f := newFakeClaude(t)
	_, err := validateClaudeOAuthToken(context.Background(), f.srv.Client(), f.srv.URL,
		[]byte("sk-ant-oat01-access-1"))
	require.NoError(t, err)
	require.Len(t, f.profiles, 1)
	assert.Equal(t, claudeOAuthBeta, f.profiles[0].Get("anthropic-beta"))
	assert.Empty(t, f.profiles[0].Get("x-api-key"), "a sign-in is a bearer, not an API key")

	_, err = validateClaudeOAuthToken(context.Background(), f.srv.Client(), f.srv.URL, []byte("sk-ant-oat01-dead"))
	require.ErrorIs(t, err, errKeyRefused)
	assert.Equal(t, "Invalid bearer token", validationDetail(err))
}
