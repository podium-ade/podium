package conductor

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alvaroibarguen/podium/internal/agent/profiles"
	"github.com/alvaroibarguen/podium/pkg/spec"
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
			refs := c.reservedSecrets(tc.agent)
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
		for _, ref := range c.reservedSecrets(agent) {
			assert.NotEqual(t, profiles.XAIRefreshSecret, ref.Name)
		}
	}
}

func TestMemoryIsAttachedOnTopOfWhicheverCredential(t *testing.T) {
	c := &Conductor{memory: &BriefMemory{MCPURL: "http://x/mcp", APIKeyEnv: profiles.MemoryKeyEnv}}
	refs := c.reservedSecrets(profiles.AgentGrok)
	require.Len(t, refs, 2)
	assert.Equal(t, profiles.XAIKeySecret, refs[0].Name)
	assert.Equal(t, profiles.MemoryKeySecret, refs[1].Name)
}

// A Claude turn needs no provider block: the agent SDK's own defaults are already right, and
// a block naming them would be a second place to keep them correct.
func TestOnlyANonDefaultBackendCarriesAProviderBlock(t *testing.T) {
	c := &Conductor{xaiBaseURL: "https://api.x.ai"}
	assert.Nil(t, c.providerFor(profiles.AgentClaude))

	p := c.providerFor(profiles.AgentGrok)
	require.NotNil(t, p)
	assert.Equal(t, "https://api.x.ai", p.BaseURL)
	// The NAME of a secret, never a value: a brief is an environment variable on a task
	// spec and is readable by anything that can read the spec.
	assert.Equal(t, profiles.XAIKeyEnv, p.APIKeyEnv)
}

// The endpoint is configurable so an install behind an egress proxy can name one.
func TestTheProviderBlockFollowsTheConfiguredEndpoint(t *testing.T) {
	proxied := &Conductor{xaiBaseURL: "https://xai.proxy.internal"}
	assert.Equal(t, "https://xai.proxy.internal", proxied.providerFor(profiles.AgentGrok).BaseURL)
}

// The credential follows the OVERRIDE, not just the skill. A human who switches a Claude
// skill to Grok for one message must get the xAI credential on that turn — and must not get
// the Anthropic one.
func TestAnOverrideChangesWhichCredentialTheTurnGets(t *testing.T) {
	claudeSkill := profiles.Skill{Name: "general"}
	p := &profiles.Profile{Agent: profiles.AgentClaude, Model: "claude-opus-5"}

	assert.Equal(t, profiles.AnthropicKeySecret,
		(&Conductor{}).reservedSecrets(p.Resolve(claudeSkill, profiles.Override{}).Agent)[0].Name)

	grok := p.Resolve(claudeSkill, profiles.Override{Model: "grok-4.6"})
	refs := (&Conductor{}).reservedSecrets(grok.Agent)
	require.Len(t, refs, 1)
	assert.Equal(t, profiles.XAIKeySecret, refs[0].Name,
		"the override moved the turn to Grok, so the credential has to move with it")
}

// And so does the provider block: a Grok turn reached by override still needs the endpoint.
func TestAnOverrideChangesTheProviderBlock(t *testing.T) {
	c := &Conductor{xaiBaseURL: "https://api.x.ai"}
	p := &profiles.Profile{Agent: profiles.AgentClaude, Model: "claude-opus-5"}

	assert.Nil(t, c.providerFor(p.Resolve(profiles.Skill{}, profiles.Override{}).Agent))

	got := c.providerFor(p.Resolve(profiles.Skill{}, profiles.Override{Model: "grok-4.6"}).Agent)
	require.NotNil(t, got)
	assert.Equal(t, profiles.XAIKeyEnv, got.APIKeyEnv)
}
