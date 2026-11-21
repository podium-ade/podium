package conductor

import (
	"context"

	"github.com/podium-ade/podium/internal/agent/chathost"
	"github.com/podium-ade/podium/internal/agent/profiles"
	"github.com/podium-ade/podium/internal/agent/store"
)

// credentialFor is what a turn on provider, spending owner's credential ("" the company's),
// is paid with: the same answer the code that hands the turn its credential reaches, written
// down so the usage screen can say it.
//
// claudeAgent is whether the turn runs Claude Code on the chat host (claudeAgentTurn): that,
// and only that, spends a Claude subscription. Every other Anthropic turn spends an API key
// whatever sign-ins exist, because credentialOwner already moved it to the company key.
// OpenAI and xAI spend a subscription when the stored credential is a sign-in, which is what
// their settings row records.
func (c *Conductor) credentialFor(provider, owner string, claudeAgent bool) store.Credential {
	key := profiles.ProviderSettingKey(provider, owner)
	if claudeAgent && owner == "" {
		key = profiles.ClaudeSubscriptionSettingKey
	}
	row := c.providerSetting(key)
	out := store.Credential{AuthKind: store.AuthAPIKey, Owner: owner, Account: row.Account}
	if out.Account == "" {
		out.Account = row.KeyHint
	}
	switch {
	case claudeAgent:
		out.AuthKind = store.AuthSubscription
	case provider != profiles.ProviderAnthropic && row.AuthKind == "oauth":
		out.AuthKind = store.AuthSubscription
	}
	return out
}

// credentialRow is the part of a provider's settings row (api.providerRow) that says what the
// credential is. It holds no secret.
type credentialRow struct {
	AuthKind string `json:"auth_kind"`
	Account  string `json:"account"`
	KeyHint  string `json:"key_hint"`
}

// providerSetting reads a provider's settings row. A missing row is the zero row: an API key
// nobody described, which is what an install configured only by environment has.
func (c *Conductor) providerSetting(key string) credentialRow {
	var row credentialRow
	if c.store == nil {
		return row
	}
	_ = c.store.GetSetting(context.Background(), key, &row)
	return row
}

// recordTurnCredential writes what paid for a turn. A failure costs the usage screen one
// attribution, never the turn, so it is logged and the turn goes on.
func (c *Conductor) recordTurnCredential(ctx context.Context, turnID string, cred store.Credential) {
	if c.store == nil {
		return
	}
	if err := c.store.SetTurnCredential(ctx, turnID, cred); err != nil {
		c.logger.WarnContext(ctx, "recording what paid for the turn failed", "turn_id", turnID, "error", err)
	}
}

// recordDelegationCredential is recordTurnCredential for a delegated task.
func (c *Conductor) recordDelegationCredential(ctx context.Context, delegationID string, cred store.Credential) {
	if c.store == nil {
		return
	}
	if err := c.store.SetDelegationCredential(ctx, delegationID, cred); err != nil {
		c.logger.WarnContext(ctx, "recording what paid for the delegated task failed",
			"delegation_id", delegationID, "error", err)
	}
}

// tokens is the accounting's token counts as the store keeps them. A runtime from before it
// reported tokens sends none, and its row keeps nulls rather than zeroes.
func (a *accounting) tokens() (store.Tokens, bool) {
	if a == nil || (a.InputTokens == nil && a.OutputTokens == nil && a.ReasoningTokens == nil &&
		a.CacheReadTokens == nil && a.CacheWriteTokens == nil) {
		return store.Tokens{}, false
	}
	return store.Tokens{
		Input: a.InputTokens, Output: a.OutputTokens, Reasoning: a.ReasoningTokens,
		CacheRead: a.CacheReadTokens, CacheWrite: a.CacheWriteTokens,
	}, true
}

// setTokens fills a chat-host turn's accounting from what opencode reported.
func (a *accounting) setTokens(t chathost.Tokens) {
	a.InputTokens, a.OutputTokens, a.ReasoningTokens = &t.Input, &t.Output, &t.Reasoning
	a.CacheReadTokens, a.CacheWriteTokens = &t.CacheRead, &t.CacheWrite
}

// storeTokens is a chat-host result's tokens as the store keeps them.
func storeTokens(t chathost.Tokens) store.Tokens {
	return store.Tokens{
		Input: &t.Input, Output: &t.Output, Reasoning: &t.Reasoning,
		CacheRead: &t.CacheRead, CacheWrite: &t.CacheWrite,
	}
}
