package conductor

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/agent/profiles"
	"github.com/podium-ade/podium/pkg/spec"
)

// Exactly one model credential goes on a turn, and it is the one the turn's backend spends.
// A Grok turn that also carried the Anthropic key would be handing one company's credential
// to a container that is about to talk to another.
func TestATurnIsHandedOnlyItsOwnBackendsCredential(t *testing.T) {
	tests := []struct {
		agent      string
		wantSecret string
		wantEnv    string
		notSecret  string
	}{
		{profiles.AgentClaude, profiles.AnthropicKeySecret, profiles.AnthropicKeyEnv, profiles.XAIKeySecret},
		{profiles.AgentGrok, profiles.XAIKeySecret, profiles.XAIKeyEnv, profiles.AnthropicKeySecret},
	}
	for _, tc := range tests {
		t.Run(tc.agent, func(t *testing.T) {
			c := &Conductor{}
			refs := c.reservedSecrets(tc.agent, nil)
			require.Len(t, refs, 1, "no memory on this host, so the model credential is the only one")
			assert.Equal(t, tc.wantSecret, refs[0].Name)
			assert.Equal(t, tc.wantEnv, refs[0].Key)
			assert.Equal(t, spec.SecretTargetEnv, refs[0].Target)
			for _, r := range refs {
				assert.NotEqual(t, tc.notSecret, r.Name)
			}
		})
	}
}

// The refresh token of a subscription sign-in never leaves the conductor's host: it is not a
// Podium secret and no turn is ever handed it.
func TestNoTurnIsEverHandedARefreshToken(t *testing.T) {
	c := &Conductor{memory: &BriefMemory{MCPURL: "http://x/mcp", APIKeyEnv: profiles.MemoryKeyEnv}}
	for _, agent := range []string{profiles.AgentClaude, profiles.AgentGrok} {
		for _, ref := range c.reservedSecrets(agent, nil) {
			assert.NotEqual(t, profiles.XAIRefreshSecret, ref.Name)
		}
	}
}

func TestMemoryIsAttachedOnTopOfWhicheverCredential(t *testing.T) {
	c := &Conductor{memory: &BriefMemory{MCPURL: "http://x/mcp", APIKeyEnv: profiles.MemoryKeyEnv}}
	refs := c.reservedSecrets(profiles.AgentGrok, nil)
	require.Len(t, refs, 2)
	assert.Equal(t, profiles.XAIKeySecret, refs[0].Name)
	assert.Equal(t, profiles.MemoryKeySecret, refs[1].Name)
}

// Every turn carries a provider block now: the harness is told `provider/model` and cannot
// run without one. What changes between backends is which provider and which credential.
func TestEveryTurnNamesItsProviderAndCredential(t *testing.T) {
	c := &Conductor{xaiBaseURL: "https://api.x.ai"}

	claude := c.providerFor(profiles.AgentClaude)
	require.NotNil(t, claude)
	assert.Equal(t, profiles.ProviderAnthropic, claude.ID)
	assert.Equal(t, "ANTHROPIC_API_KEY", claude.APIKeyEnv)
	assert.Empty(t, claude.BaseURL, "no endpoint opinion means the harness's own default")

	grok := c.providerFor(profiles.AgentGrok)
	require.NotNil(t, grok)
	assert.Equal(t, profiles.ProviderXAI, grok.ID)
	// The NAME of a secret, never a value: a brief is an environment variable on a task
	// spec and is readable by anything that can read the spec.
	assert.Equal(t, "XAI_API_KEY", grok.APIKeyEnv)
	// With the version, because the harness appends the endpoint to this and nothing
	// else: a bare host sends the turn to https://api.x.ai/responses, which is a 404.
	assert.Equal(t, "https://api.x.ai/v1", grok.BaseURL)
}

// An agent id nobody configured still produces a runnable turn rather than a brief the
// runtime will refuse: it falls back to the default backend.
func TestAnUnknownBackendFallsBackRatherThanEmittingNothing(t *testing.T) {
	got := (&Conductor{}).providerFor("gemini")
	require.NotNil(t, got)
	assert.Equal(t, profiles.ProviderAnthropic, got.ID)
}

// The credential follows the OVERRIDE, and so does the provider block.
func TestAnOverrideChangesTheProviderBlock(t *testing.T) {
	c := &Conductor{xaiBaseURL: "https://api.x.ai"}
	p := &profiles.Profile{Agent: profiles.AgentClaude, Model: "claude-opus-5"}

	base := c.providerFor(p.Resolve(profiles.Playbook{}, profiles.Override{}).Agent)
	assert.Equal(t, profiles.ProviderAnthropic, base.ID)

	moved := c.providerFor(p.Resolve(profiles.Playbook{}, profiles.Override{Model: "grok-4.6"}).Agent)
	assert.Equal(t, profiles.ProviderXAI, moved.ID)
	assert.Equal(t, "XAI_API_KEY", moved.APIKeyEnv)
}

// The derived names must agree with the constants the rest of the code and the docs use.
func TestDerivedCredentialNamesMatchTheConstants(t *testing.T) {
	assert.Equal(t, profiles.AnthropicKeySecret, profiles.SecretFor(profiles.ProviderAnthropic))
	assert.Equal(t, profiles.AnthropicKeyEnv, profiles.KeyEnvFor(profiles.ProviderAnthropic))
	assert.Equal(t, profiles.XAIKeySecret, profiles.SecretFor(profiles.ProviderXAI))
	assert.Equal(t, profiles.XAIKeyEnv, profiles.KeyEnvFor(profiles.ProviderXAI))
}
