package auth

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"

	"github.com/podium-ade/podium/internal/proto/podium/v1/podiumv1connect"
	"github.com/podium-ade/podium/internal/server/store"
	"github.com/podium-ade/podium/internal/transport"
)

// Layer wraps a transport.Listener so a Google session cookie (or bearer) becomes KindUser
// before the inner transport is asked. Listen is delegated. Ready/BaseURL are NOT promoted:
// server.unwrapTransport looks through Inner() so the tailnet probes still hit the real listener.
type Layer struct {
	inner transport.Listener
	store *store.Store
}

// Wrap returns inner unchanged when st is nil. Otherwise Identify tries a session first.
func Wrap(inner transport.Listener, st *store.Store) transport.Listener {
	if inner == nil || st == nil {
		return inner
	}
	return &Layer{inner: inner, store: st}
}

// Inner returns the wrapped listener. server.unwrapTransport uses this.
func (l *Layer) Inner() transport.Listener { return l.inner }

func (l *Layer) Listen(ctx context.Context) (net.Listener, error) {
	return l.inner.Listen(ctx)
}

func (l *Layer) Identify(r *http.Request) (transport.Identity, error) {
	if tok := sessionToken(r); tok != "" {
		if id, err := l.identifySession(r.Context(), r, tok); err == nil {
			return id, nil
		}
	}
	return l.inner.Identify(r)
}

func (l *Layer) identifySession(ctx context.Context, r *http.Request, tok string) (transport.Identity, error) {
	sess, err := l.store.GetSession(ctx, tok)
	if err != nil {
		return transport.Identity{}, err
	}
	user, err := l.store.GetUser(ctx, sess.Login)
	if err != nil {
		return transport.Identity{}, err
	}
	return sessionIdentity(r, user.Login, user.DisplayName), nil
}

// RestrictUnclaimed forbids KindUser from everything except WhoAmI and Claim until the
// instance has an owner, and afterwards requires the caller's domain to match. Nodes and
// the local token are not humans and are not gated — workers and the CLI still use them.
// A no-op when googleEnabled is false, so existing deployments do not change.
func RestrictUnclaimed(st *store.Store, googleEnabled bool, next http.Handler) http.Handler {
	if st == nil || !googleEnabled {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := transport.From(r.Context())
		if !ok || id.Kind != transport.KindUser {
			next.ServeHTTP(w, r)
			return
		}
		inst, err := st.GetInstance(r.Context())
		if errors.Is(err, store.ErrNotFound) {
			if unclaimedPath(r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}
			http.Error(w, "this instance has not been claimed", http.StatusForbidden)
			return
		}
		if err != nil {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		domain := store.EmailDomain(id.Login)
		if user, err := st.GetUser(r.Context(), id.Login); err == nil {
			domain = store.UserDomain(user)
		}
		if !strings.EqualFold(domain, inst.HostedDomain) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func unclaimedPath(path string) bool {
	switch path {
	case podiumv1connect.IdentityServiceWhoAmIProcedure, podiumv1connect.IdentityServiceClaimProcedure:
		return true
	default:
		return false
	}
}
