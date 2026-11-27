package examples

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/agent/profiles"
)

// TestReviewSampleLoads runs examples/review through the conductor's own loader, as
// TestDesktopSampleLoads does for examples/desktop: a playbook and its prompt, meant to be
// copied into a reader's profile, so the test gives it the smallest profile around them.
func TestReviewSampleLoads(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "profile.yaml"), []byte(
		"name: review-sample\ndisplay_name: Review sample\nsystem_prompt: hi\nmodel: claude-opus-5-5\nagent: claude\n"), 0o600))
	for _, sub := range []string{"playbooks", "prompts"} {
		abs, err := filepath.Abs(filepath.Join("review", sub))
		require.NoError(t, err)
		require.NoError(t, os.Symlink(abs, filepath.Join(dir, sub)))
	}
	p, err := profiles.Load(dir)
	require.NoError(t, err, "examples/review does not load; docs/agent.md and this directory disagree")
	require.Equal(t, []string{"review"}, p.PlaybookNames())

	review := p.Playbooks["review"]
	require.True(t, review.Reviews(), "it is what \"The repository's review playbook\" resolves to")
	require.Equal(t, "review", p.GitHubPlaybook())
	require.Empty(t, review.Secrets, "it posts with the token its task is given, and holds none of its own")
	require.NotContains(t, review.AllowedTools, "edit", "a review changes nothing")
	require.NotContains(t, review.AllowedTools, "write")
	// The prompt is the whole of what a review is now: every step the conductor once took on
	// its own is in it, in words a reader can change.
	for _, step := range []string{"<!-- podium-review-status -->", "Review in progress", "repos/$R/pulls/$N/reviews",
		"REQUEST_CHANGES", "Review did not finish", "commit_id"} {
		require.True(t, strings.Contains(review.SystemPrompt, step), "the prompt has lost %q", step)
	}
	// Every lookup of its own comments and reviews matches on the App's login. That is the
	// variable the runtime sets from a minted App token (AppLoginEnv in agent/runtime/src/
	// gitcred.ts); a clone's user.name is a persona, or podium-agent, and matches nothing.
	require.Contains(t, review.SystemPrompt, "ME=${PODIUM_GITHUB_APP_LOGIN:?}")
	require.NotContains(t, review.SystemPrompt, "config user.name")
}
