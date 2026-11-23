package examples

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/agent/profiles"
)

// TestDesktopSampleLoads runs examples/desktop through the conductor's own loader, as
// TestAgentProfileLoads does for examples/agent, so the sample a reader copies into their own
// profile is one the conductor accepts: every key known, the prompt resolved, and nothing
// a desktop refuses (a browser sidecar, a port of the desktop's own).
func TestDesktopSampleLoads(t *testing.T) {
	// The sample is a playbook and its prompt, meant to be copied into a reader's own profile,
	// so it carries no profile.yaml of its own: the repository has two profiles and this is not
	// a third. The test gives it the smallest one, around the sample's own directories.
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "profile.yaml"), []byte(
		"name: desktop-sample\ndisplay_name: Desktop sample\nsystem_prompt: hi\nmodel: claude-opus-5-5\nagent: claude\n"), 0o600))
	for _, sub := range []string{"playbooks", "prompts"} {
		abs, err := filepath.Abs(filepath.Join("desktop", sub))
		require.NoError(t, err)
		require.NoError(t, os.Symlink(abs, filepath.Join(dir, sub)))
	}
	p, err := profiles.Load(dir)
	require.NoError(t, err, "examples/desktop does not load; docs/agent.md and this directory disagree")
	require.Equal(t, []string{"spreadsheet-desk"}, p.PlaybookNames())

	desk := p.Playbooks["spreadsheet-desk"]
	require.True(t, desk.Desktop)
	require.False(t, desk.Browser, "a desktop's browser is the one on its screen")
	require.NotEmpty(t, desk.SystemPrompt, "the playbook prompt must be read from prompts/spreadsheet-desk.md")
	require.Empty(t, desk.Secrets, "the sample holds no credentials of its own")
	require.Empty(t, desk.Repos, "the sample clones nothing")

	// The playbook names the image this directory's Dockerfile builds, and the README tells a
	// reader to build it under that tag. A rename in one place and not the others is a turn
	// that fails pulling an image nobody built.
	require.Equal(t, "podium-desktop-sample:dev", desk.Image)
	readme, err := os.ReadFile(filepath.Join("desktop", "README.md"))
	require.NoError(t, err)
	require.Contains(t, string(readme), "docker build -t "+desk.Image+" examples/desktop")

	// The image extends the desktop, and stays an extension: no command of its own in front
	// of the runtime, and it hands the box back to the agent user the desktop runs as.
	dockerfile, err := os.ReadFile(filepath.Join("desktop", "Dockerfile"))
	require.NoError(t, err)
	require.Contains(t, string(dockerfile), "ARG DESKTOP_IMAGE="+profiles.LocalDesktopImage)
	require.Regexp(t, regexp.MustCompile(`(?m)^USER agent$`), string(dockerfile))
	require.NotRegexp(t, regexp.MustCompile(`(?m)^(ENTRYPOINT|CMD)\b`), string(dockerfile))
}
