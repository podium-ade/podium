package agent

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/podium-ade/podium/internal/agent/config"
)

// minClaudeCode is the oldest Claude Code opencode 2's claude-agent provider runs: an older
// one ignores verbatim prompts and can read a person's message as a Claude Code command.
const minClaudeCode = "2.1.248"

// claudeCode is the Claude Code binary the chat host runs for a person signed in with their
// Claude subscription, or why there is none, in words for whoever tries the sign-in.
//
// chatHost is whether this conductor runs a chat host. bin is config.ChatHostClaude: what an
// operator named, or the `claude` found on PATH.
func claudeCode(ctx context.Context, cfg config.Config, chatHost bool) (path, off string) {
	const orKey = ", or paste an API key instead"
	switch {
	case !chatHost:
		return "", "Claude subscriptions are spent on the chat host, which this control plane " +
			"does not run: set PODIUM_AGENT_CHAT_HOST to an opencode 2 binary" + orKey
	case cfg.ChatHostClaude == "":
		return "", "Claude Code is not installed where the conductor runs: install Claude Code " +
			minClaudeCode + " or later there, or set PODIUM_AGENT_CHAT_HOST_CLAUDE to its path" + orKey
	}
	path = resolveBinary(cfg.ChatHostClaude)
	if path == "" {
		return "", fmt.Sprintf("the conductor cannot run Claude Code at %s: install Claude Code %s or "+
			"later there, or set PODIUM_AGENT_CHAT_HOST_CLAUDE to its path%s", cfg.ChatHostClaude, minClaudeCode, orKey)
	}
	version, err := claudeCodeVersion(ctx, path)
	if err != nil {
		return "", fmt.Sprintf("Claude Code at %s did not report its version (%v): install Claude Code "+
			"%s or later%s", path, err, minClaudeCode, orKey)
	}
	if !versionAtLeast(version, minClaudeCode) {
		return "", fmt.Sprintf("Claude Code %s at %s is too old: update it to %s or later%s",
			version, path, minClaudeCode, orKey)
	}
	if cfg.AnthropicOAuthClientID == "" {
		return path, "the Claude sign-in is turned off on this control plane " +
			"(PODIUM_AGENT_ANTHROPIC_OAUTH_CLIENT_ID=off)" + orKey
	}
	return path, ""
}

// claudeCodeVersion asks a Claude Code binary for its version: `claude --version` prints
// "2.1.330 (Claude Code)".
func claudeCodeVersion(ctx context.Context, bin string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "--version").Output() //nolint:gosec // the configured binary
	if err != nil {
		return "", err
	}
	fields := strings.Fields(string(out))
	if len(fields) == 0 || !isVersion(fields[0]) {
		return "", fmt.Errorf("unexpected output %q", strings.TrimSpace(string(out)))
	}
	return fields[0], nil
}

// isVersion is whether v is dotted numbers, with or without a pre-release or build suffix.
func isVersion(v string) bool {
	parts := versionParts(v)
	return len(parts) > 0
}

// versionParts is a release number like 2.1.248 as numbers, or nil when it is not one. A
// pre-release or build suffix is ignored, as opencode ignores it.
func versionParts(v string) []int {
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	var parts []int
	for _, f := range strings.Split(v, ".") {
		n, err := strconv.Atoi(f)
		if err != nil || n < 0 {
			return nil
		}
		parts = append(parts, n)
	}
	return parts
}

// versionAtLeast is whether release v is minimum or later.
func versionAtLeast(v, minimum string) bool {
	have, want := versionParts(v), versionParts(minimum)
	if have == nil {
		return false
	}
	for i, w := range want {
		h := 0
		if i < len(have) {
			h = have[i]
		}
		if h != w {
			return h > w
		}
	}
	return true
}
