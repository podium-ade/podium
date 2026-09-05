package api

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentv1 "github.com/alvaroibarguen/podium/internal/proto/podium/agent/v1"
)

// fakeKey is an obvious fake. There is no real Anthropic key anywhere in this repository.
const fakeKey = "sk-ant-not-a-real-key-0000abcd"

// fakeRefusal is the sentence the fake Anthropic sends with its 400. Tests assert this exact
// string reaches the operator: the point of the detail is that it is the provider's own
// words, not a paraphrase of them.
const fakeRefusal = "API key is invalid."

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
				`{"type":"authentication_error","message":` + strconv.Quote(fakeRefusal) + `}}`))
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
	assert.Contains(t, err.Error(), fakeRefusal, "the provider's own words must survive")
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
	// The provider's own words ride along as a detail, so the operator is told why and not
	// only that. The code and the message are untouched by it.
	assert.Equal(t, fakeRefusal, providerKeyDetail(t, err))
	assert.Empty(t, secrets.set, "a refused key must not reach the secret store")
	assert.NotContains(t, buf.String(), "sk-ant-a-key-nobody-knows")
}

// The bug this fixes. A key that only needs a header Podium does not send is not a dead key,
// and "Anthropic rejected this key" on its own sent an operator looking for a new one for an
// hour. The provider says exactly what is wrong; the operator has to be able to read it.
func TestSetProviderKeyPassesTheProvidersOwnExplanationOn(t *testing.T) {
	const said = "anthropic-workspace-id is required when authenticating with an " +
		"identity-linked API key; send the id of the workspace this request acts in."
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"invalid_request_error",` +
			`"message":` + strconv.Quote(said) + `}}`))
	}))
	defer srv.Close()
	secrets := newFakeSecrets()
	var buf bytes.Buffer
	svc := NewAgentService(AgentServiceOptions{
		Secrets:          secrets,
		AnthropicBaseURL: srv.URL,
		HTTPClient:       srv.Client(),
		Logger:           slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})),
	})

	_, err := svc.SetProviderKey(context.Background(),
		connect.NewRequest(&agentv1.SetProviderKeyRequest{Key: fakeKey}))
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	assert.Equal(t, said, providerKeyDetail(t, err))
	assert.Empty(t, secrets.set)
	assert.NotContains(t, buf.String(), fakeKey)
}

// The unreachable path carries a detail too: "could not validate" says nothing about whether
// the operator has a proxy in the way or a provider having a bad day.
func TestSetProviderKeyExplainsAProviderItCouldNotReach(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"overloaded_error",` +
			`"message":"Anthropic is temporarily overloaded."}}`))
	}))
	defer srv.Close()
	svc := NewAgentService(AgentServiceOptions{
		Secrets:          newFakeSecrets(),
		AnthropicBaseURL: srv.URL,
		HTTPClient:       srv.Client(),
		Logger:           quietLogger(),
	})

	_, err := svc.SetProviderKey(context.Background(),
		connect.NewRequest(&agentv1.SetProviderKeyRequest{Key: fakeKey}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))
	assert.Equal(t, "could not validate the key with Anthropic; nothing was saved", errMessage(err))
	detail := providerKeyDetail(t, err)
	assert.Contains(t, detail, "503 Service Unavailable")
	assert.Contains(t, detail, "Anthropic is temporarily overloaded.")
	assert.NotContains(t, detail, fakeKey)
}

// A provider that cannot be dialled at all still explains itself, and still says nothing
// about the key.
func TestSetProviderKeyExplainsADialFailure(t *testing.T) {
	srv := fakeAnthropic(t, fakeKey)
	srv.Close()
	svc := NewAgentService(AgentServiceOptions{
		Secrets:          newFakeSecrets(),
		AnthropicBaseURL: srv.URL,
		HTTPClient:       srv.Client(),
		Logger:           quietLogger(),
	})

	_, err := svc.SetProviderKey(context.Background(),
		connect.NewRequest(&agentv1.SetProviderKeyRequest{Key: fakeKey}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))
	detail := providerKeyDetail(t, err)
	assert.Contains(t, detail, "connection refused")
	assert.NotContains(t, detail, fakeKey)
}

// The same, through the whole handler: a provider that echoes the key back into its own
// error message puts it neither in the response nor in the log.
func TestSetProviderKeyScrubsAKeyTheProviderEchoedBack(t *testing.T) {
	const echoed = "sk-ant-not-a-real-key-echoed"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"authentication_error","message":` +
			strconv.Quote("the key "+r.Header.Get("x-api-key")+" is not valid") + `}}`))
	}))
	defer srv.Close()
	var buf bytes.Buffer
	svc := NewAgentService(AgentServiceOptions{
		Secrets:          newFakeSecrets(),
		AnthropicBaseURL: srv.URL,
		HTTPClient:       srv.Client(),
		Logger:           slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})),
	})

	_, err := svc.SetProviderKey(context.Background(),
		connect.NewRequest(&agentv1.SetProviderKeyRequest{Key: echoed}))
	require.Error(t, err)
	assert.Equal(t, "the key [redacted] is not valid", providerKeyDetail(t, err))
	assert.NotContains(t, buf.String(), echoed, "nor may it reach the log")
}

// A provider that echoes the key back does not get to put it on a page. Anthropic does not
// do this; the point is that it would not matter if one did.
func TestOperatorDetailScrubsAnythingKeyShaped(t *testing.T) {
	assert.Equal(t, "[redacted] is not valid",
		operatorDetail(fakeKey+" is not valid", []byte(fakeKey)))
	// Truncated by the provider's own formatting, so the exact-key match cannot fire.
	assert.Equal(t, "the key [redacted]… is not valid",
		operatorDetail("the key sk-ant-not-a-re… is not valid", []byte(fakeKey)))
	// A key with an unfamiliar prefix is still the key, and is still matched exactly.
	assert.Equal(t, "[redacted] is not valid",
		operatorDetail("xyz-123 is not valid", []byte("xyz-123")))
}

func TestOperatorDetailBoundsAndFlattensWhatTheProviderSaid(t *testing.T) {
	assert.Equal(t, "one line now", operatorDetail("one\nline\tnow", nil))
	assert.Equal(t, "no bells here", operatorDetail("no \x07bells\x00 here", nil))
	assert.Empty(t, operatorDetail("", nil))

	long := operatorDetail(strings.Repeat("a", 10_000), nil)
	assert.Equal(t, providerDetailLimit+1, len([]rune(long)), "bounded, plus the ellipsis")
	assert.True(t, strings.HasSuffix(long, "…"))

	// Bounding is by rune, so a multi-byte sentence is not cut in half.
	runes := operatorDetail(strings.Repeat("é", 10_000), nil)
	assert.True(t, utf8.ValidString(runes))
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

// providerKeyDetail is the ProviderKeyError a failed SetProviderKey carries, or "" when it
// carries none.
func providerKeyDetail(t *testing.T, err error) string {
	t.Helper()
	var cerr *connect.Error
	if !errors.As(err, &cerr) {
		return ""
	}
	for _, d := range cerr.Details() {
		msg, verr := d.Value()
		require.NoError(t, verr)
		if pk, ok := msg.(*agentv1.ProviderKeyError); ok {
			return pk.GetProviderMessage()
		}
	}
	return ""
}

// errMessage is the message without Connect's "code: " prefix.
func errMessage(err error) string {
	var cerr *connect.Error
	if errors.As(err, &cerr) {
		return cerr.Message()
	}
	return strings.TrimSpace(err.Error())
}
