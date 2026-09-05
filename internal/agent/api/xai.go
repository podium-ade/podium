package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// xaiError is every shape an xAI failure has been seen in. The API is OpenAI-shaped, and
// OpenAI-shaped APIs disagree with each other about whether `error` is an object or a
// string, so both are decoded and whichever arrived is used.
type xaiError struct {
	Code   string `json:"code"`
	Detail string `json:"detail"`
	Error  struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

// xaiStringError is the other half of the same body: `{"error": "a sentence"}`.
type xaiStringError struct {
	Error string `json:"error"`
}

// validateXAIKey asks xAI whether a credential works, and returns the model ids it can see.
// It is a GET of the model list: no token cost and no side effect.
//
// The credential may be an API key (xai-…) or the access token of a subscription sign-in.
// Both are bearer tokens for the same endpoint, which is the whole reason the two auth
// kinds share one secret and one code path from here on.
//
// The status mapping matches the Anthropic one deliberately: 400/401/403 are the provider
// refusing this credential, and everything else — 429, 5xx, a transport failure — is "could
// not find out", which is worth retrying. Nothing is stored either way.
func validateXAIKey(ctx context.Context, hc *http.Client, baseURL string, key []byte) ([]string, error) {
	url := strings.TrimSuffix(baseURL, "/") + "/v1/models"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, &validationError{kind: errCannotValidate, detail: err.Error(), cause: err}
	}
	req.Header.Set("Authorization", "Bearer "+string(key))
	req.Header.Set("Accept", "application/json")

	res, err := hc.Do(req)
	if err != nil {
		// The URL is in the error; the credential is in a header and never in a URL.
		return nil, &validationError{kind: errCannotValidate, detail: err.Error(), cause: err}
	}
	defer func() { _ = res.Body.Close() }()

	switch res.StatusCode {
	case http.StatusOK:
		var body modelsResponse
		if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&body); err != nil {
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

// xaiMessage is xAI's own words about a failure, or its status line when the body is not
// one of the shapes above. It never contains the credential: the credential was only ever a
// request header.
func xaiMessage(res *http.Response) string {
	raw, err := io.ReadAll(io.LimitReader(res.Body, errorBodyLimit))
	if err != nil || len(raw) == 0 {
		return res.Status
	}
	var body xaiError
	if err := json.Unmarshal(raw, &body); err == nil {
		switch {
		case body.Error.Message != "":
			return body.Error.Message
		case body.Detail != "":
			return body.Detail
		case body.Code != "":
			return body.Code
		}
	}
	var str xaiStringError
	if err := json.Unmarshal(raw, &str); err == nil && str.Error != "" {
		return str.Error
	}
	return res.Status
}

// unusualXAIFormat is the note the response carries when a key does not look like one.
// It is a note and not a refusal: xAI has already agreed the credential works, and a
// subscription access token legitimately does not carry the xai- prefix.
func unusualXAIFormat(key []byte) string {
	if strings.HasPrefix(string(key), "xai-") {
		return ""
	}
	return "key format looks unusual; validated anyway"
}
