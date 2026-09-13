// Package profile holds the profile Podium's own bot runs: profile.yaml, the playbooks
// beside it and their prompts. It has no Go code — only the tests below, which load the
// directory the way podium-agent does at start-up and resolve every skill it names against
// ../skills.
//
// This is the profile that ACTUALLY RUNS, so breaking it has to fail CI rather than a turn.
// examples/agent_profile_test.go is the same job for the worked example; the two are
// separate because that one has to load on a machine with no privileged node, no GitHub
// token and no skill library, and this one describes a bot that has all three.
package profile

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/agent/profiles"
	"github.com/podium-ade/podium/internal/agent/skills"
	"github.com/podium-ade/podium/pkg/spec"
)

// skillsDir is the other half of this deployment: PODIUM_AGENT_SKILLS_DIR points here, and a
// playbook below names a skill out of it by name.
const skillsDir = "../skills"

func TestTheBotsOwnProfileLoads(t *testing.T) {
	p, err := profiles.Load(".")
	require.NoError(t, err, "profile/ does not load; the real deployment would not start")

	require.Equal(t, "podium", p.Name)
	require.Equal(t, "Podium", p.DisplayName)
	require.NotEmpty(t, p.SystemPrompt, "the profile prompt must be read from prompts/profile.md")
	require.Equal(t, []string{"general", "podium"}, p.PlaybookNames())

	// A mention with no /playbook must NOT run the dogfood. `podium` needs a privileged node,
	// a Docker daemon, a browser and 8 GB; a question in a thread should cost a container.
	require.Equal(t, "general", p.DefaultPlaybook)

	// The web chat runs neither: it is answered by the assistant, in the conductor's own
	// process, and the two playbooks above are what that turn delegates to. It has no step
	// cap; the playbooks it delegates to do.
	require.Zero(t, p.Assistant().MaxTurns)
	require.Equal(t, 200, p.Playbooks["podium"].MaxTurns)

	// No playbook takes Linear tickets, so this profile cannot be used with a Linear key as
	// it stands — the conductor refuses to start when a key is set and nothing claims it.
	require.Empty(t, p.LinearPlaybook())
}

// The cheap half. It holds no credential and clones nothing, which is what makes it safe to
// be the default for anything anybody types.
func TestTheGeneralPlaybookCostsNothingToRun(t *testing.T) {
	p, err := profiles.Load(".")
	require.NoError(t, err)

	general := p.Playbooks["general"]
	require.Equal(t, "podium-agent-runtime:dev", general.Image)
	require.NotEmpty(t, general.SystemPrompt, "the prompt must be read from prompts/general.md")
	require.NotEmpty(t, general.AllowedTools)
	require.Empty(t, general.Secrets, "the default playbook holds no credential of its own")
	require.Empty(t, general.Repos, "it clones nothing, so it needs no GitHub token")
	require.Empty(t, general.Labels, "it runs on any node")
	require.Empty(t, general.Skills, "nothing is implicit: a skill is named or it is denied")
	require.False(t, general.Docker, "the default must not need --allow-privileged-sidecars")
	require.False(t, general.Browser)
}

// The dogfood, and the three grants that make it one: an environment (a Docker daemon and a
// browser), a credential (the GitHub token) and a procedure (the validate-pr skill). All
// three have to be here together — the prompt tells a turn to attack its own pull request in
// a browser, and a prompt that says so with any one of them missing is a turn that fails
// discovering it.
func TestThePodiumPlaybookCanRunTheStackAndLookAtIt(t *testing.T) {
	p, err := profiles.Load(".")
	require.NoError(t, err)

	dogfood := p.Playbooks["podium"]
	require.Equal(t, "podium-agent-runtime-dev:dev", dogfood.Image,
		"the -dev image is what carries Go, the docker client and the browser's MCP client")
	require.NotEmpty(t, dogfood.SystemPrompt, "the prompt must be read from prompts/podium.md")

	require.True(t, dogfood.Docker, "the playbook exists to run Podium's own container tests")
	require.True(t, dogfood.Browser, "the prompt tells the turn to drive what it built in a browser")
	require.Equal(t, []string{"validate-pr"}, dogfood.Skills)

	// Podium places on labels alone and knows nothing about which nodes allow privilege, so
	// the label and --allow-privileged-sidecars are a pair an operator sets together. The
	// browser adds nothing here: it is an ordinary unprivileged container.
	require.Equal(t, []string{"privileged"}, dogfood.Labels)

	// `resources` is the TASK container's limit and every sidecar carries its own, so the
	// browser's memory comes out of what the node has free rather than out of this number.
	// That is why adding `browser: true` did not move it.
	require.Equal(t, 8192, dogfood.Resources.MemoryMB)
	require.Equal(t, 4.0, dogfood.Resources.CPU)

	require.Equal(t, []spec.SecretRef{{
		Name: "podium.agent.github_token", Target: spec.SecretTargetEnv, Key: "GITHUB_TOKEN",
	}}, dogfood.Secrets, "no model credential may be named here; the conductor attaches that itself")
	require.Equal(t, []profiles.Repo{{
		Name: "podium", URL: "https://github.com/podium-ade/podium", DefaultBranch: "main",
	}}, dogfood.Repos)

	require.Equal(t, "/workspace/tmp", dogfood.Env["PODIUM_TEST_TMPDIR"],
		"the executor suite's scratch dir must sit on the volume the nested daemon also sees")
	require.NotContains(t, dogfood.Env, "DOCKER_HOST", "the conductor writes it, not the file")

	require.False(t, dogfood.Linear, "this profile takes no tickets")
}

// TestEverySkillAPlaybookNamesResolves is the link that has to fail CI rather than a turn.
// profiles.Load deliberately does NOT check that a named skill exists — a playbook file has
// to load on a machine with no skills directory at all — so nothing else in the tree notices
// a `skills:` entry pointing at a directory that was renamed, deleted or never added. Here
// the two are checked against each other, through exactly the loader and exactly the caps the
// conductor applies at the start of every turn.
func TestEverySkillAPlaybookNamesResolves(t *testing.T) {
	p, err := profiles.Load(".")
	require.NoError(t, err)

	named := 0
	for _, name := range p.PlaybookNames() {
		pb := p.Playbooks[name]
		require.LessOrEqual(t, len(pb.Skills), skills.MaxSkills,
			"playbook %q names more skills than one playbook may have", name)
		for _, s := range pb.Skills {
			named++
			b, err := skills.Load(skillsDir, s)
			require.NoErrorf(t, err, "playbook %q names skill %q and %s cannot deliver it",
				name, s, skillsDir)
			require.Equal(t, s, b.Name)
			require.NotEmpty(t, b.Description,
				"the description is the whole of what the model reads to reach for a skill")
			require.Equal(t, skills.EnvFor(s), b.Env)
		}
	}
	require.Positive(t, named, "no playbook names a skill; this test would then prove nothing")
}

// TestEverySkillInTheDirectoryLoads covers the other direction: a skill that is in ../skills
// and cannot be packed is broken whether or not a playbook names it yet, and the operator
// would find out from ListSkills reporting it with a reason attached. ListDir is what that
// screen calls.
func TestEverySkillInTheDirectoryLoads(t *testing.T) {
	listed, err := skills.ListDir(skillsDir)
	require.NoError(t, err)
	require.NotEmpty(t, listed, "%s holds no skills at all — has the directory moved?", skillsDir)

	for _, s := range listed {
		require.Emptyf(t, s.Problem, "skill %q does not load: %s", s.Name, s.Problem)
		require.Positive(t, s.FileCount)
		require.LessOrEqual(t, s.FileCount, skills.MaxFiles)
		require.LessOrEqual(t, s.SizeBytes, int64(skills.MaxBytes))
	}
}

// TestTheSkillFitsInAnEnvironmentVariable is the cap that is not about tidiness. A bundle
// travels as one environment string on the task spec, and past MAX_ARG_STRLEN the container
// cannot exec at all — the runtime never starts, so it cannot report why. A skill that grows
// past the delivery cap has to fail here, where the number is visible, and not in a task
// container that never runs.
func TestTheSkillFitsInAnEnvironmentVariable(t *testing.T) {
	b, err := skills.Load(skillsDir, "validate-pr")
	require.NoError(t, err)

	require.LessOrEqual(t, len(b.Encoded), skills.MaxEncodedBytes,
		"validate-pr is %d bytes encoded and the delivery cap is %d",
		len(b.Encoded), skills.MaxEncodedBytes)
	require.LessOrEqual(t, len(b.Document), skills.MaxBytes)
	t.Logf("validate-pr: %d files, %d bytes unpacked, %d bytes encoded, sha256 %s",
		b.Files, len(b.Document), len(b.Encoded), b.SHA256)
}

// Routing on the profile a human actually deploys, and the case the worked example cannot
// show because it has only one playbook: a playbook the SOURCE knows is knowledge and beats
// a /playbook typed in the same message, while an unknown /word is left in the text so that
// somebody typing /shrug does not break the bot.
func TestASourcesOwnPlaybookBeatsATypedOne(t *testing.T) {
	p, err := profiles.Load(".")
	require.NoError(t, err)

	known := p.Select(profiles.Routing{Playbook: "general", Text: "/podium run the tests"})
	require.Equal(t, "general", known.Playbook.Name)
	require.True(t, known.Explicit)

	typed := p.Select(profiles.Routing{Text: "/podium run the tests"})
	require.Equal(t, "podium", typed.Playbook.Name)
	require.True(t, typed.Explicit)
	require.Equal(t, "run the tests", typed.Instruction)

	unknown := p.Select(profiles.Routing{Text: "/shrug reply with pong"})
	require.Equal(t, "general", unknown.Playbook.Name)
	require.False(t, unknown.Explicit)
	require.Equal(t, "/shrug reply with pong", unknown.Instruction)

	plain := p.Select(profiles.Routing{Channel: "C1", Text: "how many active accounts"})
	require.Equal(t, "general", plain.Playbook.Name)
}
