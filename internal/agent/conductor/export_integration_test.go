//go:build integration

package conductor

import (
	"context"
	"io"
	"log/slog"
	"time"

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

// DelegateForTest delegates for the conversation sessionID as the turn turnID would, with no
// token: what a host turn's podium_delegate does once its token and menu have been checked.
func (c *Conductor) DelegateForTest(
	ctx context.Context, src Source, sessionID, turnID, ref, playbook, instruction string, opts DelegateOptions,
) (store.Delegation, error) {
	return c.delegate(ctx, turnGrant{
		turnID: turnID, sessionID: sessionID, ref: ref, src: src, playbooks: []string{playbook},
	}, playbook, instruction, opts)
}

// SummariseNowForTest runs the summary job as if every conversation had been quiet long enough.
func (c *Conductor) SummariseNowForTest(ctx context.Context) {
	c.chat.mu.Lock()
	for _, cv := range c.chat.byID {
		cv.lastUsed = cv.lastUsed.Add(-summaryIdle - time.Minute)
	}
	c.chat.mu.Unlock()
	c.summarise(ctx)
}

// WithSummaryForTest is what a turn of sessionID would be handed for entries.
func (c *Conductor) WithSummaryForTest(ctx context.Context, sessionID string, entries []BriefEntry) []BriefEntry {
	return c.withSummary(ctx, sessionID, entries)
}
