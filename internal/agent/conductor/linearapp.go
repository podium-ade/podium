package conductor

// A run that acts as Podium — a system routine's, which nobody in Podium is behind — reaches
// Linear as the Linear app, not as a person (docs/agent.md, "Runs as"). Its Linear MCP server
// is the same server a person connects (mcp.LinearURL, named mcp.LinearName), signed in with
// the app's own actor=app access token, so what it writes in Linear is the app's. Anything else
// a turn names, and every run a person is behind, is resolved exactly as before.
//
// The token is the one the conductor's Linear app already holds (Options.LinearAppToken),
// minted or refreshed when the turn starts. It reaches the runtime as every MCP credential
// does: a host turn's environment, or, for a task container, a Podium secret the task spec
// names (mcp.LinearAppTokenSecret). It is never in a brief, a turn row or a log.

import (
	"context"
	"crypto/sha256"
	"errors"
	"slices"

	"github.com/podium-ade/podium/internal/agent/mcp"
	"github.com/podium-ade/podium/internal/agent/store"
)

// mcpReasonNoLinearApp is why a run as Podium has no Linear, as the agent is told it.
const mcpReasonNoLinearApp = "this run acts as Podium, which reaches Linear as the Podium Linear app, " +
	"and that app is not connected to Podium or Linear revoked it; an admin can connect it under " +
	"Organization settings → Connections"

// mcpReasonLinearAppUnstored is a token that could not be handed to the task container.
const mcpReasonLinearAppUnstored = "this run acts as Podium, and the Podium Linear app's token could not be " +
	"handed to it; an operator should check the conductor's logs"

// runsAsPodium reports whether a run acts as Podium: no person is behind it and it is a system
// routine's run. A person's turn, a personal routine's run, and every other turn are not.
func (c *Conductor) runsAsPodium(ctx context.Context, login string, ref *RoutineRef) bool {
	if turnOwner(login) != "" || ref == nil || ref.RoutineID == "" || c.store == nil {
		return false
	}
	r, err := c.store.GetRoutine(ctx, ref.RoutineID)
	return err == nil && r.System()
}

// linearAccess is where a turn's Linear MCP server comes from.
type linearAccess struct {
	// mode is the identity policy's choice for Linear: store.IdentityApp (always the app's),
	// store.IdentityPersonThenApp (the person's own, else the app's) or
	// store.IdentityPersonOnly (the person's own, else none). A run as Podium is always
	// store.IdentityApp.
	mode string
	// fromLinear is a turn that came from Linear. Its assistant has Linear whether or not it
	// names it: whoever wrote there is in that workspace, and the turn is about an issue.
	fromLinear bool
}

// linearAccessOf is where a turn's Linear comes from: the app's for a run as Podium, and the
// identity policy's choice otherwise. kind is the surface the turn came from.
func (c *Conductor) linearAccessOf(ctx context.Context, login string, ref *RoutineRef, kind string) linearAccess {
	a := linearAccess{mode: c.identityPolicy(ctx).Linear, fromLinear: kind == SourceLinear}
	if c.runsAsPodium(ctx, login, ref) {
		a.mode = store.IdentityApp
	}
	return a
}

// appMayStandIn is whether the app's Linear may stand in for a person's they cannot use. Not when they, or an operator, turned Linear off: that was a choice.
func appMayStandIn(reason string) bool {
	return reason != mcpReasonPersonOff && reason != mcpReasonOff
}

// routineRefOf is the routine run a turn this conductor is running belongs to, or nil.
func (c *Conductor) routineRefOf(turnID string) *RoutineRef {
	c.mu.Lock()
	defer c.mu.Unlock()
	rt := c.routineTurns[turnID]
	if rt == nil {
		return nil
	}
	ref := rt.ref
	return &ref
}

// withLinearApp takes the Linear server out of names for a run as Podium, so the registry is
// not asked for it, and reports whether it was there.
func withLinearApp(names []string) ([]string, bool) {
	if !slices.Contains(names, mcp.LinearName) {
		return names, false
	}
	return slices.DeleteFunc(slices.Clone(names), func(n string) bool { return n == mcp.LinearName }), true
}

// linearAppServer is the Linear MCP server signed in as the app, for a run as Podium, or why
// the run has none. A task container receives the token as a secret, so for one (onHost false)
// the secret is brought up to date here; a host turn reads the token when it starts the
// runtime (mcpToken). Neither failure fails the run: it runs without Linear, and is told so.
func (c *Conductor) linearAppServer(ctx context.Context, onHost bool) (mcp.Server, string) {
	if c.linearAppToken == nil {
		return mcp.Server{}, mcpReasonNoLinearApp
	}
	token, err := c.linearAppToken(ctx)
	if err != nil || token == "" {
		c.logger.WarnContext(ctx, "a run as Podium gets no Linear: the Linear app has no token", "error", err)
		return mcp.Server{}, mcpReasonNoLinearApp
	}
	if !onHost {
		if err := c.storeLinearAppToken(ctx, token); err != nil {
			c.logger.ErrorContext(ctx, "a run as Podium gets no Linear: storing the app's token for its task failed",
				"error", err)
			return mcp.Server{}, mcpReasonLinearAppUnstored
		}
	}
	return mcp.LinearAppServer(), ""
}

// storeLinearAppToken puts token in mcp.LinearAppTokenSecret, unless this process already put
// that token there. The token changes when the app's expires, so most runs write nothing.
func (c *Conductor) storeLinearAppToken(ctx context.Context, token string) error {
	if c.podium == nil {
		return errors.New("this conductor has no control plane to keep secrets in")
	}
	sum := sha256.Sum256([]byte(token))
	c.mu.Lock()
	same := c.linearAppStored == sum
	c.mu.Unlock()
	if same {
		return nil
	}
	if _, err := c.podium.SetSecret(ctx, mcp.LinearAppTokenSecret, []byte(token)); err != nil {
		return err
	}
	c.mu.Lock()
	c.linearAppStored = sum
	c.mu.Unlock()
	return nil
}
