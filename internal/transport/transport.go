// Package transport is the seam between how a request reaches the server and who the server
// thinks made it. The dev implementation trusts a shared bearer token on loopback; the tailnet
// one (step 11) resolves a Tailscale WhoIs. Handlers never see either — they read an Identity
// out of the request context.
package transport

import (
	"context"
	"errors"
	"net"
	"net/http"
)

// IdentityKind says which of the three authentication paths produced an Identity.
type IdentityKind string

// The canonical identity kinds.
const (
	// KindUser is a human operator: a tailnet login, or a dev token in MVP-0.
	KindUser IdentityKind = "user"
	// KindNode is an enrolled node that has proved possession of its node key.
	KindNode IdentityKind = "node"
	// KindLocalToken is the shared bearer token of the local transport. It is trusted for
	// both operator and node calls; a node only becomes KindNode once Hello validates.
	KindLocalToken IdentityKind = "local_token"
)

// Identity is who the server believes is on the other end of a request.
type Identity struct {
	Kind IdentityKind
	// Login is the Tailscale login name of a user, the FQDN of a node device, or "dev".
	Login string
	// DisplayName is the human-readable name a tailnet user profile carries. Empty otherwise.
	DisplayName string
	// NodeTags are the ACL tags of the calling Tailscale device, empty under the local transport.
	NodeTags []string
	// NodeStableID is the calling Tailscale device's stable node ID. It is what binds a Podium
	// node identity to one device, so a stolen identity.json is useless on another machine.
	NodeStableID string
	RemoteAddr   string
}

// Listener binds the server's socket and names the caller behind each request.
type Listener interface {
	// Listen binds and returns the socket the HTTP server should accept on.
	Listen(ctx context.Context) (net.Listener, error)
	// Identify authenticates one request. It returns ErrUnauthenticated when the caller
	// presented no usable credential.
	Identify(r *http.Request) (Identity, error)
}

// ErrUnauthenticated is what Identify returns for a missing or wrong credential.
var ErrUnauthenticated = errors.New("transport: unauthenticated")

// ErrForbidden is what Identify returns for a caller it recognised and refuses anyway: a
// tailnet device carrying the wrong tag. Retrying with a credential cannot help, so the
// middleware answers 403 and does not offer a challenge.
var ErrForbidden = errors.New("transport: forbidden")

type contextKey struct{}

// NewContext returns ctx carrying id.
func NewContext(ctx context.Context, id Identity) context.Context {
	return context.WithValue(ctx, contextKey{}, id)
}

// From returns the Identity WithIdentity put in ctx. The second result is false when the
// handler was reached without going through the middleware.
func From(ctx context.Context) (Identity, bool) {
	id, ok := ctx.Value(contextKey{}).(Identity)
	return id, ok
}

// WithIdentity authenticates every request with l and puts the resulting Identity in the
// request context. Unauthenticated requests get 401 and never reach next. Mount it on the RPC
// mux only: /healthz, /readyz and /metrics are deliberately open.
func WithIdentity(l Listener, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := l.Identify(r)
		if err != nil {
			if errors.Is(err, ErrForbidden) {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "unauthenticated", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r.WithContext(NewContext(r.Context(), id)))
	})
}
