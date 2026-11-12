//go:build integration

package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/agent/config"
	"github.com/podium-ade/podium/internal/agent/profiles"
	agentv1 "github.com/podium-ade/podium/internal/proto/podium/agent/v1"
)

// fakeCompletions is a provider's chat completions endpoint. It records what reached it and
// answers 404 for a model it does not know, the way the real ones do.
type fakeCompletions struct {
	mu    sync.Mutex
	paths []string
	auth  []string
	body  []map[string]any
}

func (f *fakeCompletions) server(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		f.mu.Lock()
		f.paths = append(f.paths, r.URL.Path)
		f.auth = append(f.auth, r.Header.Get("Authorization"))
		f.body = append(f.body, body)
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if body["model"] == "no-such-model" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"message":"model no-such-model does not exist","type":"not_found_error"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"c1","object":"chat.completion","choices":[{"index":0,` +
			`"message":{"role":"assistant","content":"{\"facts\":[]}"},"finish_reason":"stop"}]}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (f *fakeCompletions) last() (string, string, map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := len(f.paths) - 1
	return f.paths[n], f.auth[n], f.body[n]
}

func (f *fakeCompletions) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.paths)
}

func memoryLLMFixture(t *testing.T) (*AgentService, *fakeSecrets, *fakeCompletions) {
	t.Helper()
	up := &fakeCompletions{}
	srv := up.server(t)
	secrets := newFakeSecrets()
	svc := NewAgentService(AgentServiceOptions{
		Store:            newStore(t),
		Secrets:          secrets,
		Memory:           &fakeMemory{},
		Env:              config.Config{MemoryLLMToken: "llm-token"},
		AnthropicBaseURL: srv.URL,
		OpenAIBaseURL:    srv.URL,
		HTTPClient:       srv.Client(),
		Logger:           quietLogger(),
	})
	return svc, secrets, up
}

func extract(t *testing.T, svc *AgentService, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/memory-llm/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer llm-token")
	rec := httptest.NewRecorder()
	svc.MemoryLLMHandler().ServeHTTP(rec, req)
	return rec
}

func TestMemorySettingsChooseAKeyAndExtractionSpendsIt(t *testing.T) {
	svc, secrets, up := memoryLLMFixture(t)
	ctx := adminCtx("alice")

	// Nothing chosen and no key saved: every provider says why, and extraction waits.
	got, err := svc.GetMemorySettings(ctx, connect.NewRequest(&agentv1.GetMemorySettingsRequest{}))
	require.NoError(t, err)
	assert.True(t, got.Msg.GetSettings().GetProxyEnabled())
	assert.Empty(t, got.Msg.GetSettings().GetProvider())
	for _, p := range got.Msg.GetSettings().GetProviders() {
		assert.False(t, p.GetAvailable(), p.GetProvider())
		assert.NotEmpty(t, p.GetReason(), p.GetProvider())
	}
	rec := extract(t, svc, `{"model":"podium","messages":[]}`)
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code, "retryable, so a retain is not lost")
	assert.Contains(t, rec.Body.String(), "Memory page")

	_, err = svc.SetMemorySettings(ctx, connect.NewRequest(&agentv1.SetMemorySettingsRequest{
		Provider: ProviderAnthropic, Model: "claude-haiku-5-5",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err), "no Anthropic key is saved")
	assert.Zero(t, up.calls())

	_, err = secrets.SetSecret(context.Background(), profiles.AnthropicKeySecret, []byte("sk-ant-the-org-key"))
	require.NoError(t, err)

	// A model the provider does not know is refused by the probe, in the provider's words,
	// and is not saved.
	_, err = svc.SetMemorySettings(ctx, connect.NewRequest(&agentv1.SetMemorySettingsRequest{
		Provider: ProviderAnthropic, Model: "no-such-model",
	}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "model no-such-model does not exist")
	row, ok, err := svc.memoryLLMRow(context.Background())
	require.NoError(t, err)
	assert.False(t, ok, "a refused model is not saved: %+v", row)

	res, err := svc.SetMemorySettings(ctx, connect.NewRequest(&agentv1.SetMemorySettingsRequest{
		Provider: ProviderAnthropic, Model: " claude-haiku-5-5 ",
	}))
	require.NoError(t, err)
	settings := res.Msg.GetSettings()
	assert.Equal(t, ProviderAnthropic, settings.GetProvider())
	assert.Equal(t, "claude-haiku-5-5", settings.GetModel())
	assert.Equal(t, "alice", settings.GetSetBy())
	path, auth, _ := up.last()
	assert.Equal(t, "/v1/chat/completions", path)
	assert.Equal(t, "Bearer sk-ant-the-org-key", auth)

	// Extraction now goes to Anthropic, with the saved key and the chosen model.
	rec = extract(t, svc, `{"model":"podium","user":"podium","max_tokens":2000,`+
		`"messages":[{"role":"user","content":"extract"}],"response_format":{"type":"json_object"}}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"chat.completion"`)
	path, auth, body := up.last()
	assert.Equal(t, "/v1/chat/completions", path)
	assert.Equal(t, "Bearer sk-ant-the-org-key", auth)
	assert.Equal(t, "claude-haiku-5-5", body["model"])
	assert.NotContains(t, body, "user")
	assert.EqualValues(t, 2000, body["max_tokens"])

	// The upstream's refusal is passed back as-is, so the memory service records the
	// provider's own words on the failed operation.
	_, err = secrets.SetSecret(context.Background(), profiles.OpenAIKeySecret, []byte("sk-openai"))
	require.NoError(t, err)
	_, err = svc.SetMemorySettings(ctx, connect.NewRequest(&agentv1.SetMemorySettingsRequest{
		Provider: ProviderOpenAI, Model: "gpt-5-mini",
	}))
	require.NoError(t, err)
	rec = extract(t, svc, `{"model":"podium","max_tokens":2000,"temperature":0.1,"messages":[]}`)
	require.Equal(t, http.StatusOK, rec.Code)
	_, auth, body = up.last()
	assert.Equal(t, "Bearer sk-openai", auth)
	assert.Equal(t, "gpt-5-mini", body["model"])
	assert.EqualValues(t, 2000, body["max_completion_tokens"])
	assert.NotContains(t, body, "temperature")
}

func TestOnlyAnAdminChoosesTheMemoryModel(t *testing.T) {
	svc, secrets, up := memoryLLMFixture(t)
	_, err := secrets.SetSecret(context.Background(), profiles.AnthropicKeySecret, []byte("sk-ant-x"))
	require.NoError(t, err)

	_, err = svc.SetMemorySettings(loginCtx("bob"), connect.NewRequest(&agentv1.SetMemorySettingsRequest{
		Provider: ProviderAnthropic, Model: "claude-haiku-5-5",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	assert.Zero(t, up.calls())
}

func TestMemorySettingsWithoutTheRouteSayWhy(t *testing.T) {
	svc, secrets, _ := memoryLLMFixture(t)
	svc.env.MemoryLLMToken = ""
	_, err := secrets.SetSecret(context.Background(), profiles.AnthropicKeySecret, []byte("sk-ant-x"))
	require.NoError(t, err)

	got, err := svc.GetMemorySettings(adminCtx("alice"), connect.NewRequest(&agentv1.GetMemorySettingsRequest{}))
	require.NoError(t, err)
	assert.False(t, got.Msg.GetSettings().GetProxyEnabled())

	_, err = svc.SetMemorySettings(adminCtx("alice"), connect.NewRequest(&agentv1.SetMemorySettingsRequest{
		Provider: ProviderAnthropic, Model: "claude-haiku-5-5",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "PODIUM_AGENT_MEMORY_LLM_TOKEN")
}
