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
//
// This directory is the worked example and nothing else. The profile the real bot runs is
// ../playbooks, and playbooks/profile_test.go is its equivalent of this test — the two are
// separate because one of them can afford to need a privileged node and a GitHub token and
// the other cannot.
func TestAgentProfileLoads(t *testing.T) {
	p, err := profiles.Load("agent")
	require.NoError(t, err, "examples/agent does not load; docs/agent.md and this directory disagree")

	require.Equal(t, "podium", p.Name)
	require.Equal(t, "general", p.DefaultPlaybook)
	// One playbook, on the base image Podium ships. Any OTHER set of tools is an image a
	// reader builds `FROM podium-agent-runtime` and names in a playbook of their own; the
	// example does not guess at which tools that would be, and the dogfood that does — the
	// `podium` playbook — lives in ../playbooks because it is configuration and not
	// documentation.
	require.Equal(t, []string{"general"}, p.PlaybookNames())
	require.NotEmpty(t, p.SystemPrompt, "the profile prompt must be read from prompts/profile.md")

	general := p.Playbooks["general"]
	require.NotEmpty(t, general.SystemPrompt, "the playbook prompt must be read from prompts/general.md")
	require.NotEmpty(t, general.AllowedTools)
	// The example is what the e2e suite runs, and the e2e node has only the locally built
	// image. Nothing here may reference a tag that has to be pulled.
	require.Equal(t, "podium-agent-runtime:dev", general.Image)
	require.Empty(t, general.Secrets, "the example playbook holds no credentials of its own")
	require.Empty(t, general.Labels, "the example runs on any node")
	require.Empty(t, general.Repos, "the example clones nothing, so it needs no GitHub token")

	// No playbook takes Linear tickets, so this profile cannot be used with a Linear key — the
	// conductor refuses to start when a key is set and no playbook claims it. Whoever wants
	// tickets adds a playbook with `linear: true`; docs/agent.md#linear says so.
	require.Empty(t, p.LinearPlaybook())
	for _, name := range p.PlaybookNames() {
		require.False(t, p.Playbooks[name].Linear, "%s must not claim Linear", name)
	}

	// The assistant — what answers the web chat, in the conductor's own process — names no
	// Agent Skills and takes the default step cap. A reader's first profile must come up on a
	// machine with an empty skill library.
	require.Empty(t, p.Assistant().Skills)
	require.Equal(t, profiles.DefaultMaxTurns, p.Assistant().MaxTurns)

	// Nothing here may ask for a privileged node, a browser or a skill. This profile is what
	// the e2e suite runs and what a reader copies first, so it has to come up on an ordinary
	// node with an empty skill library — the playbook that needs all three is in
	// ../playbooks.
	require.False(t, general.Docker, "the example must not ask for a privileged node")
	require.False(t, general.Browser, "the example must not need a browser sidecar")
	require.Empty(t, general.Skills, "the example must load with no skill library at all")

	// Routing a TASK — a Slack thread or a Linear ticket. A typed /playbook is stripped from
	// what the model is told, an unknown /word is left alone, and a message naming nothing
	// runs the default. The case where a source's own choice beats a DIFFERENT typed name
	// needs two playbooks to be worth anything, so it is asserted in
	// ../playbooks/profile_test.go, which has them.
	typed := p.Select(profiles.Routing{Text: "/general reply with pong"})
	require.Equal(t, "general", typed.Playbook.Name)
	require.True(t, typed.Explicit)
	require.Equal(t, "reply with pong", typed.Instruction)

	unknown := p.Select(profiles.Routing{Text: "/shrug reply with pong"})
	require.Equal(t, "general", unknown.Playbook.Name)
	require.False(t, unknown.Explicit)
	require.Equal(t, "/shrug reply with pong", unknown.Instruction, "an unknown prefix is left in the text")

	plain := p.Select(profiles.Routing{Channel: "C1", Text: "how many active accounts"})
	require.Equal(t, "general", plain.Playbook.Name)
}
