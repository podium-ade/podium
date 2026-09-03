package api

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentv1 "github.com/alvaroibarguen/podium/internal/proto/podium/agent/v1"
)

// fakeKey is an obvious fake. There is no real Anthropic key anywhere in this repository.
const fakeKey = "sk-ant-not-a-real-key-0000abcd"

// fakeAnthropic answers GET /v1/models the way the live API does, which is the part of this
// step that had to be checked against the real thing: a key it does not know gets **400**,
// not 401. Anything else about the request — a wrong path, a missing version header — is a
// bug in the caller and answered as such.
func fakeAnthropic(t *testing.T, good string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/models", r.URL.Path)
		assert.Equal(t, anthropicVersion, r.Header.Get("anthropic-version"))
		if r.Header.Get("x-api-key") != good {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"type":"error","error":` +
				`{"type":"authentication_error","message":"API key is invalid."}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"claude-opus-5"},{"id":"claude-sonnet-5"}],` +
			`"first_id":"claude-opus-5","last_id":"claude-sonnet-5","has_more":false}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// fakeSecrets is a control plane's secret store: what is in it, at which version, and what
// reached it. A test asserts on all three, and on nothing having reached it at all.
type fakeSecrets struct {
	mu       sync.Mutex
	set      map[string][]byte
	versions map[string]int32
	deleted  []string
	setErr   error
	delErr   error
	listErr  error
}

func newFakeSecrets() *fakeSecrets {
	return &fakeSecrets{set: map[string][]byte{}, versions: map[string]int32{}}
}

func (f *fakeSecrets) SetSecret(_ context.Context, name string, value []byte) (int32, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.setErr != nil {
		return 0, f.setErr
	}
	// A copy: the caller zeroes its buffer, which is the behaviour being relied on.
	f.set[name] = append([]byte(nil), value...)
	f.versions[name]++
	return f.versions[name], nil
}

func (f *fakeSecrets) DeleteSecret(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.delErr != nil {
		return f.delErr
	}
	delete(f.set, name)
	delete(f.versions, name)
	f.deleted = append(f.deleted, name)
	return nil
}

func (f *fakeSecrets) SecretVersion(_ context.Context, name string) (int32, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listErr != nil {
		return 0, f.listErr
	}
	return f.versions[name], nil
}

func TestValidateAnthropicKeyAcceptsAGoodKey(t *testing.T) {
	srv := fakeAnthropic(t, fakeKey)
	models, err := validateAnthropicKey(context.Background(), srv.Client(), srv.URL, []byte(fakeKey))
	require.NoError(t, err)
	assert.Equal(t, []string{"claude-opus-5", "claude-sonnet-5"}, models)
}

// The whole reason this test exists: the live API answers an invalid key with 400. Code that
// only looks for 401 would report "could not validate" and tell an operator to retry a key
// that will never work.
func TestValidateAnthropicKeyTreatsA400AuthenticationErrorAsRefusal(t *testing.T) {
	srv := fakeAnthropic(t, fakeKey)
	_, err := validateAnthropicKey(context.Background(), srv.Client(), srv.URL, []byte("sk-ant-wrong"))
	require.ErrorIs(t, err, errKeyRefused)
	assert.Contains(t, err.Error(), "API key is invalid.", "the provider's own words must survive")
}

func TestValidateAnthropicKeyMapsEveryStatus(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{"401", http.StatusUnauthorized, `{"error":{"type":"authentication_error","message":"nope"}}`, errKeyRefused},
		{"403", http.StatusForbidden, `{"error":{"type":"permission_error","message":"nope"}}`, errKeyRefused},
		{"429", http.StatusTooManyRequests, `{"error":{"type":"rate_limit_error","message":"slow down"}}`, errCannotValidate},
		{"500", http.StatusInternalServerError, `{"error":{"type":"api_error","message":"boom"}}`, errCannotValidate},
		{"503", http.StatusServiceUnavailable, ``, errCannotValidate},
		{"200 that is not a model list", http.StatusOK, `<html>a proxy login page</html>`, errCannotValidate},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			_, err := validateAnthropicKey(context.Background(), srv.Client(), srv.URL, []byte(fakeKey))
			require.ErrorIs(t, err, tc.want)
		})
	}
}

func TestValidateAnthropicKeyTreatsAnUnreachableProviderAsUnknown(t *testing.T) {
	srv := fakeAnthropic(t, fakeKey)
	srv.Close()
	_, err := validateAnthropicKey(context.Background(), srv.Client(), srv.URL, []byte(fakeKey))
	require.ErrorIs(t, err, errCannotValidate)
	assert.NotContains(t, err.Error(), fakeKey, "the key must not reach an error string")
}

func TestTrimPastedKey(t *testing.T) {
	tests := map[string]string{
		fakeKey:                   fakeKey,
		"  " + fakeKey + "\n":     fakeKey,
		`"` + fakeKey + `"`:       fakeKey,
		"'" + fakeKey + "'":       fakeKey,
		` "  ` + fakeKey + `  " `: fakeKey,
		"":                        "",
		`""`:                      "",
		"ANTHROPIC_API_KEY":       "ANTHROPIC_API_KEY",
	}
	for in, want := range tests {
		assert.Equal(t, want, trimPastedKey(in), "input %q", in)
	}
}

func TestKeyHintIsTheLastFour(t *testing.T) {
	assert.Equal(t, "abcd", keyHint([]byte("sk-ant-0000abcd")))
	assert.Equal(t, "ab", keyHint([]byte("ab")))
	assert.Equal(t, "", keyHint(nil))
}

func TestUnusualFormatOnlyFiresOnAnUnfamiliarPrefix(t *testing.T) {
	assert.Empty(t, unusualFormat([]byte(fakeKey)))
	assert.Contains(t, unusualFormat([]byte("xyz-123")), "looks unusual")
}

func TestRedactedKeyRequestPrintsNoKey(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	logger.Debug("x", "request", redactedKeyRequest{&agentv1.SetProviderKeyRequest{
		Provider: ProviderAnthropic, Key: fakeKey,
	}})
	out := buf.String()
	assert.NotContains(t, out, fakeKey)
	assert.Contains(t, out, "[redacted]")
	assert.Contains(t, out, "key_len=30")
}

func TestZeroScrubs(t *testing.T) {
	b := []byte(fakeKey)
	zero(b)
	assert.Equal(t, make([]byte, len(fakeKey)), b)
}

// The refused path never reaches the store, so this test needs no Postgres: it is the one
// that proves an unvalidated key is not written anywhere at all. A nil store is deliberate —
// if a future change starts writing before validation, this panics rather than passing.
func TestSetProviderKeyRefusedWritesNothingAndLogsNoKey(t *testing.T) {
	srv := fakeAnthropic(t, fakeKey)
	secrets := newFakeSecrets()
	var buf bytes.Buffer
	svc := NewAgentService(AgentServiceOptions{
		Secrets:          secrets,
		AnthropicBaseURL: srv.URL,
		HTTPClient:       srv.Client(),
		Logger:           slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})),
	})

	_, err := svc.SetProviderKey(context.Background(),
		connect.NewRequest(&agentv1.SetProviderKeyRequest{Key: "sk-ant-a-key-nobody-knows"}))
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	assert.Equal(t, "Anthropic rejected this key", errMessage(err))
	assert.Empty(t, secrets.set, "a refused key must not reach the secret store")
	assert.NotContains(t, buf.String(), "sk-ant-a-key-nobody-knows")
}

func TestSetProviderKeyRejectsAnotherProviderAndAnEmptyKey(t *testing.T) {
	svc := NewAgentService(AgentServiceOptions{Secrets: newFakeSecrets(), Logger: quietLogger()})

	_, err := svc.SetProviderKey(context.Background(),
		connect.NewRequest(&agentv1.SetProviderKeyRequest{Provider: "openai", Key: fakeKey}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	_, err = svc.SetProviderKey(context.Background(),
		connect.NewRequest(&agentv1.SetProviderKeyRequest{Key: "   \" \"  "}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	_, err = svc.ClearProviderKey(context.Background(),
		connect.NewRequest(&agentv1.ClearProviderKeyRequest{Provider: "openai"}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestSetProviderKeyWithNoPodiumClientSaysSo(t *testing.T) {
	svc := NewAgentService(AgentServiceOptions{Logger: quietLogger()})
	_, err := svc.SetProviderKey(context.Background(),
		connect.NewRequest(&agentv1.SetProviderKeyRequest{Key: fakeKey}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
}

// errMessage is the message without Connect's "code: " prefix.
func errMessage(err error) string {
	var cerr *connect.Error
	if errors.As(err, &cerr) {
		return cerr.Message()
	}
	return strings.TrimSpace(err.Error())
}
