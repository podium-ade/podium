//go:build integration

package conductor

import (
	"context"
	"io"
	"log/slog"

	"github.com/podium-ade/podium/internal/agent/profiles"
	"github.com/podium-ade/podium/internal/agent/store"
)

// MintCapabilityForTest and DropGitCapabilityForTest reach the capability lifecycle from the
// external test package, which owns the database these tests need.
func (c *Conductor) MintCapabilityForTest(turnID string, scope GitScope) (string, error) {
	return c.mintCapability(turnID, scope)
}

func (c *Conductor) DropGitCapabilityForTest(ctx context.Context, turnID string, playbook profiles.Playbook) {
	c.dropGitCapability(ctx, turnID, playbook)
}

// PlacedForTest is where a turn of playbook runs, against st's saved default.
func PlacedForTest(ctx context.Context, st *store.Store, playbook profiles.Playbook) profiles.Playbook {
	c := &Conductor{store: st, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	return c.placed(ctx, playbook)
}
