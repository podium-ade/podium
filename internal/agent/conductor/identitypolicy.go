package conductor

import (
	"context"

	"github.com/podium-ade/podium/internal/agent/store"
)

// identityPolicy is the organization's identity policy (store.IdentityPolicy), read for each
// turn so an admin's change reaches the next one. One that cannot be read is the default,
// which refuses outsiders: a database hiccup must not let a stranger in.
func (c *Conductor) identityPolicy(ctx context.Context) store.IdentityPolicy {
	if c.store == nil {
		return store.DefaultIdentityPolicy
	}
	p, err := c.store.GetIdentityPolicy(ctx)
	if err != nil {
		c.logger.WarnContext(ctx, "the identity policy could not be read; the turn uses the default", "error", err)
		return store.DefaultIdentityPolicy
	}
	return p
}
