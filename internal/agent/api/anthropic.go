package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"unicode"
)

// anthropicVersion is the API version header Anthropic requires on every request. It is a
// date, not a semver, and 2023-06-01 is the current one.
const anthropicVersion = "2023-06-01"

// maxModelsReported caps SetProviderKeyResponse.models. It is proof that the key works, not
// a model picker.
const maxModelsReported = 20

// errorBodyLimit is how much of a provider error body is read before giving up on it.
const errorBodyLimit = 8 << 10

// providerDetailLimit bounds the explanation that reaches an operator. Anthropic's messages
// are a sentence; anything longer than this is a body that is not what it claims to be.
const providerDetailLimit = 400

// errKeyRefused means the provider itself said no to this request. The key is not saved.
//
// It does NOT mean the key is dead, and the classification is deliberately wider than that.
// Anthropic answers 400 both for a key it has never heard of and for a key it knows but that
// needs something this request did not send — an identity-linked key wants an
// anthropic-workspace-id header, and Podium supports standard workspace keys only, so it
// sends none. Both are the provider refusing *this* request, both leave nothing stored, and
// both are permission_denied to the operator. Only the provider's own sentence separates
// "get a new key" from "that kind of key is not supported here", which is exactly why
// validationError carries that sentence instead of throwing it away.
var errKeyRefused = errors.New("the provider refused this key")

// errCannotValidate means we never found out. Nothing is saved, and trying again may work.
var errCannotValidate = errors.New("could not validate the key with the provider")

// keyShaped matches anything with the shape of a provider key, so a message that echoes one
// back — whole, or truncated by the provider's own formatting — never reaches a page.
var keyShaped = regexp.MustCompile(`(?i)\b(?:sk|ant)-[A-Za-z0-9_-]{8,}`)

// validationError is a validation failure that still knows what to tell the operator.
//
// kind is errKeyRefused or errCannotValidate, so errors.Is classifies it exactly as a
// wrapped sentinel did; detail is the sentence behind that classification, which
// SetProviderKey passes on to the operator rather than dropping at the RPC boundary.
type validationError struct {
	kind   error
	detail string
	cause  error
}

func (e *validationError) Error() string { return e.kind.Error() + ": " + e.detail }

func (e *validationError) Unwrap() []error {
	if e.cause == nil {
		return []error{e.kind}
	}
	return []error{e.kind, e.cause}
}

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
		return nil, &validationError{kind: errCannotValidate, detail: err.Error(), cause: err}
	}
	req.Header.Set("x-api-key", string(key))
	req.Header.Set("anthropic-version", anthropicVersion)
	req.Header.Set("Accept", "application/json")

	res, err := hc.Do(req)
	if err != nil {
		// The URL is in the error; the key is in a header and never in a URL.
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
		return nil, &validationError{kind: errKeyRefused, detail: providerMessage(res)}

	default:
		return nil, &validationError{
			kind:   errCannotValidate,
			detail: fmt.Sprintf("%s answered %s: %s", url, res.Status, providerMessage(res)),
		}
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

// validationDetail is the explanation inside a validation failure, or "" when there is not
// one to give.
func validationDetail(err error) string {
	var v *validationError
	if errors.As(err, &v) {
		return v.detail
	}
	return ""
}

// operatorDetail is a provider explanation made fit to hand to a human.
//
// PROVENANCE, because somebody will eventually want to harden this away: this text comes
// from **Anthropic**, over TLS, in answer to a request this process made. It is NOT the
// untrusted task output docs/security.md is about — no agent turn, no repository and no
// ticket can put a byte into it — which is why it is shown to the operator verbatim instead
// of being swallowed. Swallowing it cost this project an hour of misdiagnosis: a key that
// only needed a header was written off as dead.
//
// It is still a string of unknown length and shape from another company's server, so it is
// treated as one: control characters and newlines collapsed to single spaces, the result
// bounded, and anything key-shaped scrubbed in case a provider ever echoes a key back. The
// browser then renders it as a text node and never as markup — the same stance one layer up
// in web/src/lib/markdown.ts.
func operatorDetail(detail string, key []byte) string {
	clean := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, detail)
	clean = strings.Join(strings.Fields(clean), " ")
	if len(key) > 0 {
		clean = strings.ReplaceAll(clean, string(key), "[redacted]")
	}
	clean = keyShaped.ReplaceAllString(clean, "[redacted]")
	if r := []rune(clean); len(r) > providerDetailLimit {
		clean = strings.TrimSpace(string(r[:providerDetailLimit])) + "…"
	}
	return clean
}
