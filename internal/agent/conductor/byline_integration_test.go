//go:build integration

package conductor_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/agent/conductor"
	"github.com/podium-ade/podium/internal/agent/profiles"
	"github.com/podium-ade/podium/internal/agent/store"
)

// The byline names a person as the members list does, and a subscription by the plan people
// buy, read from how the credential was stored: a ChatGPT or Grok sign-in replaces the key it
// sits in, for a person and for the workspace alike.
func TestTheBylineNamesThePersonAndThePlan(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	const login = "alvaro@podium.test"
	require.NoError(t, st.UpsertIdentity(ctx, store.Identity{
		Login: login, Provider: store.ProviderWeb, ExternalID: login,
		DisplayName: "Alvaro Ibarguen", VerifiedAt: time.Now(),
	}))
	for _, key := range []string{
		profiles.ProviderSettingKey(profiles.ProviderOpenAI, login),
		profiles.ProviderSettingKey(profiles.ProviderXAI, ""),
	} {
		require.NoError(t, st.PutSetting(ctx, key, map[string]string{"auth_kind": "oauth"}))
	}

	assert.Equal(t, conductor.Byline{Model: "GPT-6.1 Sol", Credential: "Alvaro Ibarguen's ChatGPT plan"},
		conductor.BylineForTest(ctx, st, profiles.ProviderOpenAI, "gpt-6.1-sol", login, false))
	assert.Equal(t, conductor.Byline{Model: "GPT-6.1 Sol", Credential: "Workspace API key"},
		conductor.BylineForTest(ctx, st, profiles.ProviderOpenAI, "gpt-6.1-sol", "", false),
		"a person's sign-in is not the workspace's")
	assert.Equal(t, conductor.Byline{Model: "Grok 4.7", Credential: "Workspace Grok plan"},
		conductor.BylineForTest(ctx, st, profiles.ProviderXAI, "grok-4.7", "", false))
	assert.Equal(t, conductor.Byline{Model: "Claude Opus 5.5", Credential: "Alvaro Ibarguen's Claude plan"},
		conductor.BylineForTest(ctx, st, profiles.ProviderAnthropic, "claude-opus-5-5", login, true))
	assert.Equal(t, conductor.Byline{Model: "Claude Opus 5.5", Credential: "nobody's API key"},
		conductor.BylineForTest(ctx, st, profiles.ProviderAnthropic, "claude-opus-5-5", "nobody@podium.test", false),
		"somebody with no name on record is named by their login")
}
