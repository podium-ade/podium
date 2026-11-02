package api

import (
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path"
	"strings"

	"github.com/podium-ade/podium/internal/proto/podium/agent/v1/agentv1connect"
)

// TurnSurfacePrefix is where this server serves the conductor's turn surface: under a path of
// its own, apart from the UI and the operator APIs, so it can be told apart — routed, logged,
// rate limited or firewalled — from everything else this server answers. A sandbox is given
// PODIUM_PUBLIC_URL + TurnSurfacePrefix as PODIUM_AGENT_SANDBOX_URL.
const TurnSurfacePrefix = "/turn-surface"

// The conductor's turn surface: what a running task calls back to. These are its HTTP
// resources (internal/agent/api.NewTurnREST); every request carries the TURN's credential,
// minted for it and checked by the conductor, so none of them is behind this server's sign-in.
// Nothing else under TurnSurfacePrefix is forwarded. The server does not import the
// conductor's packages; a test holds these equal to its routes.
var turnSurfaceRoutes = []string{
	"/v1/delegations",
	"/v1/delegations/",
	"/v1/git-tokens",
	"/v1/mcp/pull-request",
}

// legacyTurnSurfaceRoutes are the same calls as Connect procedures, which runtimes built
// before the resources existed still make — and the images built on them, in a cloud
// sandbox, until they are rebuilt. Deprecated: drop them once no such image is in use.
var legacyTurnSurfaceRoutes = []string{
	"/" + agentv1connect.TurnServiceName + "/",
	"/" + agentv1connect.GitCredentialServiceName + "/",
	"/mcp/pull-request",
}

func isTurnSurfaceRoute(p string) bool {
	// A path with dot segments or doubled slashes means something else once cleaned, and
	// the allowlist is checked against what it says, not what it would become.
	if path.Clean(p) != p {
		return false
	}
	for _, routes := range [][]string{turnSurfaceRoutes, legacyTurnSurfaceRoutes} {
		for _, r := range routes {
			if strings.HasSuffix(r, "/") && strings.HasPrefix(p, r) || p == r {
				return true
			}
		}
	}
	return false
}

// NewTurnSurfaceProxy reverse-proxies the conductor's turn surface, so a cloud sandbox reaches
// it at this server's public address under TurnSurfacePrefix
// (PODIUM_AGENT_SANDBOX_URL=PODIUM_PUBLIC_URL/turn-surface) instead of through a tunnel of its
// own to PODIUM_AGENT_SURFACE_LISTEN.
//
// It is the opposite of NewAgentProxy in the one way that matters: the request's own
// Authorization — the turn's credential — goes through untouched, and the server's bearer is
// never added, so nothing that arrives here can act as the server. The headers the conductor
// reads as the server's word about a person are stripped. Mount it on TurnSurfacePrefix + "/":
// the prefix is removed, and a path that is not one of the surface's routes is 404 here, never
// forwarded — the conductor's operator API is on the same listener.
func NewTurnSurfaceProxy(agentURL string, logger *slog.Logger) (http.Handler, error) {
	if logger == nil {
		logger = slog.Default()
	}
	target, err := url.Parse(agentURL)
	if err != nil {
		return nil, err
	}
	proxy := &httputil.ReverseProxy{Rewrite: func(pr *httputil.ProxyRequest) {
		pr.SetURL(target)
		pr.SetXForwarded()
		pr.Out.Header.Del(AgentLoginHeader)
		pr.Out.Header.Del(AgentScopeHeader)
		pr.Out.Header.Del(AgentInfraHeader)
	}}
	// Streaming MCP and Connect answers go out as they are written.
	proxy.FlushInterval = -1
	proxy.Transport = &http.Transport{
		DialContext:           (&net.Dialer{Timeout: agentDialTimeout}).DialContext,
		ResponseHeaderTimeout: agentResponseHeaderTimeout,
		TLSHandshakeTimeout:   agentTLSHandshakeTimeout,
		IdleConnTimeout:       agentIdleConnTimeout,
		ExpectContinueTimeout: agentExpectContinueTimeout,
		MaxIdleConnsPerHost:   agentMaxIdleConnsPerHost,
		DisableCompression:    true,
	}
	proxy.ErrorLog = slog.NewLogLogger(logger.Handler(), slog.LevelWarn)
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		logger.WarnContext(r.Context(), "the conductor's turn surface is not reachable",
			"agent_url", agentURL, "path", r.URL.Path, "error", err)
		writeConnectUnavailable(w, r)
	}
	return http.StripPrefix(TurnSurfacePrefix, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isTurnSurfaceRoute(r.URL.Path) {
			http.NotFound(w, r)
			return
		}
		proxy.ServeHTTP(w, r)
	})), nil
}
