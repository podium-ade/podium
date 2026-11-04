//go:build integration

package conductor

import (
	"context"
	"errors"
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

// RecoverAtStartupForTest runs the startup recovery pass again, as it runs while the turn
// surface is already serving.
func (c *Conductor) RecoverAtStartupForTest(ctx context.Context) {
	c.recoverWork(ctx, true)
}

// GlobalCredentialForTest is whether a chat-host turn on provider with no credential of its
// person's own may start, on a conductor that can run Claude Code, against st's settings rows
// and these global credentials (provider name -> value).
func GlobalCredentialForTest(ctx context.Context, st *store.Store, provider string, creds map[string]string) bool {
	c := &Conductor{
		store:          st,
		chatHostClaude: true,
		logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		host: &HostRuntime{Credential: func(_ context.Context, p string) (string, error) {
			if v := creds[p]; v != "" {
				return v, nil
			}
			return "", errors.New("no credential is stored for " + p)
		}},
	}
	return c.hasGlobalCredential(ctx, &hostRun{provider: &BriefProvider{ID: provider}})
}

// DeliverDueForTest runs one pass of the delivery outbox worker and returns how many rows it
// claimed.
func (c *Conductor) DeliverDueForTest(ctx context.Context) int { return c.deliverDue(ctx) }

// TurnTranscriptForTest is the transcript a turn of sess at ref would be handed.
func (c *Conductor) TurnTranscriptForTest(ctx context.Context, src Source, ref string, sess store.Session) ([]BriefEntry, error) {
	return c.turnTranscript(ctx, src, ref, sess)
}
