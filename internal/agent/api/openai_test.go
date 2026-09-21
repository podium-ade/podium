package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const fakeOpenAIKey = "sk-not-a-real-key-0000wxyz"
const fakeOpenAIRefusal = "Incorrect API key provided."

func fakeOpenAI(t *testing.T, good ...string) *httptest.Server {
	t.Helper()
	accepted := map[string]bool{}
	for _, g := range good {
		accepted["Bearer "+g] = true
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models", "/models":
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if !accepted[r.Header.Get("Authorization")] {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"message":` + strconv.Quote(fakeOpenAIRefusal) +
				`,"type":"invalid_request_error"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"gpt-5.4"},{"id":"gpt-5.3-codex"}]}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestValidateOpenAIKeyAcceptsAGoodKey(t *testing.T) {
	srv := fakeOpenAI(t, fakeOpenAIKey)
	models, err := validateOpenAIKey(context.Background(), srv.Client(), srv.URL, []byte(fakeOpenAIKey))
	require.NoError(t, err)
	assert.Equal(t, []string{"gpt-5.4", "gpt-5.3-codex"}, models)
}

func TestValidateOpenAIKeyPassesTheProvidersOwnWordsOn(t *testing.T) {
	srv := fakeOpenAI(t, fakeOpenAIKey)
	_, err := validateOpenAIKey(context.Background(), srv.Client(), srv.URL, []byte("sk-wrong"))
	require.ErrorIs(t, err, errKeyRefused)
	assert.Contains(t, err.Error(), fakeOpenAIRefusal)
}

func TestValidateOpenAICodexTokenHitsTheCodexRoot(t *testing.T) {
	srv := fakeOpenAI(t, "access-1")
	models, err := validateOpenAICodexToken(context.Background(), srv.Client(), srv.URL, []byte("access-1"))
	require.NoError(t, err)
	assert.Equal(t, []string{"gpt-5.4", "gpt-5.3-codex"}, models)
}

func TestUnusualOpenAIFormat(t *testing.T) {
	assert.Empty(t, unusualOpenAIFormat([]byte("sk-abc")))
	assert.NotEmpty(t, unusualOpenAIFormat([]byte("xai-abc")))
}

func TestChatGPTAccountID(t *testing.T) {
	assert.Empty(t, chatgptAccountID("not-a-jwt"))
	payload, _ := json.Marshal(map[string]any{
		"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "acct-1"},
	})
	jwt := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`)) + "." +
		base64.RawURLEncoding.EncodeToString(payload) + ".sig"
	assert.Equal(t, "acct-1", chatgptAccountID(jwt))
}
