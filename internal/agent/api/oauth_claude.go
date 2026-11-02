package api

// Claude OAuth is the Claude Pro/Max subscription sign-in. It is not a device grant:
// Anthropic offers none. It is the authorisation-code grant with PKCE (RFC 7636) and a
// redirect to Anthropic's own page, which shows the code instead of sending it anywhere — the
// "paste the code back" flow Claude Code uses on a machine with no browser. So the conductor
// mints a verifier and a state, the browser opens the authorize URL, the human approves and
// copies the code, and pasting it into Podium is what PollProviderOAuth exchanges.
//
// The client, the endpoints and the request shapes are Claude Code's own (see
// config.DefaultAnthropicOAuthClientID). The verifier never leaves this process except to the
// token endpoint, so a code seen by anybody else is worthless on its own.
//
// What the token is spent on is narrow on purpose: only a chat-host turn, through opencode 2's
// claude-agent provider, which runs Claude Code itself. See conductor.credentialOwner.

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// claudeFlowTTL is how long a started sign-in waits for its code.
const claudeFlowTTL = 15 * time.Minute

// claudeOAuthBeta is the beta an OAuth bearer has to name on Anthropic's API.
const claudeOAuthBeta = "oauth-2025-04-20"

// claudeClient is one Claude OAuth configuration.
type claudeClient struct {
	clientID     string
	authorizeURL string
	tokenURL     string
	redirectURI  string
	scopes       string
	http         *http.Client
}

// newClaudeClient returns nil when the sign-in is off: no client id, or no endpoint.
func newClaudeClient(clientID, authorizeURL, tokenURL, redirectURI, scopes string, hc *http.Client) *claudeClient {
	c := &claudeClient{
		clientID:     strings.TrimSpace(clientID),
		authorizeURL: strings.TrimSpace(authorizeURL),
		tokenURL:     strings.TrimSpace(tokenURL),
		redirectURI:  strings.TrimSpace(redirectURI),
		scopes:       strings.TrimSpace(scopes),
		http:         hc,
	}
	if c.clientID == "" || c.authorizeURL == "" || c.tokenURL == "" || c.redirectURI == "" {
		return nil
	}
	return c
}

// claudeTokenResponse is the token endpoint's answer. Its error may be OAuth's string or
// Anthropic's {type, message} object, so it is decoded raw.
type claudeTokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	Scope        string `json:"scope"`
	Account      struct {
		EmailAddress string `json:"email_address"`
	} `json:"account"`
	Error     json.RawMessage `json:"error"`
	ErrorDesc string          `json:"error_description"`
}

func (r *claudeTokenResponse) token() *tokenResponse {
	return &tokenResponse{
		AccessToken:  r.AccessToken,
		RefreshToken: r.RefreshToken,
		ExpiresIn:    r.ExpiresIn,
		Scope:        r.Scope,
		Account:      r.Account.EmailAddress,
	}
}

// problem is the provider's own words about a refusal, or "".
func (r *claudeTokenResponse) problem() string {
	if r.ErrorDesc != "" {
		return r.ErrorDesc
	}
	if len(r.Error) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(r.Error, &s) == nil {
		return s
	}
	var obj struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	}
	if json.Unmarshal(r.Error, &obj) == nil {
		if obj.Message != "" {
			return obj.Message
		}
		return obj.Type
	}
	return ""
}

func (c *claudeClient) startDevice(_ context.Context) (*deviceCodeResponse, error) {
	verifier, err := randomURLToken(32)
	if err != nil {
		return nil, err
	}
	state, err := randomURLToken(32)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(verifier))
	u, err := url.Parse(c.authorizeURL)
	if err != nil {
		return nil, fmt.Errorf("the Claude authorize URL is not a URL: %w", err)
	}
	q := u.Query()
	q.Set("code", "true")
	q.Set("client_id", c.clientID)
	q.Set("response_type", "code")
	q.Set("redirect_uri", c.redirectURI)
	if c.scopes != "" {
		q.Set("scope", c.scopes)
	}
	q.Set("code_challenge", base64.RawURLEncoding.EncodeToString(sum[:]))
	q.Set("code_challenge_method", "S256")
	q.Set("state", state)
	u.RawQuery = q.Encode()
	link := u.String()
	return &deviceCodeResponse{
		DeviceCode:              verifier,
		VerificationURI:         link,
		VerificationURIComplete: link,
		ExpiresIn:               int(claudeFlowTTL / time.Second),
		Interval:                int(defaultPollInterval / time.Second),
		State:                   state,
		CodePaste:               true,
	}, nil
}

// pollFlow exchanges the pasted code. With nothing pasted yet it is pending, and Anthropic is
// not asked: there is nothing to ask it about.
func (c *claudeClient) pollFlow(ctx context.Context, flow *oauthFlow) (string, *tokenResponse, string, error) {
	if flow.code == "" {
		return OAuthPending, nil, "", nil
	}
	code, state := splitClaudeCode(flow.code)
	if code == "" {
		return OAuthDenied, nil, "that is not the code Claude showed; start the sign-in again", nil
	}
	if state != "" && state != flow.state {
		return OAuthDenied, nil, "that code is from a different sign-in; start again and paste the code it shows", nil
	}
	body, status, err := c.post(ctx, map[string]string{
		"grant_type":    "authorization_code",
		"code":          code,
		"redirect_uri":  c.redirectURI,
		"client_id":     c.clientID,
		"code_verifier": flow.deviceCode,
		"state":         flow.state,
	})
	if err != nil {
		return "", nil, "", err
	}
	switch {
	case status == http.StatusOK && body.AccessToken != "":
		return OAuthDone, body.token(), "", nil
	case status == http.StatusOK:
		return "", nil, "", errors.New("the provider authorised the sign-in but returned no access token")
	case status >= 500:
		return "", nil, "", fmt.Errorf("%s answered %s", c.tokenURL, statusLine(status))
	default:
		// A wrong, used or expired code. The verifier is single-use with it, so the sign-in
		// starts again.
		detail := body.problem()
		if detail == "" {
			detail = statusLine(status)
		}
		return OAuthDenied, nil, detail, nil
	}
}

func (c *claudeClient) refresh(ctx context.Context, refreshToken string) (*tokenResponse, error) {
	payload := map[string]string{
		"grant_type":    refreshGrant,
		"refresh_token": refreshToken,
		"client_id":     c.clientID,
	}
	if c.scopes != "" {
		payload["scope"] = c.scopes
	}
	body, status, err := c.post(ctx, payload)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		detail := body.problem()
		if detail == "" {
			detail = statusLine(status)
		}
		return nil, fmt.Errorf("the provider refused the refresh token: %s", detail)
	}
	if body.AccessToken == "" {
		return nil, errors.New("the provider returned no access token")
	}
	return body.token(), nil
}

func (c *claudeClient) post(ctx context.Context, payload map[string]string) (*claudeTokenResponse, int, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.tokenURL, bytes.NewReader(raw))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	res, err := c.http.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("reaching %s: %w", c.tokenURL, err)
	}
	defer func() { _ = res.Body.Close() }()
	data, _ := io.ReadAll(io.LimitReader(res.Body, errorBodyLimit))
	var out claudeTokenResponse
	if err := json.Unmarshal(data, &out); err != nil {
		if res.StatusCode >= 500 {
			return &out, res.StatusCode, nil
		}
		return nil, 0, fmt.Errorf("%s answered %s with something that is not a token response",
			c.tokenURL, res.Status)
	}
	return &out, res.StatusCode, nil
}

// splitClaudeCode reads what a human pasted: Anthropic's page shows `code#state`, and a
// pasted callback URL carries both as query parameters.
func splitClaudeCode(pasted string) (code, state string) {
	s := strings.TrimSpace(trimPastedKey(pasted))
	if u, err := url.Parse(s); err == nil && u.Scheme != "" && u.Query().Get("code") != "" {
		return u.Query().Get("code"), u.Query().Get("state")
	}
	code, state, _ = strings.Cut(s, "#")
	return strings.TrimSpace(code), strings.TrimSpace(state)
}

func randomURLToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generating a sign-in secret: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// validateClaudeOAuthToken asks Anthropic who a subscription access token belongs to. It is
// the profile read Claude Code makes at sign-in: no tokens spent, and a token that cannot
// read its own profile is not one a turn could use either.
func validateClaudeOAuthToken(ctx context.Context, hc *http.Client, baseURL string, token []byte) ([]string, error) {
	u := strings.TrimSuffix(baseURL, "/") + "/api/oauth/profile"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, &validationError{kind: errCannotValidate, detail: err.Error(), cause: err}
	}
	req.Header.Set("Authorization", "Bearer "+string(token))
	req.Header.Set("anthropic-beta", claudeOAuthBeta)
	req.Header.Set("Accept", "application/json")
	res, err := hc.Do(req)
	if err != nil {
		return nil, &validationError{kind: errCannotValidate, detail: err.Error(), cause: err}
	}
	defer func() { _ = res.Body.Close() }()
	switch res.StatusCode {
	case http.StatusOK:
		return nil, nil
	case http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden:
		return nil, &validationError{kind: errKeyRefused, detail: providerMessage(res)}
	default:
		return nil, &validationError{
			kind:   errCannotValidate,
			detail: fmt.Sprintf("%s answered %s: %s", u, res.Status, providerMessage(res)),
		}
	}
}
