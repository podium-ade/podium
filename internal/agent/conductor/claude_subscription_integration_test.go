//go:build integration

package conductor_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/agent/conductor"
	"github.com/podium-ade/podium/internal/agent/profiles"
)

// A Claude plan is one person's: only a turn that spends the sign-in of the person who asked is
// answered by Claude Code on a subscription. A turn that spends nobody's own credential never
// is, even on a database that still holds a Claude sign-in for everyone from before those went.
func TestOnlyAPersonsOwnClaudeSignInIsSpentByClaudeCode(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	require.NoError(t, st.PutSetting(ctx, profiles.ProviderSettingKey(profiles.ProviderAnthropic, "alice"),
		map[string]string{"auth_kind": "oauth"}))
	require.NoError(t, st.PutSetting(ctx, profiles.ProviderSettingKey(profiles.ProviderAnthropic, "bob"),
		map[string]string{"auth_kind": "api_key"}))
	require.NoError(t, st.PutSetting(ctx, profiles.ClaudeSubscriptionSettingKey,
		map[string]string{"auth_kind": "oauth"}))

	assert.True(t, conductor.ClaudeAgentTurnForTest(st, "alice"), "her own sign-in")
	assert.False(t, conductor.ClaudeAgentTurnForTest(st, "bob"), "his own credential is an API key")
	assert.False(t, conductor.ClaudeAgentTurnForTest(st, "carol"), "she has no credential of her own")
	assert.False(t, conductor.ClaudeAgentTurnForTest(st, ""),
		"a turn on the company's credential never spends a Claude plan")
}
