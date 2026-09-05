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
	// Two skills: `general` on the base image, and `podium`, the dogfood, on the one image
	// Podium ships beside it. Any OTHER set of tools is an image a reader builds `FROM
	// podium-agent-runtime` and names in a skill of their own; the example does not guess at
	// which tools that would be.
	require.Equal(t, []string{"general", "podium"}, p.SkillNames())
	require.NotEmpty(t, p.SystemPrompt, "the profile prompt must be read from prompts/profile.md")

	general := p.Skills["general"]
	require.NotEmpty(t, general.SystemPrompt, "the skill prompt must be read from prompts/general.md")
	require.NotEmpty(t, general.AllowedTools)
	// The example is what the e2e suite runs, and the e2e node has only the locally built
	// image. Nothing here may reference a tag that has to be pulled.
	require.Equal(t, "podium-agent-runtime:dev", general.Image)
	require.Empty(t, general.Secrets, "the example skill holds no credentials of its own")
	require.Empty(t, general.Labels, "the example runs on any node")
	require.Empty(t, general.Repos, "the example clones nothing, so it needs no GitHub token")

	// No skill takes Linear tickets, so this profile cannot be used with a Linear key — the
	// conductor refuses to start when a key is set and no skill claims it. Whoever wants
	// tickets adds a skill with `linear: true`; docs/agent.md#linear says so.
	require.Empty(t, p.LinearSkill())
	for _, name := range p.SkillNames() {
		require.False(t, p.Skills[name].Linear, "%s must not claim Linear", name)
	}

	// profile.yaml leaves chat_default_skill unset, so the chat falls back to default_skill
	// rather than to nothing. That fallback is what the web chat's skill chip reads.
	require.Equal(t, "general", p.ChatSkill(), "an unset chat_default_skill falls back to default_skill")

	// The dogfood skill is the only one that asks for a Docker daemon, and the only one
	// that has to land on a node whose operator turned --allow-privileged-sidecars on.
	// Podium places on labels alone, so the label and the flag are a pair an operator sets
	// together; the label here is what makes that pairing expressible at all.
	dogfood := p.Skills["podium"]
	require.True(t, dogfood.Docker, "the skill exists to run Podium's own container tests")
	require.Equal(t, "podium-agent-runtime-dev:dev", dogfood.Image)
	require.Equal(t, []string{"privileged"}, dogfood.Labels)
	require.NotEmpty(t, dogfood.SystemPrompt, "the skill prompt must be read from prompts/podium.md")
	require.False(t, dogfood.Linear, "only one skill takes tickets")
	require.Equal(t, "/workspace/tmp", dogfood.Env["PODIUM_TEST_TMPDIR"],
		"the executor suite's scratch dir must sit on the volume the daemon also sees")
	require.NotContains(t, dogfood.Env, "DOCKER_HOST", "the conductor writes it, not the file")
	require.False(t, general.Docker, "general must not ask for a privileged node")

	// Routing, on the profile a human actually deploys: the chip wins outright, a typed
	// /skill is stripped from what the model is told, an unknown /word is left alone, and a
	// message naming nothing runs the default.
	chip := p.Select(profiles.Routing{
		Skill: "general", DefaultSkill: p.ChatSkill(), Text: "/podium run the tests",
	})
	require.Equal(t, "general", chip.Skill.Name)
	require.True(t, chip.Explicit)
	typed := p.Select(profiles.Routing{DefaultSkill: p.ChatSkill(), Text: "/general reply with pong"})
	require.Equal(t, "general", typed.Skill.Name)
	require.True(t, typed.Explicit)
	require.Equal(t, "reply with pong", typed.Instruction)

	unknown := p.Select(profiles.Routing{DefaultSkill: p.ChatSkill(), Text: "/shrug reply with pong"})
	require.Equal(t, "general", unknown.Skill.Name)
	require.False(t, unknown.Explicit)
	require.Equal(t, "/shrug reply with pong", unknown.Instruction, "an unknown prefix is left in the text")

	plain := p.Select(profiles.Routing{Channel: "C1", Text: "how many active accounts"})
	require.Equal(t, "general", plain.Skill.Name)
}
