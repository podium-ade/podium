package api

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/agent/mcp"
)

// fakeMcpServer is an MCP server that advertises OAuth the way the specification says to: a
// protected-resource document naming its authorization server, and that server's own
// metadata naming its endpoints, dynamic registration and a token endpoint.
//
// It is one httptest server playing both roles, which is also the common real shape — a
// vendor's MCP endpoint and its authorization server on the same site.
type fakeMcpServer struct {
	srv *httptest.Server
	// mcpPath is where the "MCP server" itself lives, so the path-aware well-known lookup
	// has something to find.
	mcpPath string

	// noResourceMetadata drops the RFC 9728 document, leaving the origin to be tried as an
	// issuer itself.
	noResourceMetadata bool
	// noRegistration drops the registration endpoint.
	noRegistration bool
	// authorizationServers overrides what the resource document points at.
	authorizationServers []string
	// tokenEndpoint overrides what the metadata advertises, for the hostile-document tests.
	tokenEndpoint string
	// scopes is what the resource says it wants.
	scopes []string
	// issuedSecret is handed back by registration when set, making this a confidential client.
	issuedSecret string
	// tokenErr, when set, is the OAuth error the token endpoint answers with.
	tokenErr string
	// noRefreshToken drops the refresh token from the token response.
	noRefreshToken bool

	// What the fake recorded, for the assertions.
	registrations atomic.Int32
	lastRedirect  atomic.Value // string
	lastForm      atomic.Value // url.Values
}

func newFakeMcpServer(t *testing.T) *fakeMcpServer {
	t.Helper()
	f := &fakeMcpServer{mcpPath: "/mcp"}
	mux := http.NewServeMux()

	mux.HandleFunc(protectedResourcePath+"/mcp", func(w http.ResponseWriter, _ *http.Request) {
		if f.noResourceMetadata {
			http.NotFound(w, nil)
			return
		}
		servers := f.authorizationServers
		if servers == nil {
			servers = []string{f.srv.URL}
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"resource":              f.srv.URL + f.mcpPath,
			"authorization_servers": servers,
			"scopes_supported":      f.scopes,
		})
	})

	metadata := func(w http.ResponseWriter, _ *http.Request) {
		doc := map[string]any{
			"issuer":                           f.srv.URL,
			"authorization_endpoint":           f.srv.URL + "/authorize",
			"token_endpoint":                   f.srv.URL + "/token",
			"code_challenge_methods_supported": []string{"S256"},
			"grant_types_supported":            []string{"authorization_code", "refresh_token"},
		}
		if !f.noRegistration {
			doc["registration_endpoint"] = f.srv.URL + "/register"
		}
		if f.tokenEndpoint != "" {
			doc["token_endpoint"] = f.tokenEndpoint
		}
		writeJSON(w, http.StatusOK, doc)
	}
	mux.HandleFunc(authServerPath, metadata)
	mux.HandleFunc(oidcPath, metadata)

	mux.HandleFunc("/register", func(w http.ResponseWriter, r *http.Request) {
		f.registrations.Add(1)
		var body struct {
			RedirectURIs []string `json:"redirect_uris"`
			ClientName   string   `json:"client_name"`
			AuthMethod   string   `json:"token_endpoint_auth_method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if len(body.RedirectURIs) == 1 {
			f.lastRedirect.Store(body.RedirectURIs[0])
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"client_id":     "client-abc",
			"client_secret": f.issuedSecret,
		})
	})

	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		f.lastForm.Store(r.Form)
		if f.tokenErr != "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error":             f.tokenErr,
				"error_description": "the authorization server said no",
			})
			return
		}
		doc := map[string]any{
			"access_token": "mcp-access-" + r.Form.Get("grant_type"),
			"token_type":   "Bearer",
			"expires_in":   3600,
			"scope":        "read write",
		}
		if !f.noRefreshToken {
			doc["refresh_token"] = "mcp-refresh-2"
		}
		writeJSON(w, http.StatusOK, doc)
	})

	// TLS, because discovery refuses a plaintext endpoint and this fake has to be able to
	// advertise usable ones. srv.Client() trusts the certificate.
	f.srv = httptest.NewTLSServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

// service is a conductor whose HTTP client trusts this fake's certificate. Nothing else
// about it is configured: the MCP sign-in has no client id, no issuer and no scopes to be
// given, because it discovers and registers all three.
func (f *fakeMcpServer) service() *AgentService {
	return NewAgentService(AgentServiceOptions{HTTPClient: f.srv.Client()})
}

func (f *fakeMcpServer) url() string { return f.srv.URL + f.mcpPath }

func (f *fakeMcpServer) form() url.Values {
	v, _ := f.lastForm.Load().(url.Values)
	return v
}

// Discovery is the whole point: nothing about a server's OAuth is configuration. The
// resource document names the authorization server, and that server's metadata names its
// endpoints.
func TestDiscoveringAnMcpServersAuthorizationServer(t *testing.T) {
	f := newFakeMcpServer(t)
	f.scopes = []string{"read", "write"}
	s := f.service()

	as, err := s.discoverMcpAuth(t.Context(), f.url())
	require.NoError(t, err)
	assert.Equal(t, f.srv.URL, as.meta.Issuer)
	assert.Equal(t, f.srv.URL+"/token", as.meta.TokenEndpoint)
	assert.Equal(t, f.srv.URL+"/register", as.meta.RegistrationEndpoint)
	// The resource indicator is the server's OWN spelling of itself, which is what the
	// token has to be requested for.
	assert.Equal(t, f.srv.URL+"/mcp", as.resource)
	assert.Equal(t, "read write", as.scope)
}

// A server that publishes no RFC 9728 document may still be its own authorization server,
// which is what a small one somebody wrote themselves looks like.
func TestAServerWithNoResourceMetadataIsTriedAsItsOwnIssuer(t *testing.T) {
	f := newFakeMcpServer(t)
	f.noResourceMetadata = true
	s := f.service()

	as, err := s.discoverMcpAuth(t.Context(), f.url())
	require.NoError(t, err)
	assert.Equal(t, f.srv.URL, as.meta.Issuer)
	// With nothing to say otherwise, the registered URL is the resource.
	assert.Equal(t, f.url(), as.resource)
}

// The one boundary this does NOT enforce is resource-to-authorization-server: RFC 9728
// exists so a resource can delegate to a server on another domain. What is enforced is the
// authorization server's endpoints against its own site, exactly as the subscription
// sign-in does — discovery output is where a hostile answer would redirect a credential.
func TestDiscoveryRefusesAnEndpointOffTheAuthorizationServersSite(t *testing.T) {
	f := newFakeMcpServer(t)
	f.tokenEndpoint = "https://evil.test/token"
	s := f.service()

	_, err := s.discoverMcpAuth(t.Context(), f.url())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "evil.test")
}

func TestDiscoverySaysSoWhenThereIsNoOAuthAtAll(t *testing.T) {
	bare := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(bare.Close)
	s := NewAgentService(AgentServiceOptions{HTTPClient: bare.Client()})

	_, err := s.discoverMcpAuth(t.Context(), bare.URL+"/mcp")
	require.Error(t, err)
}

// Dynamic registration is what makes a redirect flow work on an install nobody registered
// in advance: the callback this browser actually uses is what gets registered.
func TestRegisteringThisInstallsOwnCallback(t *testing.T) {
	f := newFakeMcpServer(t)
	s := f.service()
	as, err := s.discoverMcpAuth(t.Context(), f.url())
	require.NoError(t, err)

	redirect := "https://podium.example.ts.net" + mcpCallbackPath
	reg, err := s.registerMcpClient(t.Context(), as.meta, redirect, "read")
	require.NoError(t, err)
	assert.Equal(t, "client-abc", reg.ClientID)
	assert.Equal(t, int32(1), f.registrations.Load())
	assert.Equal(t, redirect, f.lastRedirect.Load())
}

func TestAServerWithNoRegistrationEndpointSaysToPasteAToken(t *testing.T) {
	f := newFakeMcpServer(t)
	f.noRegistration = true
	s := f.service()
	as, err := s.discoverMcpAuth(t.Context(), f.url())
	require.NoError(t, err)

	_, err = s.registerMcpClient(t.Context(), as.meta, "https://x.test"+mcpCallbackPath, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "paste a token")
}

// The authorize URL is what the browser is sent to, and it has to carry the four things
// that tie the sign-in together: the state, the S256 challenge, the callback and the
// resource indicator.
func TestTheAuthorizeURLCarriesPKCEAndTheResource(t *testing.T) {
	meta := authServerMetadata{AuthorizationEndpoint: "https://auth.test/authorize"}
	verifier := randomToken()
	raw := authorizeURL(meta, "client-abc", "https://x.test"+mcpCallbackPath, "read",
		"state-1", verifier, "https://mcp.test/mcp")

	u, err := url.Parse(raw)
	require.NoError(t, err)
	q := u.Query()
	assert.Equal(t, "code", q.Get("response_type"))
	assert.Equal(t, "client-abc", q.Get("client_id"))
	assert.Equal(t, "state-1", q.Get("state"))
	assert.Equal(t, "S256", q.Get("code_challenge_method"))
	assert.Equal(t, "https://mcp.test/mcp", q.Get("resource"))
	assert.Equal(t, "read", q.Get("scope"))
	// The challenge is the hash and never the verifier: the verifier is what stays here.
	assert.Equal(t, challengeOf(verifier), q.Get("code_challenge"))
	assert.NotContains(t, raw, verifier)
}

// RFC 7636 appendix B's own vector. The challenge is the one thing in this flow that a
// remote server checks our arithmetic on, so it is pinned rather than round-tripped.
func TestTheChallengeIsS256OfTheVerifier(t *testing.T) {
	const verifier = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	sum := sha256.Sum256([]byte(verifier))
	assert.Equal(t, base64.RawURLEncoding.EncodeToString(sum[:]), challengeOf(verifier))
	assert.Equal(t, "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM", challengeOf(verifier))
}

func TestTheExchangeSendsTheVerifierAndTheResource(t *testing.T) {
	f := newFakeMcpServer(t)
	s := f.service()
	flow := &mcpFlow{
		tokenEndpoint: f.srv.URL + "/token",
		clientID:      "client-abc",
		redirectURI:   "https://x.test" + mcpCallbackPath,
		resource:      f.url(),
		verifier:      "verifier-1",
	}

	tok, err := s.exchangeMcpCode(t.Context(), flow, "code-1")
	require.NoError(t, err)
	assert.Equal(t, "mcp-access-authorization_code", tok.AccessToken)

	form := f.form()
	assert.Equal(t, "authorization_code", form.Get("grant_type"))
	assert.Equal(t, "code-1", form.Get("code"))
	assert.Equal(t, "verifier-1", form.Get("code_verifier"))
	assert.Equal(t, f.url(), form.Get("resource"))
	assert.Empty(t, form.Get("client_secret"))
}

// A registration that issued a secret makes this a confidential client, and the secret has
// to go on the exchange or the server refuses it.
func TestAConfidentialClientSendsItsSecret(t *testing.T) {
	f := newFakeMcpServer(t)
	s := f.service()
	flow := &mcpFlow{
		tokenEndpoint: f.srv.URL + "/token",
		clientID:      "client-abc",
		clientSecret:  "shhh",
		redirectURI:   "https://x.test" + mcpCallbackPath,
		verifier:      "verifier-1",
	}

	_, err := s.exchangeMcpCode(t.Context(), flow, "code-1")
	require.NoError(t, err)
	assert.Equal(t, "shhh", f.form().Get("client_secret"))
}

func TestAnExchangeRefusalCarriesTheServersOwnSentence(t *testing.T) {
	f := newFakeMcpServer(t)
	f.tokenErr = "invalid_grant"
	s := f.service()
	flow := &mcpFlow{tokenEndpoint: f.srv.URL + "/token", clientID: "c", verifier: "v"}

	_, err := s.exchangeMcpCode(t.Context(), flow, "code-1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "the authorization server said no")
}

// A refresh names the same resource. One that omitted it could come back scoped to
// something else entirely.
func TestARefreshNamesTheSameResource(t *testing.T) {
	f := newFakeMcpServer(t)
	s := f.service()

	tok, err := s.refreshMcpToken(t.Context(), mcp.OAuth{
		TokenEndpoint: f.srv.URL + "/token",
		ClientID:      "client-abc",
		RefreshToken:  "mcp-refresh-1",
		Resource:      f.url(),
		Scope:         "read",
	})
	require.NoError(t, err)
	assert.Equal(t, "mcp-access-refresh_token", tok.AccessToken)
	// A server that rotates gives a new one, and the caller has to keep it or the next
	// refresh fails.
	assert.Equal(t, "mcp-refresh-2", tok.RefreshToken)

	form := f.form()
	assert.Equal(t, "refresh_token", form.Get("grant_type"))
	assert.Equal(t, "mcp-refresh-1", form.Get("refresh_token"))
	assert.Equal(t, f.url(), form.Get("resource"))
}

// The browser supplies the redirect URI because it is the only party that knows this
// install's address. What can be checked without knowing that address is checked.
func TestTheRedirectURIIsHeldToAShape(t *testing.T) {
	ok := []string{
		"https://podium.example.ts.net" + mcpCallbackPath,
		"https://podium.example.ts.net" + mcpCallbackPath + "/",
		"http://localhost:8080" + mcpCallbackPath,
		"http://127.0.0.1:8080" + mcpCallbackPath,
	}
	for _, raw := range ok {
		_, err := validateRedirectURI(raw)
		assert.NoError(t, err, raw)
	}

	bad := []string{
		"",
		"not a url",
		// Plaintext anywhere but loopback.
		"http://podium.example.com" + mcpCallbackPath,
		// Somewhere else entirely, which is the case this exists to refuse.
		"https://evil.test/steal",
		"https://podium.example.com/agent/mcp",
		"https://podium.example.com" + mcpCallbackPath + "?x=1",
		"https://podium.example.com" + mcpCallbackPath + "#x",
		"https://user:pw@podium.example.com" + mcpCallbackPath,
		"ftp://podium.example.com" + mcpCallbackPath,
	}
	for _, raw := range bad {
		_, err := validateRedirectURI(raw)
		assert.Error(t, err, raw)
	}
}

// What a completed sign-in leaves behind is everything a refresh needs and nothing more.
func TestACompletedFlowRecordsWhatARefreshNeeds(t *testing.T) {
	flow := &mcpFlow{
		issuer:        "https://auth.test",
		tokenEndpoint: "https://auth.test/token",
		clientID:      "client-abc",
		clientSecret:  "shhh",
		resource:      "https://mcp.test/mcp",
		scope:         "read",
	}
	o := mcpOAuthOf(flow, &tokenResponse{
		AccessToken:  "access-1",
		RefreshToken: "refresh-1",
		ExpiresIn:    3600,
		Scope:        "read write",
	})
	assert.Equal(t, "https://auth.test/token", o.TokenEndpoint)
	assert.Equal(t, "client-abc", o.ClientID)
	assert.Equal(t, "shhh", o.ClientSecret)
	assert.Equal(t, "refresh-1", o.RefreshToken)
	assert.Equal(t, "https://mcp.test/mcp", o.Resource)
	// What was granted, not what was asked for, when the server says.
	assert.Equal(t, "read write", o.Scope)
	assert.False(t, o.ExpiresAt.IsZero())
	// The access token is NOT in here: it goes to the control plane's secret store.
	blob, err := json.Marshal(o)
	require.NoError(t, err)
	assert.NotContains(t, string(blob), "access-1")
}

// A server that echoes no scope back leaves what was asked for as the best account of what
// was granted.
func TestAGrantWithNoScopeEchoedKeepsWhatWasAsked(t *testing.T) {
	o := mcpOAuthOf(&mcpFlow{scope: "read"}, &tokenResponse{AccessToken: "a"})
	assert.Equal(t, "read", o.Scope)
}

func TestARandomTokenIsNotGuessable(t *testing.T) {
	seen := map[string]bool{}
	for range 100 {
		tok := randomToken()
		assert.Len(t, tok, 43) // 32 bytes, base64url, unpadded
		assert.False(t, seen[tok])
		assert.False(t, strings.ContainsAny(tok, "+/="))
		seen[tok] = true
	}
}
