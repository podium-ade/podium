package config

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// profileDir writes the smallest directory Validate accepts.
func profileDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, ProfileFile), []byte("name: podium\n"), 0o600))
	return dir
}

func valid(t *testing.T) Config {
	return Config{
		Server:      "http://127.0.0.1:8080",
		APIToken:    "devtoken",
		DatabaseURL: "postgres://podium:podium@127.0.0.1:5432/podium_agent",
		Listen:      DefaultListen,
		Token:       "agenttoken",
		ProfileDir:  profileDir(t),
	}
}

func TestValidateAcceptsTheMinimumConfiguration(t *testing.T) {
	require.NoError(t, valid(t).Validate())
}

func TestValidateNamesTheVariableThatIsMissing(t *testing.T) {
	tests := []struct {
		name  string
		mutig func(*Config)
		want  string
	}{
		{"no server", func(c *Config) { c.Server = "" }, "PODIUM_AGENT_SERVER"},
		{"relative server", func(c *Config) { c.Server = "127.0.0.1:8080" }, "PODIUM_AGENT_SERVER"},
		{"no scheme", func(c *Config) { c.Server = "ftp://podium" }, "PODIUM_AGENT_SERVER"},
		{"http without a token", func(c *Config) { c.APIToken = "" }, "PODIUM_AGENT_API_TOKEN"},
		{"no database", func(c *Config) { c.DatabaseURL = "" }, "PODIUM_AGENT_DATABASE_URL"},
		{"no token", func(c *Config) { c.Token = "" }, "PODIUM_AGENT_TOKEN"},
		{"no listen", func(c *Config) { c.Listen = "" }, "PODIUM_AGENT_LISTEN"},
		{"no profile dir", func(c *Config) { c.ProfileDir = "" }, "PODIUM_AGENT_PROFILE_DIR"},
		{"profile dir does not exist", func(c *Config) { c.ProfileDir = "/nope/nowhere" }, "PODIUM_AGENT_PROFILE_DIR"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := valid(t)
			tc.mutig(&cfg)
			err := cfg.Validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

// A tailnet control plane names the caller by WhoIs, so an empty API token is right there
// and wrong over plain HTTP.
func TestATailnetServerNeedsNoAPIToken(t *testing.T) {
	cfg := valid(t)
	cfg.Server = "https://podium.taila79bf6.ts.net"
	cfg.APIToken = ""
	require.NoError(t, cfg.Validate())
}

// One Slack token without the other is the mistake somebody actually makes, and the error
// has to say which one is missing rather than "slack is misconfigured".
func TestOneSlackTokenNamesTheOtherOne(t *testing.T) {
	cfg := valid(t)
	cfg.SlackAppToken = "xapp-not-a-real-token"
	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "PODIUM_AGENT_SLACK_BOT_TOKEN")

	cfg = valid(t)
	cfg.SlackBotToken = "xoxb-not-a-real-token"
	err = cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "PODIUM_AGENT_SLACK_APP_TOKEN")

	cfg = valid(t)
	cfg.SlackAppToken, cfg.SlackBotToken = "xapp-a", "xoxb-b"
	require.NoError(t, cfg.Validate())
	assert.True(t, cfg.SlackEnabled())
}

func TestAProfileDirWithoutProfileYAMLIsRefused(t *testing.T) {
	cfg := valid(t)
	cfg.ProfileDir = t.TempDir()
	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), ProfileFile)
}

func TestFromEnvAppliesTheDefaults(t *testing.T) {
	t.Setenv("PODIUM_AGENT_SERVER", "http://127.0.0.1:8080")
	cfg := FromEnv()
	assert.Equal(t, DefaultListen, cfg.Listen)
	assert.Equal(t, DefaultProfileDir, cfg.ProfileDir)
	assert.False(t, cfg.DevSource)
}

// A knob that mounts routes for injecting messages must not turn itself on because
// somebody wrote "yes", which is the same rule internal/server/config.go follows.
func TestDevSourceIsOffForAnythingStrconvDoesNotUnderstand(t *testing.T) {
	for _, v := range []string{"", "yes", "on", "sure", "0", "false"} {
		t.Setenv("PODIUM_AGENT_DEV_SOURCE", v)
		assert.False(t, FromEnv().DevSource, "%q must not enable the dev source", v)
	}
	for _, v := range []string{"1", "true", "TRUE", "t"} {
		t.Setenv("PODIUM_AGENT_DEV_SOURCE", v)
		assert.True(t, FromEnv().DevSource, "%q must enable the dev source", v)
	}
}

// The Config holds four credentials, so the only way it may reach a log statement is
// through its own LogValue. This is the RedactForLog discipline applied to a struct.
func TestLoggingAConfigLeaksNoToken(t *testing.T) {
	cfg := valid(t)
	cfg.APIToken = "devtoken-secret"
	cfg.Token = "agenttoken-secret"
	cfg.SlackAppToken = "xapp-secret"
	cfg.SlackBotToken = "xoxb-secret"

	var buf bytes.Buffer
	slog.New(slog.NewTextHandler(&buf, nil)).Info("configured", "config", cfg)

	out := buf.String()
	for _, secret := range []string{"devtoken-secret", "agenttoken-secret", "xapp-secret", "xoxb-secret"} {
		assert.NotContains(t, out, secret)
	}
	// And it still says the things an operator needs to see.
	assert.Contains(t, out, cfg.Server)
	assert.Contains(t, out, "api_token_set=true")
	assert.Contains(t, out, "slack=true")
}
