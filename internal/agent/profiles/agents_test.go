package profiles

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEveryBackendInTheCatalogueIsUsable(t *testing.T) {
	require.NotEmpty(t, Backends)
	seen := map[string]bool{}
	for _, b := range Backends {
		assert.NotEmpty(t, b.ID)
		assert.False(t, seen[b.ID], "two backends share the id %q", b.ID)
		seen[b.ID] = true
		assert.NotEmpty(t, b.DisplayName)
		assert.NotEmpty(t, b.Provider)
		assert.NotEmpty(t, b.Models, "%s offers no model, so nothing could run on it", b.ID)

		// The default has to be one of the models offered, or a profile that names the
		// backend and no model would be pointed at something the picker never shows.
		_, ok := b.FindModel(b.DefaultModel)
		assert.True(t, ok, "%s's default model %q is not one of its models", b.ID, b.DefaultModel)

		for _, m := range b.Models {
			assert.NotEmpty(t, m.ID)
			assert.NotEmpty(t, m.DisplayName)
			for _, e := range m.Efforts {
				assert.NoError(t, validEffort(e),
					"%s offers effort %q, which is not a level", m.ID, e)
			}
		}
	}
	_, ok := FindBackend(DefaultAgent)
	assert.True(t, ok, "DefaultAgent must name a backend in the catalogue")
}

func TestFindBackendTreatsEmptyAsTheDefault(t *testing.T) {
	b, ok := FindBackend("")
	require.True(t, ok)
	assert.Equal(t, DefaultAgent, b.ID)

	_, ok = FindBackend("gemini")
	assert.False(t, ok)
}

func TestValidateTriple(t *testing.T) {
	tests := []struct {
		name         string
		agent, model string
		effort       string
		wantErr      string
	}{
		{name: "nothing set at all"},
		{name: "a known triple", agent: AgentClaude, model: "claude-opus-5", effort: EffortMax},
		{name: "grok at xhigh", agent: AgentGrok, model: "grok-4.6", effort: EffortXHigh},
		{
			name: "an agent that does not exist", agent: "gemini",
			wantErr: `agent "gemini" is not one this conductor runs`,
		},
		{
			name: "a level nothing accepts", agent: AgentClaude, model: "claude-opus-5",
			effort: "ludicrous", wantErr: `effort "ludicrous" is not a level`,
		},
		{
			// max is Anthropic's; xAI's reasoning_effort has no such level, so a Grok
			// model naming it would be a request the provider refuses on the first turn.
			name: "max on a model that has no max", agent: AgentGrok, model: "grok-4.6",
			effort: EffortMax, wantErr: `effort "max" is not one grok-4.6 accepts`,
		},
		{
			// grok-4.5 documents xhigh as a synonym for high. Accepting it would mean an
			// operator chose a level and silently got another one.
			name: "xhigh on a model that only pretends to have one", agent: AgentGrok,
			model: "grok-4.5", effort: EffortXHigh, wantErr: `effort "xhigh" is not one grok-4.5 accepts`,
		},
		{
			// A model released after this binary was built. The effort cannot be checked
			// against anything, so the provider gets to be the one that refuses it.
			name: "an unknown model takes any level", agent: AgentGrok, model: "grok-9",
			effort: EffortMax,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateTriple(tc.agent, tc.model, tc.effort)
			if tc.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

func TestAgentForFallsBackSkillThenProfileThenDefault(t *testing.T) {
	p := &Profile{Agent: AgentGrok}
	assert.Equal(t, AgentClaude, p.AgentFor(Skill{Agent: AgentClaude}), "the skill's own wins")
	assert.Equal(t, AgentGrok, p.AgentFor(Skill{}), "then the profile's")

	empty := &Profile{}
	assert.Equal(t, DefaultAgent, empty.AgentFor(Skill{}),
		"a profile written before there was more than one backend still runs")
}

func TestEffortForFallsBackToTheProfile(t *testing.T) {
	p := &Profile{Effort: EffortLow}
	assert.Equal(t, EffortHigh, p.EffortFor(Skill{Effort: EffortHigh}))
	assert.Equal(t, EffortLow, p.EffortFor(Skill{}))
	assert.Empty(t, (&Profile{}).EffortFor(Skill{}), "unset means the model's own default")
}

// A skill that switches backend must not silently inherit an effort its new model cannot
// take. The skill on its own validates — it names no model — so only the profile, which
// knows the effective triple, can catch it.
func TestAProfileRefusesAnInheritedEffortItsSkillsModelCannotTake(t *testing.T) {
	p := &Profile{
		Name: "podium", DisplayName: "Podium", Model: "claude-opus-5",
		Effort: EffortMax, DefaultSkill: "coder",
		Skills: map[string]Skill{"coder": {
			Name: "coder", Agent: AgentGrok, Model: "grok-4.6",
			Image: "alpine:3", AllowedTools: []string{"Read"}, SystemPrompt: "x",
			MaxTurns: 1, Timeout: DefaultTimeout,
		}},
	}
	err := p.validate("profile.yaml")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `skill "coder"`)
	assert.Contains(t, err.Error(), `effort "max" is not one grok-4.6 accepts`)
}
