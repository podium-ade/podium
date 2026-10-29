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

const fakeGoogleKey = "AIzaNotARealKey0000wxyz"
const fakeGoogleRefusal = "API key not valid. Please pass a valid API key."

func fakeGoogle(t *testing.T, good ...string) *httptest.Server {
	t.Helper()
	accepted := map[string]bool{}
	for _, g := range good {
		accepted[g] = true
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1beta/models" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if r.URL.Query().Has("key") {
			t.Errorf("the key was sent in the URL")
		}
		w.Header().Set("Content-Type", "application/json")
		if !accepted[r.Header.Get("x-goog-api-key")] {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"code":400,"message":` + strconv.Quote(fakeGoogleRefusal) +
				`,"status":"INVALID_ARGUMENT"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"models":[{"name":"models/gemini-3.8-flash"},{"name":"models/gemini-3.1-pro-preview"}]}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestValidateGoogleKeyAcceptsAGoodKeyAndStripsThePrefix(t *testing.T) {
	srv := fakeGoogle(t, fakeGoogleKey)
	models, err := validateGoogleKey(context.Background(), srv.Client(), srv.URL, []byte(fakeGoogleKey))
	require.NoError(t, err)
	assert.Equal(t, []string{"gemini-3.8-flash", "gemini-3.1-pro-preview"}, models)
}

// Google answers 400, not 401, for a key it does not know — the same trap as Anthropic.
func TestValidateGoogleKeyTreatsA400AsRefusedAndPassesTheWordsOn(t *testing.T) {
	srv := fakeGoogle(t, fakeGoogleKey)
	_, err := validateGoogleKey(context.Background(), srv.Client(), srv.URL, []byte("AIzaWrong"))
	require.ErrorIs(t, err, errKeyRefused)
	assert.Contains(t, err.Error(), fakeGoogleRefusal)
}

func TestValidateGoogleKeyCannotValidateOnAServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)
	_, err := validateGoogleKey(context.Background(), srv.Client(), srv.URL, []byte(fakeGoogleKey))
	require.ErrorIs(t, err, errCannotValidate)
	assert.NotErrorIs(t, err, errKeyRefused)
}

func TestUnusualGoogleFormat(t *testing.T) {
	assert.Empty(t, unusualGoogleFormat([]byte(fakeGoogleKey)))
	assert.NotEmpty(t, unusualGoogleFormat([]byte("sk-abc")))
}

func TestOperatorDetailScrubsAGoogleKey(t *testing.T) {
	got := operatorDetail("bad key AIzaSyD-somethingLong_123 here", nil)
	assert.NotContains(t, got, "AIzaSyD")
	assert.Contains(t, got, "[redacted]")
}
