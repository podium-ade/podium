package examples

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/agent/profiles"
	"github.com/podium-ade/podium/internal/agent/skills"
	"github.com/podium-ade/podium/pkg/spec"
)

// devKitSkillsDir is what agent-dev/README.md tells a reader to copy validate-pr out of.
const devKitSkillsDir = "../skills"

// installDevKit performs the copy in agent-dev/README.md — the playbook into a profile's
// playbooks/, its prompt into prompts/ — and returns the profile directory. agent/ stands in
// for "the profile you already run", since it is the one every deployment starts from.
//
// The copy is the point. agent-dev/ has no profile.yaml and is loaded by nothing, so the only
// thing worth asserting about it is that the documented step produces a profile that comes
// up: a `file:` prompt path that resolves only in the source tree is a playbook that fails
// every turn on the machine it was copied to.
func installDevKit(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	require.NoError(t, os.CopyFS(dir, os.DirFS("agent")))
	for _, f := range []struct{ from, to string }{
		{"agent-dev/playbooks/dev.yaml", "playbooks/dev.yaml"},
		{"agent-dev/prompts/dev.md", "prompts/dev.md"},
	} {
		b, err := os.ReadFile(f.from)
		require.NoErrorf(t, err, "%s is what the README tells a reader to copy", f.from)
		require.NoError(t, os.WriteFile(filepath.Join(dir, f.to), b, 0o644))
	}
	return dir
}

// TestTheDevKitLoadsWhereItIsCopiedTo runs the result of that copy through the loader
// podium-agent uses at start-up. profiles.Load decodes with KnownFields(true) and resolves
// every `file:` prompt, so a renamed field or a prompt this playbook cannot reach from its
// new home fails here rather than in front of somebody following the README.
func TestTheDevKitLoadsWhereItIsCopiedTo(t *testing.T) {
	p, err := profiles.Load(installDevKit(t))
	require.NoError(t, err, "the copy in examples/agent-dev/README.md does not produce a profile that loads")

	require.Equal(t, []string{"dev", "general"}, p.PlaybookNames())
	// Copying it in must not change what a mention with no /playbook runs. `dev` costs a
	// privileged node, a GitHub token and 8 GB; it is asked for by name.
	require.Equal(t, "general", p.DefaultPlaybook)

	dev := p.Playbooks["dev"]
	require.NotEmpty(t, dev.SystemPrompt, "prompts/dev.md must be resolved from the profile it was copied into")

	// The four grants the prompt's last paragraph depends on: a daemon to run the tests
	// against, a browser to look at what was built, the credential that opens the pull
	// request, and the procedure for attacking it. A prompt that says "then attack it" with
	// any one of them missing is a turn that fails discovering it.
	require.True(t, dev.Docker, "the playbook exists to run a suite that boots containers")
	require.True(t, dev.Browser, "the prompt tells the turn to drive what it built")
	require.Equal(t, []string{"validate-pr"}, dev.Skills)
	require.Equal(t, []spec.SecretRef{{
		Name: "podium.agent.github_token", Target: spec.SecretTargetEnv, Key: "GITHUB_TOKEN",
	}}, dev.Secrets, "no model credential may be named here; the conductor attaches that itself")

	// `docker: true` needs --allow-privileged-sidecars, and Podium places on labels alone, so
	// the flag and the label are a pair the README tells an operator to set together.
	require.Equal(t, []string{"privileged"}, dev.Labels)
	require.Equal(t, 8192, dev.Resources.MemoryMB)
	require.Equal(t, 4.0, dev.Resources.CPU)

	// A published tag, not the local one. A reader copying this file has not run
	// `make agent-runtime` and never will: the whole reason the -dev image is published is
	// that a node can pull it.
	require.Equal(t, "ghcr.io/podium-ade/podium-agent-runtime-dev:latest", dev.Image)

	require.NotContains(t, dev.Env, "DOCKER_HOST", "the conductor writes it, not the file")
	require.False(t, dev.Linear, "an example takes no tickets until somebody asks it to")

	// The repository is the field the README says to edit, so it has to be obviously unset
	// rather than plausibly someone else's.
	require.Equal(t, []profiles.Repo{{
		Name: "your-repo", URL: "https://github.com/your-org/your-repo", DefaultBranch: "main",
	}}, dev.Repos)
}

// TestTheDevKitsSkillIsWhereTheReadmeSaysItIs is the other half of that copy, and the half
// profiles.Load cannot check: it deliberately does not verify that a named skill exists,
// because a profile has to load on a machine with no skills directory at all. So a `skills:`
// entry pointing at a directory that was renamed or moved is caught by nothing — except
// here, and by playbooks/profile_test.go for the deployment that actually runs it.
func TestTheDevKitsSkillIsWhereTheReadmeSaysItIs(t *testing.T) {
	p, err := profiles.Load(installDevKit(t))
	require.NoError(t, err)

	for _, name := range p.Playbooks["dev"].Skills {
		b, err := skills.Load(devKitSkillsDir, name)
		require.NoErrorf(t, err, "dev.yaml names skill %q and %s cannot deliver it, so every turn would fail",
			name, devKitSkillsDir)
		require.Equal(t, name, b.Name)
		require.NotEmpty(t, b.Description,
			"the description is the whole of what the model reads to reach for a skill")
	}
}
