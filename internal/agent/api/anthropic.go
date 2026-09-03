package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// anthropicVersion is the API version header Anthropic requires on every request. It is a
// date, not a semver, and 2023-06-01 is the current one.
const anthropicVersion = "2023-06-01"

// maxModelsReported caps SetProviderKeyResponse.models. It is proof that the key works, not
// a model picker.
const maxModelsReported = 20

// errorBodyLimit is how much of a provider error body is read before giving up on it.
const errorBodyLimit = 8 << 10

// errKeyRefused means the provider itself said no. The key is not saved and retrying with
// the same key cannot help.
var errKeyRefused = errors.New("the provider refused this key")

// errCannotValidate means we never found out. Nothing is saved, and trying again may work.
var errCannotValidate = errors.New("could not validate the key with the provider")

// modelsResponse is the documented shape of GET /v1/models. Only the ids are read.
type modelsResponse struct {
	Data []struct {
		ID string `json:"id"`
	} `json:"data"`
}

// anthropicError is the envelope every Anthropic 4xx/5xx carries.
type anthropicError struct {
	Type  string `json:"type"`
	Error struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// validateAnthropicKey asks Anthropic whether a key works, and returns the model ids it can
// see. It is a GET of the model list: there is no token cost and no side effect.
//
// The status mapping is the part worth reading. **An invalid key comes back 400, not 401** —
// verified against the live API, and the opposite of what the plan assumed. So 400 joins 401
// and 403 in "refused". GET /v1/models takes no parameters, so a 400 this code provoked for
// any other reason would be a bug here, and the provider's own message is passed through so
// it is visible rather than swallowed. 429, 5xx and every transport failure are "could not
// find out", which is a different answer: nothing is saved either way, but only one of them
// is worth retrying.
func validateAnthropicKey(ctx context.Context, hc *http.Client, baseURL string, key []byte) ([]string, error) {
	url := strings.TrimSuffix(baseURL, "/") + "/v1/models"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errCannotValidate, err)
	}
	req.Header.Set("x-api-key", string(key))
	req.Header.Set("anthropic-version", anthropicVersion)
	req.Header.Set("Accept", "application/json")

	res, err := hc.Do(req)
	if err != nil {
		// The URL is in the error; the key is in a header and never in a URL.
		return nil, fmt.Errorf("%w: %w", errCannotValidate, err)
	}
	defer func() { _ = res.Body.Close() }()

	switch res.StatusCode {
	case http.StatusOK:
		var body modelsResponse
		if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&body); err != nil {
			return nil, fmt.Errorf("%w: %s answered 200 with something that is not a model list: %w",
				errCannotValidate, url, err)
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
		return nil, fmt.Errorf("%w: %s", errKeyRefused, providerMessage(res))

	default:
		return nil, fmt.Errorf("%w: %s answered %s: %s",
			errCannotValidate, url, res.Status, providerMessage(res))
	}
}

// providerMessage is the provider's own words about a failure, or its status line when the
// body is not the documented envelope. It never contains the key: the key was only ever a
// request header.
func providerMessage(res *http.Response) string {
	raw, err := io.ReadAll(io.LimitReader(res.Body, errorBodyLimit))
	if err != nil || len(raw) == 0 {
		return res.Status
	}
	var body anthropicError
	if err := json.Unmarshal(raw, &body); err == nil && body.Error.Message != "" {
		return body.Error.Message
	}
	return res.Status
}
