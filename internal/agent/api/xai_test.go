package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeXAIKey is an obvious fake. There is no real xAI key anywhere in this repository.
const fakeXAIKey = "xai-not-a-real-key-0000wxyz"

// fakeXAIRefusal is the sentence the fake xAI sends with its 401.
const fakeXAIRefusal = "Incorrect API key provided."

// fakeXAI answers GET /v1/models the way xAI does: a bearer token, an OpenAI-shaped model
// list, and an OpenAI-shaped error envelope. It takes more than one good credential because
// an API key and a subscription access token are both bearers for the same endpoint.
func fakeXAI(t *testing.T, good ...string) *httptest.Server {
	t.Helper()
	accepted := map[string]bool{}
	for _, g := range good {
		accepted["Bearer "+g] = true
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/models", r.URL.Path)
		// The credential is a bearer, not an x-api-key: that difference is the whole reason
		// the runtime sets ANTHROPIC_AUTH_TOKEN rather than ANTHROPIC_API_KEY for Grok.
		if !accepted[r.Header.Get("Authorization")] {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"message":` + strconv.Quote(fakeXAIRefusal) +
				`,"type":"invalid_request_error"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"grok-4.6"},{"id":"grok-4.5"}]}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestValidateXAIKeyAcceptsAGoodKey(t *testing.T) {
	srv := fakeXAI(t, fakeXAIKey)
	models, err := validateXAIKey(context.Background(), srv.Client(), srv.URL, []byte(fakeXAIKey))
	require.NoError(t, err)
	assert.Equal(t, []string{"grok-4.6", "grok-4.5"}, models)
}

func TestValidateXAIKeyPassesTheProvidersOwnWordsOn(t *testing.T) {
	srv := fakeXAI(t, fakeXAIKey)
	_, err := validateXAIKey(context.Background(), srv.Client(), srv.URL, []byte("xai-wrong"))
	require.ErrorIs(t, err, errKeyRefused)
	assert.Contains(t, err.Error(), fakeXAIRefusal)
}

// The same split the Anthropic path makes, and for the same reason: only one of these two
// answers is worth trying again, and an operator has to be told which.
func TestValidateXAIKeyMapsEveryStatus(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		want   error
		says   string
	}{
		{"400", http.StatusBadRequest, `{"error":{"message":"bad"}}`, errKeyRefused, "bad"},
		{"401", http.StatusUnauthorized, `{"error":{"message":"nope"}}`, errKeyRefused, "nope"},
		{"403", http.StatusForbidden, `{"code":"forbidden"}`, errKeyRefused, "forbidden"},
		// xAI is OpenAI-shaped, and OpenAI-shaped APIs disagree about whether `error` is an
		// object or a string. Both arrive in the wild, so both are read.
		{"403 with a string error", http.StatusForbidden, `{"error":"no access"}`, errKeyRefused, "no access"},
		{"429", http.StatusTooManyRequests, `{"error":{"message":"slow down"}}`, errCannotValidate, "slow down"},
		{"500", http.StatusInternalServerError, `{"error":{"message":"boom"}}`, errCannotValidate, "boom"},
		{"502 with no body at all", http.StatusBadGateway, ``, errCannotValidate, "502"},
		{"200 that is not a model list", http.StatusOK, `<html>a proxy login page</html>`, errCannotValidate, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			_, err := validateXAIKey(context.Background(), srv.Client(), srv.URL, []byte(fakeXAIKey))
			require.ErrorIs(t, err, tc.want)
			if tc.says != "" {
				assert.Contains(t, err.Error(), tc.says)
			}
		})
	}
}

func TestValidateXAIKeyCapsTheModelsItReports(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		body := `{"data":[`
		for i := range maxModelsReported + 10 {
			if i > 0 {
				body += ","
			}
			body += `{"id":"grok-` + strconv.Itoa(i) + `"}`
		}
		_, _ = w.Write([]byte(body + `]}`))
	}))
	defer srv.Close()
	models, err := validateXAIKey(context.Background(), srv.Client(), srv.URL, []byte(fakeXAIKey))
	require.NoError(t, err)
	assert.Len(t, models, maxModelsReported)
}

// A subscription access token is not xai-prefixed and must still save without complaint;
// the note exists for a key that looks wrong, not for one that legitimately looks different.
func TestUnusualXAIFormat(t *testing.T) {
	assert.Empty(t, unusualXAIFormat([]byte("xai-abc")))
	assert.NotEmpty(t, unusualXAIFormat([]byte("sk-abc")))
}

// Whatever the provider says about a refusal, the credential is not in it — it was only ever
// a request header — and operatorDetail scrubs anything key-shaped in case one is echoed.
func TestAnEchoedCredentialNeverReachesAnOperator(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"key ` + fakeXAIKey + ` is not valid"}}`))
	}))
	defer srv.Close()
	_, err := validateXAIKey(context.Background(), srv.Client(), srv.URL, []byte(fakeXAIKey))
	require.ErrorIs(t, err, errKeyRefused)

	said := operatorDetail(validationDetail(err), []byte(fakeXAIKey))
	assert.NotContains(t, said, fakeXAIKey)
	assert.Contains(t, said, "[redacted]")
}
