package api

import (
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

	"github.com/alvaroibarguen/podium/internal/agent/mcp"
)

// OAuth for an MCP server: discovery, dynamic client registration, and the authorization
// code exchange. This is the MCP authorization specification's flow, which is OAuth 2.1 plus
// four RFCs, and every one of them earns its place here:
//
//   - RFC 9728 (protected resource metadata) is how a server SAYS which authorization server
//     guards it. Without it there would be nothing to discover and the issuer would be
//     configuration an operator had to find out by reading a vendor's docs.
//   - RFC 8414 (authorization server metadata) is how that server says where its endpoints
//     are. Nothing here is hard-coded per vendor.
//   - RFC 7591 (dynamic client registration) is what makes a redirect flow possible on an
//     install whose address nobody registered in advance. The existing subscription sign-in
//     (oauth.go) chose the device grant precisely to avoid that problem; DCR solves it
//     instead, by registering THIS install's callback at sign-in time.
//   - RFC 7636 (PKCE) is mandatory in OAuth 2.1 and is what makes it safe for the
//     authorization code to pass through a browser at all: the verifier never leaves this
//     process, so the code the browser hands back is not redeemable by the browser.
//   - RFC 8707 (resource indicators) is what stops a token minted for one MCP server being
//     spent at another. The MCP spec requires it, so `resource` goes on both requests.
//
// Every discovered URL is checked back against the authorization server's own host before
// anything is sent to it, exactly as oauth.go does — discovery output is the one place a
// hostile answer could redirect a credential. The one boundary NOT enforced is between the
// MCP server and its authorization server: RFC 9728 exists so that a resource can delegate
// to an authorization server on another domain, and refusing that would refuse the normal
// case. What is trusted there is the resource's own statement, over TLS, about who guards it.

// The well-known paths. Two spellings of the authorization server's own metadata because
// RFC 8414 defines one and OpenID Connect Discovery defines the other, and real servers ship
// either.
const (
	protectedResourcePath = "/.well-known/oauth-protected-resource"
	authServerPath        = "/.well-known/oauth-authorization-server"
	oidcPath              = "/.well-known/openid-configuration"
)

// The grants this uses. The device grant is oauth.go's; this file is the other two.
const (
	codeGrant     = "authorization_code"
	refreshGrant2 = "refresh_token"
)

// mcpCallbackPath is the ONE path a redirect URI may have. It is a route in the web UI, not
// on podium-server: the SPA reads the code off its own URL and hands it to CompleteMcpOAuth
// over the authenticated API.
//
// That is not a detail of taste. An OAuth redirect is a plain browser GET carrying no bearer
// token, so a callback served by podium-server would have to sit outside the identity
// middleware — an unauthenticated endpoint that makes this conductor go and fetch a
// credential. Routing the code through the API instead means there is no such endpoint, it
// works the same on the dev and tailnet transports, and podium-server needs no new route at
// all. Pinning the path is what keeps a caller from nominating somewhere else.
const mcpCallbackPath = "/agent/mcp/callback"

// mcpClientName is what this conductor calls itself when it registers. It is shown on the
// authorization server's own consent screen, so it says what is asking rather than what
// library is asking.
const mcpClientName = "Podium"

// discoveryBodyLimit bounds a metadata document. These are a few hundred bytes of JSON.
const discoveryBodyLimit = 64 << 10

// discoverTimeout bounds the whole of discovery and registration, which is up to five
// requests to somebody else's servers with an operator waiting on a button.
const discoverTimeout = 20 * time.Second

// errNoAuthServer means the MCP server advertises no OAuth at all, which is not a failure:
// it is a server whose credential has to be pasted, and most are.
var errNoAuthServer = errors.New("this MCP server does not advertise an OAuth authorization server")

// resourceMetadata is the part of RFC 9728 this uses.
type resourceMetadata struct {
	Resource             string   `json:"resource"`
	AuthorizationServers []string `json:"authorization_servers"`
	ScopesSupported      []string `json:"scopes_supported"`
}

// authServerMetadata is the part of RFC 8414 this uses.
type authServerMetadata struct {
	Issuer                string   `json:"issuer"`
	AuthorizationEndpoint string   `json:"authorization_endpoint"`
	TokenEndpoint         string   `json:"token_endpoint"`
	RegistrationEndpoint  string   `json:"registration_endpoint"`
	ScopesSupported       []string `json:"scopes_supported"`
	CodeChallengeMethods  []string `json:"code_challenge_methods_supported"`
	GrantTypesSupported   []string `json:"grant_types_supported"`
}

// registrationResponse is the part of RFC 7591 this uses.
type registrationResponse struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	Error        string `json:"error"`
	ErrorDesc    string `json:"error_description"`
}

// mcpAuthServer is one MCP server's discovered OAuth, resolved and ready to sign in against.
type mcpAuthServer struct {
	meta authServerMetadata
	// resource is the RFC 8707 indicator: what the token is being asked for. It is the MCP
	// server's own canonical URL as the resource metadata reported it, or the registered URL.
	resource string
	// scope is what discovery suggests asking for, which the caller may override.
	scope string
}

// discoverMcpAuth finds the authorization server guarding an MCP server.
//
// The order is the specification's. The resource's own metadata is asked first and is
// authoritative; only if it says nothing is the MCP origin tried as an issuer itself, which
// is what a server that is its own authorization server looks like.
func (s *AgentService) discoverMcpAuth(ctx context.Context, mcpURL string) (*mcpAuthServer, error) {
	target, err := url.Parse(mcpURL)
	if err != nil {
		return nil, fmt.Errorf("the registered URL does not parse: %w", err)
	}

	resource := mcpURL
	var issuers []string
	var scopes []string
	if rm, err := s.resourceMetadata(ctx, target); err == nil {
		issuers = rm.AuthorizationServers
		scopes = rm.ScopesSupported
		if rm.Resource != "" {
			// The server's own canonical spelling of itself, which is what the resource
			// indicator has to carry — not necessarily the URL an operator typed.
			resource = rm.Resource
		}
	}
	// A server that publishes no resource metadata may still be its own authorization
	// server, which is common for a small one somebody wrote themselves.
	if len(issuers) == 0 {
		issuers = []string{target.Scheme + "://" + target.Host}
	}

	var lastErr error
	for _, issuer := range issuers {
		meta, err := s.authServerMetadata(ctx, issuer)
		if err != nil {
			lastErr = err
			continue
		}
		out := &mcpAuthServer{meta: *meta, resource: resource}
		if len(scopes) > 0 {
			out.scope = strings.Join(scopes, " ")
		} else if len(meta.ScopesSupported) > 0 {
			out.scope = strings.Join(meta.ScopesSupported, " ")
		}
		return out, nil
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, errNoAuthServer
}

// resourceMetadata reads RFC 9728. The path-aware spelling is tried first, because a host
// serving several MCP servers publishes one document per path; the bare one is the fallback.
func (s *AgentService) resourceMetadata(ctx context.Context, target *url.URL) (*resourceMetadata, error) {
	origin := target.Scheme + "://" + target.Host
	candidates := []string{origin + protectedResourcePath}
	if p := strings.TrimSuffix(target.Path, "/"); p != "" && p != "/" {
		candidates = []string{origin + protectedResourcePath + p, origin + protectedResourcePath}
	}
	var lastErr error
	for _, u := range candidates {
		var out resourceMetadata
		if err := s.getJSON(ctx, u, &out); err != nil {
			lastErr = err
			continue
		}
		return &out, nil
	}
	return nil, lastErr
}

// authServerMetadata reads RFC 8414, then OpenID Connect Discovery, and holds every endpoint
// it is told about to the issuer's own site.
//
// RFC 8414 inserts the well-known segment BEFORE an issuer's path component, which is not
// what anybody guesses; the OIDC spelling appends it. Both are tried because real servers
// ship either, and a couple ship the appended RFC 8414 path too.
func (s *AgentService) authServerMetadata(ctx context.Context, issuer string) (*authServerMetadata, error) {
	iss, err := url.Parse(issuer)
	if err != nil {
		return nil, fmt.Errorf("authorization server %q does not parse: %w", issuer, err)
	}
	if iss.Scheme != "https" && !isLoopbackHost(iss.Hostname()) {
		return nil, fmt.Errorf("authorization server %q is not https", issuer)
	}
	origin := iss.Scheme + "://" + iss.Host
	path := strings.TrimSuffix(iss.Path, "/")

	candidates := []string{origin + authServerPath + path, origin + path + oidcPath}
	if path != "" {
		candidates = append(candidates, origin+path+authServerPath)
	}

	var lastErr error
	for _, u := range candidates {
		var meta authServerMetadata
		if err := s.getJSON(ctx, u, &meta); err != nil {
			lastErr = err
			continue
		}
		if meta.AuthorizationEndpoint == "" || meta.TokenEndpoint == "" {
			lastErr = fmt.Errorf("%s does not name both an authorization and a token endpoint", u)
			continue
		}
		// The same rule oauth.go applies, and the reason is the same: discovery output is
		// where a hostile answer would point a credential somewhere else.
		for _, ep := range []string{meta.AuthorizationEndpoint, meta.TokenEndpoint, meta.RegistrationEndpoint} {
			if ep == "" {
				continue
			}
			if err := sameSite(issuer, ep); err != nil {
				return nil, err
			}
		}
		if meta.Issuer == "" {
			meta.Issuer = issuer
		}
		return &meta, nil
	}
	if lastErr == nil {
		lastErr = errNoAuthServer
	}
	return nil, lastErr
}

// registerMcpClient is RFC 7591: this conductor asking to be a client of an authorization
// server it has never met, with the callback this install actually uses.
//
// A server with no registration endpoint is not a dead end — some publish a client id out of
// band — but there is nothing this can do about it, so it says so with the endpoint named.
func (s *AgentService) registerMcpClient(
	ctx context.Context, meta authServerMetadata, redirectURI, scope string,
) (*registrationResponse, error) {
	if meta.RegistrationEndpoint == "" {
		return nil, fmt.Errorf("%s does not support dynamic client registration, so this "+
			"conductor cannot sign in to it; paste a token instead", meta.Issuer)
	}
	body := map[string]any{
		"client_name":    mcpClientName,
		"redirect_uris":  []string{redirectURI},
		"grant_types":    []string{codeGrant, refreshGrant2},
		"response_types": []string{"code"},
		// A public client with PKCE. Asking for no secret is the OAuth 2.1 answer for a
		// client that cannot keep one from the browser it redirects, and a server that
		// issues one anyway is honoured below.
		"token_endpoint_auth_method": "none",
		"application_type":           "web",
	}
	if scope != "" {
		body["scope"] = scope
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, meta.RegistrationEndpoint,
		strings.NewReader(string(raw)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	res, err := s.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("registering with %s: %w", meta.RegistrationEndpoint, err)
	}
	defer func() { _ = res.Body.Close() }()
	payload, _ := io.ReadAll(io.LimitReader(res.Body, errorBodyLimit))

	var out registrationResponse
	if err := json.Unmarshal(payload, &out); err != nil {
		return nil, fmt.Errorf("%s answered %s with something that is not a client registration",
			meta.RegistrationEndpoint, res.Status)
	}
	switch {
	case out.Error != "":
		detail := out.ErrorDesc
		if detail == "" {
			detail = out.Error
		}
		return nil, fmt.Errorf("%s refused the client registration: %s",
			meta.RegistrationEndpoint, detail)
	case out.ClientID == "":
		return nil, fmt.Errorf("%s answered %s but named no client id",
			meta.RegistrationEndpoint, res.Status)
	}
	return &out, nil
}

// authorizeURL builds where the browser goes. The state and the challenge are what tie the
// callback back to this flow; `resource` is what ties the token to this MCP server.
func authorizeURL(meta authServerMetadata, clientID, redirectURI, scope, state, verifier, resource string) string {
	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {clientID},
		"redirect_uri":          {redirectURI},
		"state":                 {state},
		"code_challenge":        {challengeOf(verifier)},
		"code_challenge_method": {"S256"},
	}
	if scope != "" {
		q.Set("scope", scope)
	}
	if resource != "" {
		q.Set("resource", resource)
	}
	sep := "?"
	if strings.Contains(meta.AuthorizationEndpoint, "?") {
		sep = "&"
	}
	return meta.AuthorizationEndpoint + sep + q.Encode()
}

// exchangeMcpCode trades the authorization code for a token.
func (s *AgentService) exchangeMcpCode(
	ctx context.Context, f *mcpFlow, code string,
) (*tokenResponse, error) {
	form := url.Values{
		"grant_type":    {codeGrant},
		"code":          {code},
		"redirect_uri":  {f.redirectURI},
		"client_id":     {f.clientID},
		"code_verifier": {f.verifier},
	}
	if f.resource != "" {
		form.Set("resource", f.resource)
	}
	if f.clientSecret != "" {
		form.Set("client_secret", f.clientSecret)
	}
	return s.mcpToken(ctx, f.tokenEndpoint, form)
}

// refreshMcpToken trades a refresh token for a new access token. The resource indicator goes
// on this request too: a refresh that omitted it could come back scoped to something else.
func (s *AgentService) refreshMcpToken(
	ctx context.Context, o mcp.OAuth,
) (*tokenResponse, error) {
	form := url.Values{
		"grant_type":    {refreshGrant2},
		"refresh_token": {o.RefreshToken},
		"client_id":     {o.ClientID},
	}
	if o.Resource != "" {
		form.Set("resource", o.Resource)
	}
	if o.ClientSecret != "" {
		form.Set("client_secret", o.ClientSecret)
	}
	if o.Scope != "" {
		form.Set("scope", o.Scope)
	}
	return s.mcpToken(ctx, o.TokenEndpoint, form)
}

// mcpToken POSTs a grant and turns anything that is not a usable token into an error with
// the authorization server's own sentence in it.
func (s *AgentService) mcpToken(
	ctx context.Context, endpoint string, form url.Values,
) (*tokenResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint,
		strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	res, err := s.http.Do(req)
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
	switch {
	case out.Error != "":
		detail := out.ErrorDesc
		if detail == "" {
			detail = out.Error
		}
		return nil, fmt.Errorf("%s refused the request: %s", endpoint, detail)
	case res.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("%s answered %s: %s", endpoint, res.Status,
			oauthErrorText(raw, res.Status))
	case out.AccessToken == "":
		return nil, fmt.Errorf("%s returned no access token", endpoint)
	}
	return &out, nil
}

// getJSON reads a metadata document. A non-2xx is an error and not a value: unlike a token
// endpoint, a well-known path has no documented error body.
func (s *AgentService) getJSON(ctx context.Context, endpoint string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	res, err := s.http.Do(req)
	if err != nil {
		return fmt.Errorf("reaching %s: %w", endpoint, err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("%s answered %s", endpoint, res.Status)
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, discoveryBodyLimit))
	if err != nil {
		return fmt.Errorf("reading %s: %w", endpoint, err)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("%s answered something that is not JSON: %w", endpoint, err)
	}
	return nil
}

// validateRedirectURI holds the browser's claim about where it lives to a shape.
//
// The browser supplies this because it is the only party that knows the address this control
// plane is reached at. What is checked is everything that can be checked without knowing
// that address: a real scheme, https unless it is loopback, no credentials, and exactly the
// one path the web UI serves the callback on. A caller who could nominate an arbitrary path
// on an arbitrary host could have the authorization server deliver a code somewhere else —
// and while the code alone is not redeemable without the verifier this process kept, there
// is no reason to allow it.
func validateRedirectURI(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", fmt.Errorf("redirect_uri %q does not parse: %w", raw, err)
	}
	switch {
	case u.Scheme != "https" && (u.Scheme != "http" || !isLoopbackHost(u.Hostname())):
		return "", fmt.Errorf("redirect_uri %q must be https, or http on loopback", raw)
	case u.Host == "":
		return "", fmt.Errorf("redirect_uri %q names no host", raw)
	case u.User != nil:
		return "", fmt.Errorf("redirect_uri %q may not carry credentials", raw)
	case u.RawQuery != "" || u.Fragment != "":
		return "", fmt.Errorf("redirect_uri %q may not carry a query or a fragment", raw)
	case strings.TrimSuffix(u.Path, "/") != mcpCallbackPath:
		return "", fmt.Errorf("redirect_uri %q must end in %s, which is where the web UI "+
			"receives a callback", raw, mcpCallbackPath)
	}
	return u.String(), nil
}

// isLoopbackHost is the one exception to https, for an install reached at localhost.
func isLoopbackHost(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1" || host == "[::1]"
}

// randomToken is a state or a PKCE verifier: 32 bytes of crypto/rand, base64url, unpadded.
// crypto/rand.Read cannot fail on any platform Podium runs on, and a sign-in with a
// predictable state is worse than no sign-in, so the error is a panic rather than a value
// nobody would check.
func randomToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// challengeOf is the S256 PKCE challenge. `plain` is not offered: OAuth 2.1 requires S256
// where the client can do it, and this one can.
func challengeOf(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// mcpOAuthOf turns a completed token response into the bag a refresh needs.
func mcpOAuthOf(f *mcpFlow, tok *tokenResponse) mcp.OAuth {
	out := mcp.OAuth{
		Issuer:        f.issuer,
		TokenEndpoint: f.tokenEndpoint,
		ClientID:      f.clientID,
		ClientSecret:  f.clientSecret,
		RefreshToken:  tok.RefreshToken,
		ExpiresAt:     expiryOf(tok),
		Scope:         tok.Scope,
		Account:       account(tok),
		Resource:      f.resource,
	}
	if out.Scope == "" {
		// A server that does not echo the grant back: what was asked for is the best
		// available account of what was given.
		out.Scope = f.scope
	}
	return out
}

// mcpFlow is one sign-in this conductor started and is waiting on.
//
// The verifier, the client registration and the state stay HERE. A browser is handed a flow
// id and a URL; neither is a credential, and neither lets a browser complete a sign-in on
// its own — which is what makes it safe for the authorization code to travel through one.
type mcpFlow struct {
	name          string
	issuer        string
	tokenEndpoint string
	clientID      string
	clientSecret  string
	redirectURI   string
	scope         string
	resource      string
	verifier      string
	state         string
	expiresAt     time.Time
	startedBy     string
}

// expired is whether this flow is past its own deadline.
func (f *mcpFlow) expired(now time.Time) bool { return now.After(f.expiresAt) }

func (s *AgentService) putMcpFlow(id string, f *mcpFlow) {
	s.flowMu.Lock()
	defer s.flowMu.Unlock()
	if s.mcpFlows == nil {
		s.mcpFlows = map[string]*mcpFlow{}
	}
	s.mcpFlows[id] = f
}

func (s *AgentService) mcpFlow(id string) (*mcpFlow, bool) {
	s.flowMu.Lock()
	defer s.flowMu.Unlock()
	f, ok := s.mcpFlows[id]
	return f, ok
}

func (s *AgentService) dropMcpFlow(id string) {
	s.flowMu.Lock()
	defer s.flowMu.Unlock()
	delete(s.mcpFlows, id)
}

// sweepMcpFlows drops the sign-ins nobody finished. A flow holds a PKCE verifier and a
// client registration; a map of them that is never swept is a leak of both.
func (s *AgentService) sweepMcpFlows() {
	s.flowMu.Lock()
	defer s.flowMu.Unlock()
	now := time.Now()
	for id, f := range s.mcpFlows {
		if f.expired(now) {
			delete(s.mcpFlows, id)
		}
	}
}
