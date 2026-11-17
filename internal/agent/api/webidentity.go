package api

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/podium-ade/podium/internal/agent/store"
)

// webIdentityTTL is how long a login is remembered as recorded before the next request
// writes its web identity again. The write is what moves verified_at, so this is also how
// stale "last seen signing in" may be; an hour is far below anything that reads it.
const webIdentityTTL = time.Hour

// WebIdentityStore is what RecordWebIdentities writes to.
type WebIdentityStore interface {
	UpsertIdentity(ctx context.Context, id store.Identity) error
}

// RecordWebIdentities records every login podium-server proxies a call for as a web
// identity: the row that means "this person is in Podium". A Slack email or a GitHub
// account is only ever matched to a login that has one, so a person becomes resolvable on
// other surfaces by signing in here once.
//
// It must sit INSIDE RequireBearer: the login it trusts is the one RequireBearer took from
// the proxy's header. The dev token is skipped — it is a shared operator credential, not a
// person — as are a call that carried no login and the transport's own non-person names
// (mcpPersonalOwner's list).
//
// A failed write is logged and the request goes on: an identity that is recorded on the next
// call is a much smaller problem than a UI that stops working when the table is unhappy.
func RecordWebIdentities(st WebIdentityStore, logger *slog.Logger, next http.Handler) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	var (
		mu   sync.Mutex
		seen = map[string]time.Time{}
	)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		login := Login(ctx)
		if st != nil && mcpPersonalOwner(login) != "" && !SeesAll(ctx) {
			now := time.Now()
			mu.Lock()
			fresh := now.Sub(seen[login]) < webIdentityTTL
			if !fresh {
				seen[login] = now
			}
			mu.Unlock()
			if !fresh {
				if err := st.UpsertIdentity(ctx, store.Identity{
					Login: login, Provider: store.ProviderWeb, ExternalID: login, VerifiedAt: now.UTC(),
				}); err != nil {
					mu.Lock()
					delete(seen, login)
					mu.Unlock()
					logger.WarnContext(ctx, "recording a web identity failed", "login", login, "error", err)
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}
