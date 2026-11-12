package api

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/podium-ade/podium/internal/agent/store"
	agentv1 "github.com/podium-ade/podium/internal/proto/podium/agent/v1"
)

// The memory service extracts facts with an LLM, and reads which LLM, and with which key, only
// when it starts. Podium instead points it here once — provider "openai", base URL this
// conductor's MemoryLLMPath, API key PODIUM_AGENT_MEMORY_LLM_TOKEN — and forwards each call
// to whichever provider an admin chose on the Memory page, with that provider's saved global
// key and the chosen model. Every provider Podium holds a key for answers the OpenAI chat
// completions shape, so forwarding is a model swap and a couple of parameter renames, not a
// translation.

// MemoryLLMPath is where the memory service's OpenAI client is pointed: its base URL is this
// path plus "v1", and it posts to v1/chat/completions under it.
const MemoryLLMPath = "/memory-llm/"

// memoryLLMSettingKey is the settings row the choice lives in. It holds no credential.
const memoryLLMSettingKey = "memory.llm"

// memoryLLMTimeout bounds one forwarded extraction. It is long on purpose: extraction of a
// large chunk is a long completion, and the memory service has its own timeout and retries.
const memoryLLMTimeout = 5 * time.Minute

// memoryLLMBodyLimit is the most a forwarded request may carry. A retain chunk is a few
// thousand tokens; this is generous and still bounded.
const memoryLLMBodyLimit = 16 << 20

// memoryProbeTimeout bounds the test completion SetMemorySettings makes before saving.
const memoryProbeTimeout = 60 * time.Second

// openAIReasoningModel is OpenAI's models that refuse temperature and want
// max_completion_tokens: o1, o3, o4-mini, gpt-5 and its variants.
var openAIReasoningModel = regexp.MustCompile(`^(o\d|gpt-5)`)

// memoryLLMRow is the jsonb of the settings row.
type memoryLLMRow struct {
	Provider string    `json:"provider"`
	Model    string    `json:"model"`
	SetBy    string    `json:"set_by"`
	SetAt    time.Time `json:"set_at"`
}

// GetMemorySettings reports the extraction model and the keys it could spend instead.
func (s *AgentService) GetMemorySettings(
	ctx context.Context, _ *connect.Request[agentv1.GetMemorySettingsRequest],
) (*connect.Response[agentv1.GetMemorySettingsResponse], error) {
	if s.memory == nil {
		return nil, notConfigured()
	}
	settings, err := s.memorySettings(ctx)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&agentv1.GetMemorySettingsResponse{Settings: settings}), nil
}

// SetMemorySettings saves a provider and model for extraction, after one small completion
// through exactly the path extraction will take has answered.
func (s *AgentService) SetMemorySettings(
	ctx context.Context, req *connect.Request[agentv1.SetMemorySettingsRequest],
) (*connect.Response[agentv1.SetMemorySettingsResponse], error) {
	if s.memory == nil {
		return nil, notConfigured()
	}
	if !Infra(ctx) {
		return nil, connect.NewError(connect.CodePermissionDenied,
			errors.New("only an admin or owner can choose the memory model"))
	}
	if s.env.MemoryLLMToken == "" {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			errors.New("this host's memory service uses its own key: set PODIUM_AGENT_MEMORY_LLM_TOKEN "+
				"on the conductor and point the memory service at it to choose one here"))
	}
	provider := strings.TrimSpace(req.Msg.GetProvider())
	if provider == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("choose a provider"))
	}
	p, err := findProvider(provider)
	if err != nil {
		return nil, err
	}
	model := strings.TrimSpace(req.Msg.GetModel())
	if model == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("choose a model"))
	}
	if reason := s.memoryProviderUnavailable(ctx, p); reason != "" {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New(reason))
	}

	row := memoryLLMRow{Provider: p.name, Model: model, SetBy: Login(ctx), SetAt: time.Now().UTC()}
	if err := s.probeMemoryModel(ctx, row); err != nil {
		return nil, err
	}
	if err := s.store.PutSetting(ctx, memoryLLMSettingKey, row); err != nil {
		return nil, storeError(err)
	}
	s.logger.InfoContext(ctx, "the memory model was changed",
		"provider", row.Provider, "model", row.Model, "login", row.SetBy)

	settings, err := s.memorySettings(ctx)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&agentv1.SetMemorySettingsResponse{Settings: settings}), nil
}

func (s *AgentService) memorySettings(ctx context.Context) (*agentv1.MemorySettings, error) {
	row, _, err := s.memoryLLMRow(ctx)
	if err != nil {
		return nil, err
	}
	out := &agentv1.MemorySettings{
		Provider:     row.Provider,
		Model:        row.Model,
		SetBy:        row.SetBy,
		ProxyEnabled: s.env.MemoryLLMToken != "",
	}
	if !row.SetAt.IsZero() {
		out.SetAt = timestamppb.New(row.SetAt)
	}
	for _, p := range providerSpecs {
		reason := s.memoryProviderUnavailable(ctx, p)
		out.Providers = append(out.Providers, &agentv1.MemoryProviderOption{
			Provider:  p.name,
			Label:     p.label,
			Available: reason == "",
			Reason:    reason,
		})
	}
	return out, nil
}

func (s *AgentService) memoryLLMRow(ctx context.Context) (memoryLLMRow, bool, error) {
	var row memoryLLMRow
	err := s.store.GetSetting(ctx, memoryLLMSettingKey, &row)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return memoryLLMRow{}, false, nil
	case err != nil:
		return memoryLLMRow{}, false, storeError(err)
	}
	return row, row.Provider != "" && row.Model != "", nil
}

// memoryProviderUnavailable says why p's global credential cannot be spent on extraction,
// or "" when it can. Only a global credential is considered: memory is the organisation's,
// not one person's, so one person's key is never what it spends.
func (s *AgentService) memoryProviderUnavailable(ctx context.Context, p providerSpec) string {
	if s.secrets == nil {
		return "This conductor has no secret store to read a key from."
	}
	v, err := s.secretVersion(ctx, p.secret)
	if err != nil {
		return "Could not check for a saved " + p.label + " key."
	}
	if v == 0 {
		return "No " + p.label + " key is saved for everyone. Add one under Models."
	}
	if p.name == ProviderOpenAI {
		// A ChatGPT sign-in is a Codex token, and api.openai.com refuses it.
		row, ok, err := s.providerRow(ctx, globalTarget(p))
		if err == nil && ok && row.authKind() == AuthOAuth {
			return "The saved OpenAI credential is a ChatGPT sign-in, which the OpenAI API does not accept. Save an API key instead."
		}
	}
	return ""
}

// memoryLLMEndpoint is the provider's OpenAI-compatible chat completions URL.
func (s *AgentService) memoryLLMEndpoint(provider string) (string, error) {
	switch provider {
	case ProviderAnthropic:
		return strings.TrimSuffix(s.baseURL, "/") + "/v1/chat/completions", nil
	case ProviderOpenAI:
		return strings.TrimSuffix(s.openaiBaseURL, "/") + "/v1/chat/completions", nil
	case ProviderXAI:
		return strings.TrimSuffix(s.xaiBaseURL, "/") + "/v1/chat/completions", nil
	case ProviderGoogle:
		return strings.TrimSuffix(s.googleBaseURL, "/") + "/v1beta/openai/chat/completions", nil
	}
	return "", fmt.Errorf("memory: %q has no chat completions endpoint", provider)
}

// shapeMemoryRequest rewrites a chat completions body for row's provider and model. The
// memory service was told a placeholder model name, so the model is always replaced.
func shapeMemoryRequest(body map[string]json.RawMessage, row memoryLLMRow) error {
	model, err := json.Marshal(row.Model)
	if err != nil {
		return err
	}
	body["model"] = model
	// Per-bank cost attribution. It means nothing to the provider behind this route, and not
	// every OpenAI-compatible endpoint accepts it.
	delete(body, "user")
	// Extraction is read in one piece.
	delete(body, "stream")
	delete(body, "stream_options")
	if row.Provider == ProviderOpenAI {
		// The memory service sends max_tokens to a custom base URL, and OpenAI's newer models
		// accept only max_completion_tokens. Every OpenAI model accepts the newer name.
		if mt, ok := body["max_tokens"]; ok {
			if _, has := body["max_completion_tokens"]; !has {
				body["max_completion_tokens"] = mt
			}
			delete(body, "max_tokens")
		}
		if openAIReasoningModel.MatchString(strings.ToLower(row.Model)) {
			delete(body, "temperature")
		}
	}
	return nil
}

// forwardMemoryLLM sends one chat completions body to row's provider with its saved global
// key. The caller owns the response body.
func (s *AgentService) forwardMemoryLLM(
	ctx context.Context, row memoryLLMRow, body map[string]json.RawMessage,
) (*http.Response, error) {
	endpoint, err := s.memoryLLMEndpoint(row.Provider)
	if err != nil {
		return nil, err
	}
	if err := shapeMemoryRequest(body, row); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	key, err := s.HostCredential(ctx, row.Provider)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(key))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	client := *s.http
	client.Timeout = memoryLLMTimeout
	res, err := client.Do(req)
	if err != nil {
		// The URL is in the error; the key is in a header and never in a URL.
		return nil, fmt.Errorf("calling %s: %w", endpoint, err)
	}
	return res, nil
}

// probeMemoryModel makes one tiny completion through forwardMemoryLLM, so a model name the
// provider does not know, or a key it refuses, is caught before it is saved rather than when
// the first retain fails in the background.
func (s *AgentService) probeMemoryModel(ctx context.Context, row memoryLLMRow) error {
	ctx, cancel := context.WithTimeout(ctx, memoryProbeTimeout)
	defer cancel()
	body := map[string]json.RawMessage{
		"messages":   json.RawMessage(`[{"role":"user","content":"Reply with the single word ok."}]`),
		"max_tokens": json.RawMessage(`64`),
	}
	res, err := s.forwardMemoryLLM(ctx, row, body)
	if err != nil {
		return connect.NewError(connect.CodeUnavailable,
			fmt.Errorf("could not reach %s to try the model: %s", row.Provider, operatorDetail(err.Error(), nil)))
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode/100 == 2 {
		_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, errorBodyLimit))
		return nil
	}
	detail := operatorDetail(providerMessage(res), nil)
	code := connect.CodeFailedPrecondition
	if res.StatusCode >= 500 || res.StatusCode == http.StatusTooManyRequests {
		code = connect.CodeUnavailable
	}
	return connect.NewError(code,
		fmt.Errorf("%s did not answer with %s (%s): %s", row.Provider, row.Model, res.Status, detail))
}

// MemoryLLMHandler serves MemoryLLMPath: the memory service's chat completions, forwarded.
//
// It is not behind RequireBearer. Its caller is the memory service, which holds
// PODIUM_AGENT_MEMORY_LLM_TOKEN and nothing else of this conductor's.
func (s *AgentService) MemoryLLMHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := s.env.MemoryLLMToken
		got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if token == "" || !ok || subtle.ConstantTimeCompare([]byte(strings.TrimSpace(got)), []byte(token)) != 1 {
			writeOpenAIError(w, http.StatusUnauthorized, "invalid_api_key",
				"this route needs PODIUM_AGENT_MEMORY_LLM_TOKEN as its bearer")
			return
		}
		rest := strings.TrimPrefix(r.URL.Path, strings.TrimSuffix(MemoryLLMPath, "/"))
		if rest != "/v1/chat/completions" {
			writeOpenAIError(w, http.StatusNotFound, "not_found",
				"only v1/chat/completions is served here")
			return
		}
		if r.Method != http.MethodPost {
			writeOpenAIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST only")
			return
		}

		ctx := r.Context()
		row, ok, err := s.memoryLLMRow(ctx)
		if err != nil {
			s.logger.WarnContext(ctx, "memory extraction: reading the chosen model failed", "error", err)
			writeOpenAIError(w, http.StatusServiceUnavailable, "server_error",
				"the conductor could not read which model memory uses")
			return
		}
		if !ok {
			// 503 rather than 400: the memory service retries it, so a retain that arrives a
			// minute before an admin chooses a model is not lost.
			writeOpenAIError(w, http.StatusServiceUnavailable, "memory_model_not_chosen",
				"no model is chosen for memory yet: an admin chooses one on the Memory page")
			return
		}

		var body map[string]json.RawMessage
		if err := json.NewDecoder(io.LimitReader(r.Body, memoryLLMBodyLimit)).Decode(&body); err != nil {
			writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error",
				"the request is not a JSON object")
			return
		}
		res, err := s.forwardMemoryLLM(ctx, row, body)
		if err != nil {
			s.logger.WarnContext(ctx, "memory extraction: forwarding failed",
				"provider", row.Provider, "model", row.Model, "error", operatorDetail(err.Error(), nil))
			writeOpenAIError(w, http.StatusBadGateway, "upstream_error",
				"the conductor could not reach "+row.Provider)
			return
		}
		defer func() { _ = res.Body.Close() }()
		if res.StatusCode/100 != 2 {
			s.logger.WarnContext(ctx, "memory extraction: the provider refused a call",
				"provider", row.Provider, "model", row.Model, "status", res.StatusCode)
		}
		if ct := res.Header.Get("Content-Type"); ct != "" {
			w.Header().Set("Content-Type", ct)
		}
		if ra := res.Header.Get("Retry-After"); ra != "" {
			w.Header().Set("Retry-After", ra)
		}
		w.WriteHeader(res.StatusCode)
		_, _ = io.Copy(w, res.Body)
	})
}

// writeOpenAIError answers in the envelope an OpenAI client reads its message out of.
func writeOpenAIError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]string{"message": message, "type": code, "code": code},
	})
}
