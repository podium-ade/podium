package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"
)

// validateOpenAIKey asks OpenAI whether an API key works, and returns the model ids it can
// see. Same request as xAI: GET /v1/models with a bearer, no token cost.
func validateOpenAIKey(ctx context.Context, hc *http.Client, baseURL string, key []byte) ([]string, error) {
	return validateBearerModels(ctx, hc, strings.TrimSuffix(baseURL, "/")+"/v1/models", key)
}

// validateOpenAICodexToken asks ChatGPT's Codex backend whether a subscription access token
// works. The path is /models on the Codex root, not /v1/models: that root already is the
// API, and a token that api.openai.com would refuse is the whole reason this path exists.
func validateOpenAICodexToken(ctx context.Context, hc *http.Client, baseURL string, key []byte) ([]string, error) {
	url := strings.TrimSuffix(baseURL, "/") + "/models"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, &validationError{kind: errCannotValidate, detail: err.Error(), cause: err}
	}
	req.Header.Set("Authorization", "Bearer "+string(key))
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "podium")
	if id := chatgptAccountID(string(key)); id != "" {
		req.Header.Set("ChatGPT-Account-Id", id)
	}
	return doBearerModels(ctx, hc, req, url, key)
}

// validateBearerModels is GET <url> with a bearer, for an OpenAI-shaped model list.
func validateBearerModels(ctx context.Context, hc *http.Client, url string, key []byte) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, &validationError{kind: errCannotValidate, detail: err.Error(), cause: err}
	}
	req.Header.Set("Authorization", "Bearer "+string(key))
	req.Header.Set("Accept", "application/json")
	return doBearerModels(ctx, hc, req, url, key)
}

func doBearerModels(_ context.Context, hc *http.Client, req *http.Request, url string, _ []byte) ([]string, error) {
	res, err := hc.Do(req)
	if err != nil {
		return nil, &validationError{kind: errCannotValidate, detail: err.Error(), cause: err}
	}
	defer func() { _ = res.Body.Close() }()

	switch res.StatusCode {
	case http.StatusOK:
		var body modelsResponse
		if err := jsonDecodeLimited(res, &body); err != nil {
			return nil, &validationError{
				kind:   errCannotValidate,
				detail: fmt.Sprintf("%s answered 200 with something that is not a model list: %s", url, err),
				cause:  err,
			}
		}
		ids := make([]string, 0, len(body.Data))
		for _, m := range body.Data {
			if m.ID == "" {
				continue
			}
			ids = append(ids, m.ID)
			if len(ids) == maxModelsReported {
				break
			}
		}
		return ids, nil

	case http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden:
		return nil, &validationError{kind: errKeyRefused, detail: xaiMessage(res)}

	default:
		return nil, &validationError{
			kind:   errCannotValidate,
			detail: fmt.Sprintf("%s answered %s: %s", url, res.Status, xaiMessage(res)),
		}
	}
}

// unusualOpenAIFormat is the note a save carries when a key does not look like one.
// A subscription access token is a JWT and legitimately does not carry the sk- prefix;
// this note is only attached to a pasted key, so a JWT never sees it.
func unusualOpenAIFormat(key []byte) string {
	if strings.HasPrefix(string(key), "sk-") {
		return ""
	}
	return "key format looks unusual; validated anyway"
}

// chatgptAccountID is the ChatGPT account id a Codex access token names, or "". It is
// read out of the JWT without verifying the signature, for the same reason account() is:
// the token came back over TLS from an endpoint already checked against the issuer, and
// the value is only ever a request header — nothing authorises on it.
func chatgptAccountID(token string) string {
	claims := jwtClaims(token)
	if claims == nil {
		return ""
	}
	auth, _ := claims["https://api.openai.com/auth"].(map[string]any)
	if auth == nil {
		return ""
	}
	id, _ := auth["chatgpt_account_id"].(string)
	return strings.TrimSpace(id)
}
