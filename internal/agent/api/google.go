package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"
)

// googleModelPrefix is what the Gemini API puts in front of every model id it lists. The
// harness, the catalogue and every playbook name a model without it.
const googleModelPrefix = "models/"

// googleModelsResponse is the documented shape of GET /v1beta/models. Only the names are read.
type googleModelsResponse struct {
	Models []struct {
		Name string `json:"name"`
	} `json:"models"`
}

// validateGoogleKey asks the Gemini API whether a key works, and returns the model ids it
// can see. Like the others it is a GET of the model list: no token cost and no side effect.
//
// The key goes in the x-goog-api-key header and never in the ?key= query parameter the
// Gemini docs also show, because a URL ends up in error strings and this one would carry
// the key with it.
//
// An invalid key is a 400 with "API key not valid", and a key for a project that has not
// enabled the API is a 403; both are the provider refusing this key, exactly as Anthropic's
// 400 is. The error envelope is OpenAI-shaped ({"error": {"message": …}}), so xaiMessage
// reads it.
func validateGoogleKey(ctx context.Context, hc *http.Client, baseURL string, key []byte) ([]string, error) {
	url := strings.TrimSuffix(baseURL, "/") + "/v1beta/models"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, &validationError{kind: errCannotValidate, detail: err.Error(), cause: err}
	}
	req.Header.Set("x-goog-api-key", string(key))
	req.Header.Set("Accept", "application/json")

	res, err := hc.Do(req)
	if err != nil {
		return nil, &validationError{kind: errCannotValidate, detail: err.Error(), cause: err}
	}
	defer func() { _ = res.Body.Close() }()

	switch res.StatusCode {
	case http.StatusOK:
		var body googleModelsResponse
		if err := jsonDecodeLimited(res, &body); err != nil {
			return nil, &validationError{
				kind:   errCannotValidate,
				detail: fmt.Sprintf("%s answered 200 with something that is not a model list: %s", url, err),
				cause:  err,
			}
		}
		ids := make([]string, 0, len(body.Models))
		for _, m := range body.Models {
			id := strings.TrimPrefix(m.Name, googleModelPrefix)
			if id == "" {
				continue
			}
			ids = append(ids, id)
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

// unusualGoogleFormat is the note a save carries when a key does not look like one. Gemini
// API keys are Google Cloud API keys, which start AIza.
func unusualGoogleFormat(key []byte) string {
	if strings.HasPrefix(string(key), "AIza") {
		return ""
	}
	return "key format looks unusual; validated anyway"
}
