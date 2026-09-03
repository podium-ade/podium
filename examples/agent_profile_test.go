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
	require.Equal(t, []string{"analyst", "coder", "general"}, p.SkillNames())
	require.NotEmpty(t, p.SystemPrompt, "the profile prompt must be read from prompts/profile.md")

	general := p.Skills["general"]
	require.NotEmpty(t, general.SystemPrompt, "the skill prompt must be read from prompts/general.md")
	require.NotEmpty(t, general.AllowedTools)
	// The example is what the e2e suite runs, and the e2e node has only the locally built
	// image. Nothing here may reference a tag that has to be pulled.
	require.Equal(t, "podium-agent-runtime:dev", general.Image)
	require.Empty(t, general.Secrets, "the example skill holds no credentials of its own")

	// The coder skill is the one Linear tickets run, the only skill that names the GitHub
	// token, and the only one on the browser image. All three are load-bearing: a second
	// skill claiming Linear is a load error, and a token on any other skill would hand
	// repository write access to a turn nobody scoped it for.
	coder := p.Skills["coder"]
	require.True(t, coder.Linear, "exactly one skill must set linear: true")
	require.Equal(t, "coder", p.LinearSkill())
	require.Equal(t, "podium-agent-runtime-browser:dev", coder.Image)
	require.Equal(t, []string{"browser"}, coder.Labels)
	require.NotEmpty(t, coder.SystemPrompt, "the skill prompt must be read from prompts/coder.md")
	require.NotEmpty(t, coder.Repos, "the coder skill has to name a repository to clone")
	require.Len(t, coder.Secrets, 1)
	require.Equal(t, "podium.agent.github_token", coder.Secrets[0].Name)
	require.Equal(t, "GITHUB_TOKEN", coder.Secrets[0].Key)
	require.False(t, general.Linear, "only one skill takes tickets")

	// The analyst skill is what the web chat starts with, and its two warehouse secrets are
	// the whole of its access: the read-only role behind one of them is the real control.
	// The example ships both because docs/agent.md tells a deployment to delete the line it
	// does not use, and a test that pinned one would make the other look wrong.
	analyst := p.Skills["analyst"]
	require.Equal(t, "analyst", p.ChatSkill(), "profile.yaml: chat_default_skill")
	require.Equal(t, "podium-agent-runtime-data:dev", analyst.Image)
	require.NotEmpty(t, analyst.SystemPrompt, "the skill prompt must be read from prompts/analyst.md")
	require.False(t, analyst.Linear, "only one skill takes tickets")
	require.Empty(t, analyst.Labels, "a warehouse query needs no special node")
	require.Len(t, analyst.Secrets, 2)
	require.Equal(t, "podium.agent.warehouse_url", analyst.Secrets[0].Name)
	require.Equal(t, "WAREHOUSE_URL", analyst.Secrets[0].Key)
	require.Equal(t, "podium.agent.warehouse_credentials", analyst.Secrets[1].Name)
	require.Equal(t, "/podium/secrets/warehouse.json", analyst.Secrets[1].Key)
	// Nothing but the coder skill gets the GitHub token, and nothing but the analyst gets a
	// warehouse credential. A skill only ever gets the secrets its own file names.
	for _, ref := range coder.Secrets {
		require.NotContains(t, ref.Name, "warehouse", "the coder skill has no warehouse access")
	}
	for _, ref := range analyst.Secrets {
		require.NotEqual(t, "podium.agent.github_token", ref.Name,
			"the analyst skill has no repository access")
	}

	// The chat's skill chip: an explicit skill from the source wins over every routing rule,
	// which is how a human overrides the chat default for one message.
	chip := p.Select("general", "", "how many active accounts last month")
	require.Equal(t, "general", chip.Skill.Name)
	require.True(t, chip.Explicit)
	// And typing it still works.
	typed := p.Select("", "", "/analyst how many active accounts last month")
	require.Equal(t, "analyst", typed.Skill.Name)
	require.Equal(t, "how many active accounts last month", typed.Instruction)

	// Story four's entry point: `/coder …` in Slack. The prefix rule is step 17's and the
	// skill is this step's, and the two only meet in this directory.
	sel := p.Select("", "C1", "/coder write a PR that adds a copy button")
	require.Equal(t, "coder", sel.Skill.Name)
	require.True(t, sel.Explicit)
	require.Equal(t, "write a PR that adds a copy button", sel.Instruction)
}
