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

// A global Claude subscription with no global Anthropic key is enough for a chat-host turn of
// somebody with no credential of their own: Claude Code answers it on the subscription.
func TestAGlobalClaudeSubscriptionAloneLetsAChatTurnStart(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	sub := map[string]string{"claude-agent": "sk-ant-oat01-company"}

	assert.False(t, conductor.GlobalCredentialForTest(ctx, st, "anthropic", sub),
		"with no settings row the subscription is not known to be signed in")

	require.NoError(t, st.PutSetting(ctx, profiles.ClaudeSubscriptionSettingKey,
		map[string]string{"auth_kind": "oauth"}))
	assert.True(t, conductor.GlobalCredentialForTest(ctx, st, "anthropic", sub))
	assert.False(t, conductor.GlobalCredentialForTest(ctx, st, "anthropic", nil),
		"a row whose secret was removed is no credential")
	assert.True(t, conductor.GlobalCredentialForTest(ctx, st, "anthropic",
		map[string]string{"anthropic": "sk-ant-api03-key"}), "the global key still works on its own")
	assert.False(t, conductor.GlobalCredentialForTest(ctx, st, "xai", sub),
		"the Claude subscription is no other provider's credential")
}
