package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/agent/config"
)

// fakeClaude is a `claude` that prints version the way Claude Code does.
func fakeClaude(t *testing.T, version string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "claude")
	script := "#!/bin/sh\necho '" + version + "'\n"
	require.NoError(t, os.WriteFile(path, []byte(script), 0o700)) //nolint:gosec // a test's own script
	return path
}

func TestVersionAtLeast(t *testing.T) {
	for _, tc := range []struct {
		v    string
		want bool
	}{
		{"2.1.248", true},
		{"2.1.330", true},
		{"2.2.0", true},
		{"3.0.0", true},
		{"2.1.248-beta.1", true},
		{"2.1.247", false},
		{"2.0.999", false},
		{"1.9.9", false},
		{"2.1", false},
		{"", false},
		{"latest", false},
	} {
		assert.Equal(t, tc.want, versionAtLeast(tc.v, minClaudeCode), tc.v)
	}
}

// The chat host runs Claude Code only when there is one recent enough for opencode, and a
// conductor without one can say what is missing.
func TestClaudeCodeIsRunOnlyWhenItIsRecentEnough(t *testing.T) {
	ctx := context.Background()
	cfg := config.Config{AnthropicOAuthClientID: "client"}

	path, off := claudeCode(ctx, cfg, false)
	assert.Empty(t, path)
	assert.Contains(t, off, "PODIUM_AGENT_CHAT_HOST ")

	path, off = claudeCode(ctx, cfg, true)
	assert.Empty(t, path)
	assert.Contains(t, off, "Claude Code is not installed")

	cfg.ChatHostClaude = filepath.Join(t.TempDir(), "nothing-here")
	path, off = claudeCode(ctx, cfg, true)
	assert.Empty(t, path)
	assert.Contains(t, off, "cannot run Claude Code")

	cfg.ChatHostClaude = fakeClaude(t, "2.1.100 (Claude Code)")
	path, off = claudeCode(ctx, cfg, true)
	assert.Empty(t, path, "opencode would refuse it at the first chat")
	assert.Contains(t, off, "2.1.100")
	assert.Contains(t, off, "too old")

	cfg.ChatHostClaude = fakeClaude(t, "not a version")
	path, off = claudeCode(ctx, cfg, true)
	assert.Empty(t, path)
	assert.Contains(t, off, "did not report its version")

	cfg.ChatHostClaude = fakeClaude(t, "2.1.323 (Claude Code)")
	path, off = claudeCode(ctx, cfg, true)
	assert.NotEmpty(t, path)
	assert.Empty(t, off)

	// Claude Code is there, and the operator turned the sign-in itself off.
	cfg.AnthropicOAuthClientID = ""
	path, off = claudeCode(ctx, cfg, true)
	assert.NotEmpty(t, path)
	assert.Contains(t, off, "PODIUM_AGENT_ANTHROPIC_OAUTH_CLIENT_ID=off")
}
