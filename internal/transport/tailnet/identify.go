package tailnet

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"tailscale.com/client/tailscale/apitype"

	"github.com/podium-ade/podium/internal/transport"
)

// upsertTimeout bounds the one database write Identify can make. It is off the request's own
// deadline on purpose: recording a user must not be able to hang a request.
const upsertTimeout = 3 * time.Second

// whoIsFunc is the single Tailscale call this transport makes per request. Both modes supply
// one — tsnet from its embedded backend, host mode from the machine's tailscaled.
type whoIsFunc func(ctx context.Context, remoteAddr string) (*apitype.WhoIsResponse, error)

// identifier is the half of the transport that names callers. Both Listener and HostListener
// embed it, so the tag policy and the user bookkeeping exist exactly once.
type identifier struct {
	who    whoIsFunc
	opts   IdentityOptions
	users  UserStore
	logger *slog.Logger

	mu   sync.Mutex
	seen map[string]struct{}
}

// Identify asks Tailscale who opened this connection. There is no credential in the request:
// the answer comes from the WireGuard peer the packets arrived from, which is why the UI needs
// no login page and the CLI needs no token.
func (i *identifier) Identify(r *http.Request) (transport.Identity, error) {
	who, err := i.who(r.Context(), r.RemoteAddr)
	if err != nil {
		return transport.Identity{}, fmt.Errorf("tailnet: whois %s: %w: %w",
			r.RemoteAddr, err, transport.ErrUnauthenticated)
	}
	id, err := classify(who, r.RemoteAddr, i.opts)
	if err != nil {
		return transport.Identity{}, err
	}
	if id.Kind == transport.KindUser {
		i.recordUser(r.Context(), id)
	}
	return id, nil
}

// recordUser writes a users row the first time this process sees a login. It is best effort:
// a database that cannot record who is visiting must not stop them from visiting.
func (i *identifier) recordUser(ctx context.Context, id transport.Identity) {
	if i.users == nil {
		return
	}
	i.mu.Lock()
	if i.seen == nil {
		i.seen = make(map[string]struct{})
	}
	if _, ok := i.seen[id.Login]; ok {
		i.mu.Unlock()
		return
	}
	i.seen[id.Login] = struct{}{}
	i.mu.Unlock()

	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), upsertTimeout)
	defer cancel()
	if err := i.users.UpsertUser(writeCtx, id.Login, id.DisplayName); err != nil {
		i.logger.WarnContext(writeCtx, "recording tailnet user failed", "login", id.Login, "error", err)
		i.mu.Lock()
		delete(i.seen, id.Login)
		i.mu.Unlock()
		return
	}
	i.logger.InfoContext(writeCtx, "tailnet user seen", "login", id.Login, "display_name", id.DisplayName)
}
