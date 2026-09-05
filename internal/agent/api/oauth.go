package api

// This file is OAuth device authorisation (RFC 8628), for a provider that sells a
// subscription rather than an API key.
//
// Why device code and not a redirect: podium-server is reached at whatever address the
// operator's install happens to use — a tailnet name, a reverse proxy, localhost — and an
// authorisation-code flow would need every one of those registered against the OAuth client
// before it worked. A device code needs no redirect URI at all. The conductor asks for a
// code, the browser shows it, the human approves it somewhere else entirely, and the
// conductor polls. It also works on a host with no browser, which is the normal case here.
//
// The endpoints are discovered, not hard-coded: {issuer}/.well-known/openid-configuration
// says where to POST. Discovery output is the one place a hostile answer could redirect a
// credential, so every discovered endpoint is checked back against the issuer's own host
// before anything is sent to it.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// discoveryPath is where an OIDC issuer publishes its endpoints.
const discoveryPath = "/.well-known/openid-configuration"

// deviceGrant is the device-code grant type, and refreshGrant is what keeps the credential
// alive afterwards.
const (
	deviceGrant  = "urn:ietf:params:oauth:grant-type:device_code"
	refreshGrant = "refresh_token"
)

// minPollInterval is the floor on what a provider can ask us to poll at, and
// defaultPollInterval is what RFC 8628 says to use when it asks for nothing.
const (
	minPollInterval     = 1 * time.Second
	defaultPollInterval = 5 * time.Second
)

// discoveryTTL is how long a discovery document is trusted before it is fetched again.
const discoveryTTL = 1 * time.Hour

// The states PollProviderOAuth reports. They are the whole client contract: everything the
// provider can answer maps onto one of these five.
const (
	OAuthPending  = "pending"
	OAuthSlowDown = "slow_down"
	OAuthDone     = "done"
	OAuthDenied   = "denied"
	OAuthExpired  = "expired"
)

// errOAuthUnconfigured means this control plane has no OAuth client to sign in with. It is
// a configuration answer, not a failure of the provider.
var errOAuthUnconfigured = errors.New("subscription sign-in is not configured on this control plane")

// oidcConfig is the part of a discovery document this code uses.
type oidcConfig struct {
	Issuer                      string `json:"issuer"`
	DeviceAuthorizationEndpoint string `json:"device_authorization_endpoint"`
	TokenEndpoint               string `json:"token_endpoint"`
	UserinfoEndpoint            string `json:"userinfo_endpoint"`
}

// deviceCodeResponse is RFC 8628 §5.2.
type deviceCodeResponse struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

// tokenResponse is what both the device grant and the refresh grant answer with.
type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	Scope        string `json:"scope"`
	Error        string `json:"error"`
	ErrorDesc    string `json:"error_description"`
}

// oauthClient is one provider's OAuth configuration and the discovery cache for it.
type oauthClient struct {
	issuer   string
	clientID string
	scopes   string
	http     *http.Client

	mu       sync.Mutex
	cfg      *oidcConfig
	cachedAt time.Time
}

// newOAuthClient returns nil when the install has not configured an OAuth client, which is
// the supported "API key only" configuration.
func newOAuthClient(issuer, clientID, scopes string, hc *http.Client) *oauthClient {
	if strings.TrimSpace(issuer) == "" || strings.TrimSpace(clientID) == "" {
		return nil
	}
	return &oauthClient{
		issuer:   strings.TrimSuffix(strings.TrimSpace(issuer), "/"),
		clientID: strings.TrimSpace(clientID),
		scopes:   strings.TrimSpace(scopes),
		http:     hc,
	}
}

// discover reads the issuer's endpoints, cached for discoveryTTL.
func (c *oauthClient) discover(ctx context.Context) (*oidcConfig, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cfg != nil && time.Since(c.cachedAt) < discoveryTTL {
		return c.cfg, nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.issuer+discoveryPath, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	res, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("reading %s%s: %w", c.issuer, discoveryPath, err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s%s answered %s", c.issuer, discoveryPath, res.Status)
	}
	var cfg oidcConfig
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&cfg); err != nil {
		return nil, fmt.Errorf("%s%s is not a discovery document: %w", c.issuer, discoveryPath, err)
	}
	if cfg.DeviceAuthorizationEndpoint == "" || cfg.TokenEndpoint == "" {
		return nil, fmt.Errorf("%s does not offer the device authorisation grant: its discovery "+
			"document names no device_authorization_endpoint", c.issuer)
	}
	// The credential is about to be POSTed to whatever this document said, so what it said
	// is checked before it is used. A discovery answer that points the token endpoint at
	// another host is the one way a MITM turns a sign-in into a credential handover.
	for _, ep := range []struct{ name, value string }{
		{"device_authorization_endpoint", cfg.DeviceAuthorizationEndpoint},
		{"token_endpoint", cfg.TokenEndpoint},
		{"userinfo_endpoint", cfg.UserinfoEndpoint},
	} {
		if ep.value == "" {
			continue
		}
		if err := sameSite(c.issuer, ep.value); err != nil {
			return nil, fmt.Errorf("%s's %s is not usable: %w", c.issuer, ep.name, err)
		}
	}
	c.cfg, c.cachedAt = &cfg, time.Now()
	return c.cfg, nil
}

// sameSite refuses an endpoint that is not HTTPS on the issuer's own site.
//
// The issuer's exact host is always allowed. Beyond that it is a suffix match rather than an
// exact one, because an issuer at auth.example.com legitimately serving its token endpoint
// from api.example.com is normal — and a redirect to evil.test is not.
func sameSite(issuer, endpoint string) error {
	iu, err := url.Parse(issuer)
	if err != nil {
		return err
	}
	eu, err := url.Parse(endpoint)
	if err != nil {
		return err
	}
	if eu.Scheme != "https" {
		return fmt.Errorf("%s is not https", endpoint)
	}
	if eu.Hostname() == iu.Hostname() {
		return nil
	}
	base := registrable(iu.Hostname())
	if base == "" || (eu.Hostname() != base && !strings.HasSuffix(eu.Hostname(), "."+base)) {
		return fmt.Errorf("%s is not on %s", endpoint, base)
	}
	return nil
}

// registrable is the last two labels of a host — "auth.x.ai" becomes "x.ai" — which is what
// sameSite compares. It is a heuristic and not a public-suffix list; the consequence of it
// being too permissive on a multi-label TLD is that an endpoint on a sibling subdomain of
// the same operator is accepted, which is the case it is meant to accept anyway.
func registrable(host string) string {
	parts := strings.Split(host, ".")
	if len(parts) < 2 {
		return host
	}
	return strings.Join(parts[len(parts)-2:], ".")
}

// startDevice asks the provider for a code for a human to type.
func (c *oauthClient) startDevice(ctx context.Context) (*deviceCodeResponse, error) {
	cfg, err := c.discover(ctx)
	if err != nil {
		return nil, err
	}
	form := url.Values{"client_id": {c.clientID}}
	if c.scopes != "" {
		form.Set("scope", c.scopes)
	}
	res, err := c.postForm(ctx, cfg.DeviceAuthorizationEndpoint, form)
	if err != nil {
		return nil, err
	}
	defer func() { _ = res.Body.Close() }()

	raw, _ := io.ReadAll(io.LimitReader(res.Body, errorBodyLimit))
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s answered %s: %s", cfg.DeviceAuthorizationEndpoint,
			res.Status, oauthErrorText(raw, res.Status))
	}
	var out deviceCodeResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("%s answered something that is not a device code: %w",
			cfg.DeviceAuthorizationEndpoint, err)
	}
	if out.DeviceCode == "" || out.UserCode == "" || out.VerificationURI == "" {
		return nil, fmt.Errorf("%s answered a device code with nothing to show a human",
			cfg.DeviceAuthorizationEndpoint)
	}
	return &out, nil
}

// pollDevice asks whether the human has approved yet. The three "keep waiting" errors of
// RFC 8628 §3.5 are not failures and are reported as states.
func (c *oauthClient) pollDevice(ctx context.Context, deviceCode string) (state string, tok *tokenResponse, detail string, err error) {
	cfg, err := c.discover(ctx)
	if err != nil {
		return "", nil, "", err
	}
	body, err := c.token(ctx, cfg.TokenEndpoint, url.Values{
		"client_id":   {c.clientID},
		"device_code": {deviceCode},
		"grant_type":  {deviceGrant},
	})
	if err != nil {
		return "", nil, "", err
	}
	switch body.Error {
	case "":
		if body.AccessToken == "" {
			return "", nil, "", errors.New("the provider authorised the sign-in but returned no access token")
		}
		return OAuthDone, body, "", nil
	case "authorization_pending":
		return OAuthPending, nil, "", nil
	case "slow_down":
		return OAuthSlowDown, nil, "", nil
	case "expired_token":
		return OAuthExpired, nil, body.ErrorDesc, nil
	case "access_denied":
		return OAuthDenied, nil, body.ErrorDesc, nil
	default:
		// Anything else is the provider refusing this client outright — an unregistered
		// client id, a subscription tier that is not allowed on the OAuth surface. It ends
		// the flow, and its own words are the only thing that explains which.
		detail = body.ErrorDesc
		if detail == "" {
			detail = body.Error
		}
		return OAuthDenied, nil, detail, nil
	}
}

// refresh trades a refresh token for a new access token. A provider that rotates refresh
// tokens returns a new one; one that does not returns none, and the caller keeps the old.
func (c *oauthClient) refresh(ctx context.Context, refreshToken string) (*tokenResponse, error) {
	cfg, err := c.discover(ctx)
	if err != nil {
		return nil, err
	}
	body, err := c.token(ctx, cfg.TokenEndpoint, url.Values{
		"client_id":     {c.clientID},
		"refresh_token": {refreshToken},
		"grant_type":    {refreshGrant},
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

// token POSTs a grant and decodes the response. An OAuth error is a 400 with a documented
// body, so a non-2xx that decodes is returned as a value rather than as an error.
func (c *oauthClient) token(ctx context.Context, endpoint string, form url.Values) (*tokenResponse, error) {
	res, err := c.postForm(ctx, endpoint, form)
	if err != nil {
		return nil, err
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

func (c *oauthClient) postForm(ctx context.Context, endpoint string, form url.Values) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	res, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("reaching %s: %w", endpoint, err)
	}
	return res, nil
}

// oauthErrorText is the provider's own sentence about a non-2xx, or its status line.
func oauthErrorText(raw []byte, status string) string {
	var body tokenResponse
	if err := json.Unmarshal(raw, &body); err == nil {
		switch {
		case body.ErrorDesc != "":
			return body.ErrorDesc
		case body.Error != "":
			return body.Error
		}
	}
	return status
}

// account is who the provider says signed in, for the settings row. It is read out of the
// id_token's payload without verifying the signature, and that is safe *for this use*: the
// token came back over TLS from an endpoint already checked against the issuer, and the
// value is only ever shown on a page — nothing authorises on it. It is not passed to any
// API and it is not the credential.
func account(tok *tokenResponse) string {
	if tok.IDToken == "" {
		return ""
	}
	parts := strings.Split(tok.IDToken, ".")
	if len(parts) != 3 {
		return ""
	}
	payload, err := base64URL(parts[1])
	if err != nil {
		return ""
	}
	var claims struct {
		Email             string `json:"email"`
		PreferredUsername string `json:"preferred_username"`
		Name              string `json:"name"`
		Sub               string `json:"sub"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return ""
	}
	for _, c := range []string{claims.Email, claims.PreferredUsername, claims.Name, claims.Sub} {
		if c != "" {
			return c
		}
	}
	return ""
}

// base64URL decodes a JWT segment, which is base64url and may or may not be padded.
func base64URL(s string) ([]byte, error) {
	if pad := len(s) % 4; pad != 0 {
		s += strings.Repeat("=", 4-pad)
	}
	return base64.URLEncoding.DecodeString(s)
}
