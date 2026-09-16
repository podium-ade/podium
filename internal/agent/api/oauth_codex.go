package api

// Codex OAuth is OpenAI's ChatGPT-subscription device flow. It is not RFC 8628: there is
// no discovery document and no device_code grant. The conductor asks for a user code,
// the human types it at auth.openai.com/codex/device, the conductor polls a pending
// endpoint that answers 403/404 until it is done, then exchanges an authorization code
// (with a PKCE verifier OpenAI minted) for tokens.
//
// The endpoints and client id are the same ones Hermes ships as CODEX_OAUTH_* in
// hermes_cli/auth_constants.py. OpenAI does not offer self-service registration, so that
// is the client every third-party sign-in uses. The consent screen names Codex, not
// Podium.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

const (
	codexUserCodePath = "/api/accounts/deviceauth/usercode"
	codexPollPath     = "/api/accounts/deviceauth/token"
	codexTokenPath    = "/oauth/token"
	codexDevicePath   = "/codex/device"
	codexRedirectPath = "/deviceauth/callback"
	codexUserAgent    = "podium"
	codexDefaultPoll  = 5
)

// codexClient is one OpenAI Codex OAuth configuration.
type codexClient struct {
	issuer   string
	clientID string
	http     *http.Client
}

func newCodexClient(issuer, clientID string, hc *http.Client) *codexClient {
	if strings.TrimSpace(issuer) == "" || strings.TrimSpace(clientID) == "" {
		return nil
	}
	return &codexClient{
		issuer:   strings.TrimSuffix(strings.TrimSpace(issuer), "/"),
		clientID: strings.TrimSpace(clientID),
		http:     hc,
	}
}

type codexUserCodeResponse struct {
	UserCode     string          `json:"user_code"`
	DeviceAuthID string          `json:"device_auth_id"`
	ExpiresIn    int             `json:"expires_in"`
	Interval     json.RawMessage `json:"interval"`
}

type codexAuthCodeResponse struct {
	AuthorizationCode string `json:"authorization_code"`
	CodeVerifier      string `json:"code_verifier"`
}

func (c *codexClient) startDevice(ctx context.Context) (*deviceCodeResponse, error) {
	endpoint := c.issuer + codexUserCodePath
	if err := sameSite(c.issuer, endpoint); err != nil {
		return nil, fmt.Errorf("%s's device code endpoint is not usable: %w", c.issuer, err)
	}
	raw, status, err := c.postJSON(ctx, endpoint, map[string]string{"client_id": c.clientID})
	if err != nil {
		return nil, err
	}
	if status == http.StatusTooManyRequests {
		return nil, fmt.Errorf("%s answered 429: OpenAI is rate-limiting login requests; wait a minute and try again", endpoint)
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("%s answered %s: %s", endpoint, statusLine(status), oauthErrorText(raw, statusLine(status)))
	}
	var out codexUserCodeResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("%s answered something that is not a device code: %w", endpoint, err)
	}
	if out.UserCode == "" || out.DeviceAuthID == "" {
		return nil, fmt.Errorf("%s answered a device code with nothing to show a human", endpoint)
	}
	verify := c.issuer + codexDevicePath
	return &deviceCodeResponse{
		DeviceCode:              out.DeviceAuthID,
		UserCode:                out.UserCode,
		VerificationURI:         verify,
		VerificationURIComplete: verify + "?user_code=" + url.QueryEscape(out.UserCode),
		ExpiresIn:               out.ExpiresIn,
		Interval:                intervalOf(out.Interval),
	}, nil
}

func (c *codexClient) pollFlow(ctx context.Context, flow *oauthFlow) (state string, tok *tokenResponse, detail string, err error) {
	if flow.deviceCode == "" || flow.userCode == "" {
		return "", nil, "", errors.New("the sign-in is missing a device code")
	}
	endpoint := c.issuer + codexPollPath
	if err := sameSite(c.issuer, endpoint); err != nil {
		return "", nil, "", fmt.Errorf("%s's device poll endpoint is not usable: %w", c.issuer, err)
	}
	raw, status, err := c.postJSON(ctx, endpoint, map[string]string{
		"device_auth_id": flow.deviceCode,
		"user_code":      flow.userCode,
	})
	if err != nil {
		return "", nil, "", err
	}
	switch status {
	case http.StatusOK:
		var code codexAuthCodeResponse
		if err := json.Unmarshal(raw, &code); err != nil {
			return "", nil, "", fmt.Errorf("%s answered something that is not an authorization code: %w", endpoint, err)
		}
		tok, err := c.exchange(ctx, code)
		if err != nil {
			return "", nil, "", err
		}
		return OAuthDone, tok, "", nil
	case http.StatusForbidden, http.StatusNotFound:
		// OpenAI's pending signal. RFC 8628 would have sent authorization_pending in a
		// 400 body; here the status *is* the state.
		return OAuthPending, nil, "", nil
	case http.StatusTooManyRequests:
		return OAuthSlowDown, nil, "", nil
	default:
		detail := oauthErrorText(raw, statusLine(status))
		return OAuthDenied, nil, detail, nil
	}
}

func (c *codexClient) exchange(ctx context.Context, code codexAuthCodeResponse) (*tokenResponse, error) {
	if code.AuthorizationCode == "" || code.CodeVerifier == "" {
		return nil, errors.New("the provider authorised the sign-in but returned no authorization code")
	}
	endpoint := c.issuer + codexTokenPath
	if err := sameSite(c.issuer, endpoint); err != nil {
		return nil, fmt.Errorf("%s's token endpoint is not usable: %w", c.issuer, err)
	}
	body, err := c.postForm(ctx, endpoint, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code.AuthorizationCode},
		"redirect_uri":  {c.issuer + codexRedirectPath},
		"client_id":     {c.clientID},
		"code_verifier": {code.CodeVerifier},
	})
	if err != nil {
		return nil, err
	}
	if body.Error != "" {
		detail := body.ErrorDesc
		if detail == "" {
			detail = body.Error
		}
		return nil, fmt.Errorf("the provider refused the token exchange: %s", detail)
	}
	if body.AccessToken == "" {
		return nil, errors.New("the provider authorised the sign-in but returned no access token")
	}
	return body, nil
}

func (c *codexClient) refresh(ctx context.Context, refreshToken string) (*tokenResponse, error) {
	endpoint := c.issuer + codexTokenPath
	if err := sameSite(c.issuer, endpoint); err != nil {
		return nil, fmt.Errorf("%s's token endpoint is not usable: %w", c.issuer, err)
	}
	body, err := c.postForm(ctx, endpoint, url.Values{
		"grant_type":    {refreshGrant},
		"refresh_token": {refreshToken},
		"client_id":     {c.clientID},
	})
	if err != nil {
		return nil, err
	}
	if body.Error != "" {
		detail := body.ErrorDesc
		if detail == "" {
			detail = body.Error
		}
		return nil, fmt.Errorf("the provider refused the refresh token: %s", detail)
	}
	if body.AccessToken == "" {
		return nil, errors.New("the provider returned no access token")
	}
	return body, nil
}

func (c *codexClient) postJSON(ctx context.Context, endpoint string, payload map[string]string) ([]byte, int, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", codexUserAgent)
	res, err := c.http.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("reaching %s: %w", endpoint, err)
	}
	defer func() { _ = res.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(res.Body, errorBodyLimit))
	return body, res.StatusCode, nil
}

func (c *codexClient) postForm(ctx context.Context, endpoint string, form url.Values) (*tokenResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", codexUserAgent)
	res, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("reaching %s: %w", endpoint, err)
	}
	defer func() { _ = res.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, errorBodyLimit))

	var out tokenResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("%s answered %s with something that is not a token response",
			endpoint, res.Status)
	}
	if res.StatusCode >= 500 {
		return nil, fmt.Errorf("%s answered %s: %s", endpoint, res.Status, oauthErrorText(raw, res.Status))
	}
	if res.StatusCode != http.StatusOK && out.Error == "" {
		return nil, fmt.Errorf("%s answered %s", endpoint, res.Status)
	}
	return &out, nil
}

func intervalOf(raw json.RawMessage) int {
	if len(raw) == 0 {
		return codexDefaultPoll
	}
	var n int
	if json.Unmarshal(raw, &n) == nil && n > 0 {
		return n
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			return n
		}
	}
	return codexDefaultPoll
}

func statusLine(code int) string {
	return fmt.Sprintf("%d %s", code, http.StatusText(code))
}
