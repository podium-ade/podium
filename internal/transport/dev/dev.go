// Package dev is the loopback transport: no Tailscale, no mTLS, one shared bearer token. It
// refuses to bind anything but a loopback address, because the token is the only thing between
// a caller and the whole API. PODIUM_DEV_ALLOW_UNSAFE_LISTEN waives that for a container,
// where loopback is the container's own and the published port is the boundary.
package dev

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"

	"github.com/alvaroibarguen/podium/internal/transport"
)

// DefaultListen is the address PODIUM_DEV_LISTEN falls back to.
const DefaultListen = "127.0.0.1:8080"

// Options configures the dev transport.
type Options struct {
	// Listen is a host:port that must resolve to a loopback address unless
	// AllowNonLoopback says otherwise.
	Listen string
	// Token is the shared secret callers present as `Authorization: Bearer <token>`.
	Token string
	// AllowNonLoopback lets Listen bind an address that is not loopback. It exists for a
	// container, where loopback is the container's own and the published port is the real
	// boundary; nothing in this process can tell that case from a routable interface on a
	// host, so the operator has to say which it is. It is PODIUM_DEV_ALLOW_UNSAFE_LISTEN
	// and it publishes the whole API to whoever can reach the address and holds one static
	// token.
	AllowNonLoopback bool
}

// Listener is the dev transport's transport.Listener.
type Listener struct {
	addr  string
	token string
}

// New validates the options and returns the listener. It fails rather than binding a
// non-loopback address: the dev token is a development convenience, not an authentication
// system, and exposing it on a routable interface would publish the whole API.
func New(opts Options) (*Listener, error) {
	addr := opts.Listen
	if addr == "" {
		addr = DefaultListen
	}
	if err := CheckListen(addr, opts.AllowNonLoopback); err != nil {
		return nil, err
	}
	if opts.Token == "" {
		return nil, errors.New("dev transport: PODIUM_DEV_TOKEN is required")
	}
	return &Listener{addr: addr, token: opts.Token}, nil
}

// UnsafeListenVar is the environment variable that waives the loopback rule. It is named
// here so the server's own configuration and its error messages spell it the same way.
const UnsafeListenVar = "PODIUM_DEV_ALLOW_UNSAFE_LISTEN"

// CheckListen reports whether addr can be served. The host must be unambiguously loopback,
// because the dev token is the only credential there is and a routable address publishes
// the whole API to whoever holds it.
//
// allowNonLoopback waives that. A container is the case it exists for: loopback there is
// the container's own, so a server bound to it is unreachable even from the compose network,
// and the boundary is the published port. Nothing this process can observe distinguishes
// that from a public interface on a host, so the operator declares it.
func CheckListen(addr string, allowNonLoopback bool) error {
	if addr == "" {
		addr = DefaultListen
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("dev transport: %q is not a host:port address: %w", addr, err)
	}
	if port == "" {
		return fmt.Errorf("dev transport: %q has no port", addr)
	}
	if allowNonLoopback {
		return nil
	}
	if host == "" {
		return fmt.Errorf("dev transport: refusing to listen on %q: it binds every interface, "+
			"and the dev transport must be loopback-only (use %s, or set %s=1 if this address is "+
			"reachable only from inside a container)", addr, DefaultListen, UnsafeListenVar)
	}
	if strings.EqualFold(host, "localhost") {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("dev transport: refusing to listen on %q: %q is not a loopback address "+
			"or literal IP (use %s)", addr, host, DefaultListen)
	}
	if !ip.IsLoopback() {
		return fmt.Errorf("dev transport: refusing to listen on %q: %s is not a loopback address, "+
			"and the dev transport trusts a static token (use %s, or set %s=1 if this address is "+
			"reachable only from inside a container)", addr, ip, DefaultListen, UnsafeListenVar)
	}
	return nil
}

// Listen binds the configured loopback address.
func (l *Listener) Listen(ctx context.Context) (net.Listener, error) {
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", l.addr)
	if err != nil {
		return nil, fmt.Errorf("dev transport: listen on %s: %w", l.addr, err)
	}
	return ln, nil
}

// Addr is the configured listen address, which is not the bound one when the port is 0.
func (l *Listener) Addr() string { return l.addr }

// Identify accepts `Authorization: Bearer <PODIUM_DEV_TOKEN>` and nothing else. Operators and
// nodes present the same token in dev; a node's Identity becomes transport.KindNode only after
// Hello proves possession of its node key.
func (l *Listener) Identify(r *http.Request) (transport.Identity, error) {
	const prefix = "Bearer "
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, prefix) {
		return transport.Identity{}, fmt.Errorf("dev transport: missing bearer token: %w", transport.ErrUnauthenticated)
	}
	presented := strings.TrimSpace(strings.TrimPrefix(h, prefix))
	if subtle.ConstantTimeCompare([]byte(presented), []byte(l.token)) != 1 {
		return transport.Identity{}, fmt.Errorf("dev transport: wrong bearer token: %w", transport.ErrUnauthenticated)
	}
	return transport.Identity{
		Kind:       transport.KindDevToken,
		Login:      "dev",
		RemoteAddr: r.RemoteAddr,
	}, nil
}
