//go:build integration

package conductor

import (
	"context"

	"github.com/podium-ade/podium/internal/agent/profiles"
)

// MintCapabilityForTest and DropGitCapabilityForTest reach the capability lifecycle from the
// external test package, which owns the database these tests need.
func (c *Conductor) MintCapabilityForTest(turnID string, scope GitScope) (string, error) {
	return c.mintCapability(turnID, scope)
}

func (c *Conductor) DropGitCapabilityForTest(ctx context.Context, turnID string, playbook profiles.Playbook) {
	c.dropGitCapability(ctx, turnID, playbook)
}
