package examples

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/alvaroibarguen/podium/internal/agent/profiles"
)

// TestAgentProfileLoads runs examples/agent through the same loader podium-agent uses at
// start-up. profiles.Load decodes with KnownFields(true) and resolves every `file:` prompt,
// so a renamed field or a moved prompt fails here rather than in front of somebody
// following docs/agent.md — which is the same job TestEveryExampleParses does for task
// specs.
func TestAgentProfileLoads(t *testing.T) {
	p, err := profiles.Load("agent")
	require.NoError(t, err, "examples/agent does not load; docs/agent.md and this directory disagree")

	require.Equal(t, "podium", p.Name)
	require.Equal(t, "general", p.DefaultSkill)
	require.Equal(t, []string{"general"}, p.SkillNames())
	require.NotEmpty(t, p.SystemPrompt, "the profile prompt must be read from prompts/profile.md")

	general := p.Skills["general"]
	require.NotEmpty(t, general.SystemPrompt, "the skill prompt must be read from prompts/general.md")
	require.NotEmpty(t, general.AllowedTools)
	// The example is what the e2e suite runs, and the e2e node has only the locally built
	// image. Nothing here may reference a tag that has to be pulled.
	require.Equal(t, "podium-agent-runtime:dev", general.Image)
	require.Empty(t, general.Secrets, "the example skill holds no credentials of its own")
}
