package chathost

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// claude-agent is built into opencode, so it names no package, and Claude Code is handed a
// bearer placeholder (ANTHROPIC_AUTH_TOKEN), not an API key.
func TestABuiltinProviderNamesNoPackageAndTakesABearer(t *testing.T) {
	raw, err := locationConfig(Location{Name: "conv-1", Providers: []Provider{{
		ID: "claude-agent", Builtin: true, BaseURL: "http://127.0.0.1:1/gw/tok", AuthToken: "podium",
		Models: []string{"claude-opus-5-5"},
	}, {
		ID: "anthropic", BaseURL: "http://127.0.0.1:1/gw/tok2", APIKey: "podium",
	}}})
	require.NoError(t, err)
	var doc struct {
		Providers map[string]map[string]any `json:"providers"`
	}
	require.NoError(t, json.Unmarshal(raw, &doc))

	claude := doc.Providers["claude-agent"]
	assert.NotContains(t, claude, "package")
	assert.Equal(t, map[string]any{"baseURL": "http://127.0.0.1:1/gw/tok", "authToken": "podium"}, claude["settings"])
	assert.Contains(t, claude["models"], "claude-opus-5-5")

	plain := doc.Providers["anthropic"]
	assert.Equal(t, "@opencode/ai/providers/anthropic", plain["package"])
	assert.Equal(t, map[string]any{"baseURL": "http://127.0.0.1:1/gw/tok2", "apiKey": "podium"}, plain["settings"])
}
