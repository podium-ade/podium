package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/agent/config"
)

func shaped(t *testing.T, in string, row memoryLLMRow) map[string]any {
	t.Helper()
	var body map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(in), &body))
	require.NoError(t, shapeMemoryRequest(body, row))
	raw, err := json.Marshal(body)
	require.NoError(t, err)
	var out map[string]any
	require.NoError(t, json.Unmarshal(raw, &out))
	return out
}

// The memory service is told a placeholder model, so whatever it sends, the chosen one goes.
func TestShapeMemoryRequestReplacesTheModelAndDropsAttribution(t *testing.T) {
	got := shaped(t, `{"model":"podium","user":"podium","stream":true,"max_tokens":100,"temperature":0.1,
		"messages":[{"role":"user","content":"hi"}],"response_format":{"type":"json_object"}}`,
		memoryLLMRow{Provider: ProviderAnthropic, Model: "claude-haiku-5-5"})

	assert.Equal(t, "claude-haiku-5-5", got["model"])
	assert.NotContains(t, got, "user")
	assert.NotContains(t, got, "stream")
	assert.EqualValues(t, 100, got["max_tokens"], "only OpenAI renames max_tokens")
	assert.EqualValues(t, 0.1, got["temperature"])
	assert.Equal(t, map[string]any{"type": "json_object"}, got["response_format"])
}

// OpenAI's newer models refuse max_tokens, and its reasoning models refuse temperature.
func TestShapeMemoryRequestFitsOpenAIsParameters(t *testing.T) {
	in := `{"model":"podium","max_tokens":100,"temperature":0.1,"messages":[]}`

	got := shaped(t, in, memoryLLMRow{Provider: ProviderOpenAI, Model: "gpt-5-mini"})
	assert.NotContains(t, got, "max_tokens")
	assert.EqualValues(t, 100, got["max_completion_tokens"])
	assert.NotContains(t, got, "temperature", "a reasoning model refuses temperature")

	got = shaped(t, in, memoryLLMRow{Provider: ProviderOpenAI, Model: "gpt-4.1-mini"})
	assert.EqualValues(t, 100, got["max_completion_tokens"])
	assert.EqualValues(t, 0.1, got["temperature"])
}

func TestMemoryLLMEndpointIsEachProvidersChatCompletions(t *testing.T) {
	s := &AgentService{
		baseURL: "https://a.example/", openaiBaseURL: "https://o.example",
		xaiBaseURL: "https://x.example", googleBaseURL: "https://g.example",
	}
	for provider, want := range map[string]string{
		ProviderAnthropic: "https://a.example/v1/chat/completions",
		ProviderOpenAI:    "https://o.example/v1/chat/completions",
		ProviderXAI:       "https://x.example/v1/chat/completions",
		ProviderGoogle:    "https://g.example/v1beta/openai/chat/completions",
	} {
		got, err := s.memoryLLMEndpoint(provider)
		require.NoError(t, err)
		assert.Equal(t, want, got, provider)
	}
	_, err := s.memoryLLMEndpoint("nope")
	assert.Error(t, err)
}

// The route spends the organisation's model key, so nothing reaches the store or a provider
// without the memory service's own token — not the operator token, not the memory API key
// every turn holds, and not an empty one on a conductor that never set it.
func TestMemoryLLMHandlerRefusesAnythingButItsToken(t *testing.T) {
	cases := map[string]struct {
		token, header string
	}{
		"no bearer":          {token: "llm-token", header: ""},
		"wrong bearer":       {token: "llm-token", header: "Bearer memtoken"},
		"not a bearer":       {token: "llm-token", header: "llm-token"},
		"route not set up":   {token: "", header: "Bearer "},
		"empty against none": {token: "", header: ""},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			s := NewAgentService(AgentServiceOptions{
				Env: config.Config{MemoryLLMToken: c.token}, Logger: quietLogger(),
			})
			req := httptest.NewRequest(http.MethodPost, "/memory-llm/v1/chat/completions",
				strings.NewReader(`{}`))
			if c.header != "" {
				req.Header.Set("Authorization", c.header)
			}
			rec := httptest.NewRecorder()
			s.MemoryLLMHandler().ServeHTTP(rec, req)
			assert.Equal(t, http.StatusUnauthorized, rec.Code)
			assert.Contains(t, rec.Body.String(), `"message"`, "an OpenAI client reads error.message")
		})
	}
}

func TestMemoryLLMHandlerServesOnlyChatCompletions(t *testing.T) {
	s := NewAgentService(AgentServiceOptions{
		Env: config.Config{MemoryLLMToken: "llm-token"}, Logger: quietLogger(),
	})
	for _, path := range []string{"/memory-llm/v1/models", "/memory-llm/v1/embeddings", "/memory-llm/"} {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`))
		req.Header.Set("Authorization", "Bearer llm-token")
		rec := httptest.NewRecorder()
		s.MemoryLLMHandler().ServeHTTP(rec, req)
		assert.Equal(t, http.StatusNotFound, rec.Code, path)
	}
	req := httptest.NewRequest(http.MethodGet, "/memory-llm/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer llm-token")
	rec := httptest.NewRecorder()
	s.MemoryLLMHandler().ServeHTTP(rec, req)
	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
}
