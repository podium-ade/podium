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

// GitHubPersonaForTest is who a turn of playbook started by login commits as. Nil is the App.
func (c *Conductor) GitHubPersonaForTest(ctx context.Context, login string, playbook profiles.Playbook) (*BriefGit, error) {
	return c.githubPersona(ctx, login, playbook)
}

// MintTurnTokenForTest issues the token a turn of sessionID started by login would hold.
func (c *Conductor) MintTurnTokenForTest(sessionID, turnID, login string) (string, error) {
	return c.mintTurnToken(turnGrant{turnID: turnID, sessionID: sessionID, login: login})
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

// SharedConversationForTest is whether a turn of sess would spend the company's model key
// because its conversation is shared, against st's chat record.
func SharedConversationForTest(ctx context.Context, st *store.Store, sess store.Session) bool {
	c := &Conductor{store: st, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	return c.sharedConversation(ctx, sess)
}

// MintChatTurnTokenForTest mints a token for a turn of the chat ref, answered through src.
func (c *Conductor) MintChatTurnTokenForTest(src Source, sessionID, turnID, ref, login string) (string, error) {
	return c.mintTurnToken(turnGrant{turnID: turnID, sessionID: sessionID, ref: ref, src: src, login: login})
}

// NoteRoutineTurnForTest records turnID as the scheduled run ev carries, as runTurn does.
func (c *Conductor) NoteRoutineTurnForTest(ctx context.Context, turnID string, ev InboundEvent) {
	c.noteRoutineTurn(ctx, turnID, ev)
}

// PostForTest says out through src, as a turn would.
func (c *Conductor) PostForTest(ctx context.Context, src Source, ref string, out Outbound) string {
	return c.post(ctx, src, ref, out)
}

// FinishForTest shows a turn's outcome, as a turn would.
func (c *Conductor) FinishForTest(ctx context.Context, src Source, ref string, kind Reaction) {
	c.finish(ctx, src, ref, kind)
}

// RoutineMenuForTest is the delegation menu a turn for ev is shown.
func (c *Conductor) RoutineMenuForTest(ev InboundEvent) []DelegablePlaybook {
	return c.routineMenu(c.DelegablePlaybooks(), ev)
}

// ChatToolNamesForTest is the names of the podium server's tools the turn token holds is offered.
func (c *Conductor) ChatToolNamesForTest(ctx context.Context, token string) []string {
	var out []string
	for _, t := range c.chatDelegateToolsFor(ctx, token) {
		out = append(out, t.Name)
	}
	return out
}

// CheckRoutineDelegationsForTest is the routine caps a delegation from the turn token holds
// meets, before its playbook is looked at.
func (c *Conductor) CheckRoutineDelegationsForTest(ctx context.Context, token string) error {
	g, ok := c.grantFor(ctx, token)
	if !ok {
		return ErrNoTurn
	}
	return c.checkRoutineDelegations(ctx, g)
}
