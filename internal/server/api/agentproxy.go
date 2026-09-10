package api

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"

	"connectrpc.com/connect"

	"github.com/alvaroibarguen/podium/internal/transport"
)

// AgentLoginHeader is what the proxy tells the conductor about the human on the other end.
//
// It is a plain header, not a signed assertion. The conductor trusts it only because the
// bearer proves the request came through this server, which is the only party that knows
// the token and the only one that authenticated the caller. If the conductor is ever
// exposed beyond loopback or the compose network, this has to become a signed assertion —
// docs/security.md says so.
const AgentLoginHeader = "X-Podium-Login"

// localLogin is the login reported for the local transport, which has no per-user identity
// at all. It is the same word WhoAmI answers there.
const localLogin = "local"

// agentDialTimeout and agentResponseHeaderTimeout bound the hop to the conductor. The
// conductor is on this host; a dial that takes a second is a dial that is not going to work.
const (
	agentDialTimeout           = 2 * time.Second
	agentResponseHeaderTimeout = 30 * time.Second
	agentTLSHandshakeTimeout   = 5 * time.Second
	agentIdleConnTimeout       = 90 * time.Second
	agentExpectContinueTimeout = time.Second
	agentMaxIdleConnsPerHost   = 4
)

// NewAgentProxy reverse-proxies the conductor's Connect service.
//
// Mount it behind transport.WithIdentity on /podium.agent.v1.AgentService/ only. Two things
// happen on every request and both matter: any client-supplied Authorization and
// X-Podium-Login are deleted, and the server's own pair is set. The browser's credential is
// the dev token, which the conductor must never see; the login is the server's word about
// who is calling, which a client must never be able to write.
//
// The conductor's /healthz, /readyz and /metrics are deliberately not proxied. They are its
// own operational surface, they are unauthenticated on its listener, and publishing them
// through an authenticated origin would put a second, differently-shaped health story in
// front of an operator.
func NewAgentProxy(agentURL, agentToken string, logger *slog.Logger) (http.Handler, error) {
	if logger == nil {
		logger = slog.Default()
	}
	target, err := url.Parse(agentURL)
	if err != nil {
		return nil, err
	}

	// Rewrite rather than the older Director: SetURL joins the paths the way a prefix mount
	// needs, and the whole outgoing header set is visible in one place.
	proxy := &httputil.ReverseProxy{Rewrite: func(pr *httputil.ProxyRequest) {
		pr.SetURL(target)
		pr.SetXForwarded()
		pr.Out.Header.Del("Authorization")
		pr.Out.Header.Del(AgentLoginHeader)
		pr.Out.Header.Set("Authorization", "Bearer "+agentToken)
		pr.Out.Header.Set(AgentLoginHeader, agentLogin(pr.In.Context()))
	}}
	// -1 flushes every write immediately, which is what a server-streaming Connect call
	// needs: step 21's StreamChat must arrive frame by frame, not buffered to completion.
	proxy.FlushInterval = -1
	proxy.Transport = &http.Transport{
		DialContext:           (&net.Dialer{Timeout: agentDialTimeout}).DialContext,
		ResponseHeaderTimeout: agentResponseHeaderTimeout,
		TLSHandshakeTimeout:   agentTLSHandshakeTimeout,
		IdleConnTimeout:       agentIdleConnTimeout,
		ExpectContinueTimeout: agentExpectContinueTimeout,
		MaxIdleConnsPerHost:   agentMaxIdleConnsPerHost,
		// The conductor's Connect handler negotiates its own compression with the client.
		// Letting this hop add gzip of its own would re-encode a body the client already
		// asked for in a particular encoding.
		DisableCompression: true,
	}
	proxy.ErrorLog = slog.NewLogLogger(logger.Handler(), slog.LevelWarn)
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		logger.WarnContext(r.Context(), "the conductor is not reachable",
			"agent_url", agentURL, "procedure", r.URL.Path, "error", err)
		writeConnectUnavailable(w, r)
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A node has no business in the agent API. It is refused here rather than at the
		// conductor because the conductor cannot tell a node from a human: everything that
		// arrives there carries the server's bearer.
		if id, ok := transport.From(r.Context()); ok && id.Kind == transport.KindNode {
			http.Error(w, "forbidden: the agent API is for operators", http.StatusForbidden)
			return
		}
		proxy.ServeHTTP(w, r)
	}), nil
}

// agentLogin is who the identity middleware says is calling, in the form the conductor
// records. The local transport has no per-user identity, so every caller there is "dev".
func agentLogin(ctx context.Context) string {
	id, ok := transport.From(ctx)
	if !ok {
		return "unknown"
	}
	if id.Kind == transport.KindLocalToken || id.Login == "" {
		return localLogin
	}
	return id.Login
}

// writeConnectUnavailable answers a dial failure in the Connect protocol's own error shape
// rather than as a bare 502. The UI's client then hands the page a
// ConnectError{code: Unavailable} it can render as "the conductor is down", instead of a
// parse error on an HTML or plain-text body.
func writeConnectUnavailable(w http.ResponseWriter, r *http.Request) {
	err := connect.NewError(connect.CodeUnavailable, errors.New("podium-agent is not reachable"))
	// connect.ErrorWriter exists for exactly this: writing a Connect error from a handler
	// that is not a Connect handler. It picks the unary or the streaming envelope from the
	// request itself, so a streaming call gets the shape its client is parsing.
	writer := connect.NewErrorWriter()
	if writeErr := writer.Write(w, r, err); writeErr != nil {
		http.Error(w, "podium-agent is not reachable", http.StatusServiceUnavailable)
	}
}
