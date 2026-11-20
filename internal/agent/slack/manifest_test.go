package slack

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

// TestManifestAsksForWhatTheSourceCalls guards the checked-in app manifest: a scope missing
// there is not an error anywhere, Slack just answers with less. Without users:read.email
// every profile comes back with no email, verifiedEmail matches nobody, and every mention is
// refused as "not in podium".
func TestManifestAsksForWhatTheSourceCalls(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "deploy", "slack-app-manifest.yaml"))
	require.NoError(t, err)
	var manifest struct {
		OAuthConfig struct {
			Scopes struct {
				Bot []string `yaml:"bot"`
			} `yaml:"scopes"`
		} `yaml:"oauth_config"`
	}
	require.NoError(t, yaml.Unmarshal(raw, &manifest))

	for _, scope := range []string{
		"app_mentions:read", // the mention that starts a turn
		"channels:history",  // the thread it is in
		"chat:write",        // the answer
		"reactions:write",   // ⏳ / ✅ / ❌
		"users:read",        // a user id's display name
		"users:read.email",  // who is speaking (verifiedEmail), and DirectMessage's lookup
	} {
		require.Contains(t, manifest.OAuthConfig.Scopes.Bot, scope)
	}
}
