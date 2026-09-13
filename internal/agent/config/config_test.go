package config

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
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
	cfg.Server = "https://podium.tail0a1b2c.ts.net"
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

// ---------------------------------------------------------------------------
// shared memory
// ---------------------------------------------------------------------------

// Memory is optional and off by default: an install with no memory service runs every turn
// without one rather than refusing to start.
func TestMemoryIsOffUnlessAURLIsGiven(t *testing.T) {
	cfg := valid(t)
	assert.False(t, cfg.MemoryEnabled())
	require.NoError(t, cfg.Validate(), "no memory is a supported configuration")
}

func TestMemoryDefaults(t *testing.T) {
	t.Setenv("PODIUM_AGENT_MEMORY_URL", "http://hindsight:8888")
	t.Setenv("PODIUM_AGENT_MEMORY_API_KEY", "memtoken")
	cfg := FromEnv()

	assert.True(t, cfg.MemoryEnabled())
	assert.Equal(t, DefaultMemoryTaskURL, cfg.MemoryTaskURL,
		"a task reaches the host through the bridge gateway, not over the compose network")
	assert.Equal(t, DefaultMemoryBank, cfg.MemoryBank)
}

func TestMemoryValidationNamesTheVariableThatIsWrong(t *testing.T) {
	tests := []struct {
		name string
		mut  func(*Config)
		want string
	}{
		// Hindsight has no authentication until it is given a key, so a URL with no key is
		// a memory anybody who can reach the port can read and rewrite.
		{"no key", func(c *Config) { c.MemoryAPIKey = "" }, "PODIUM_AGENT_MEMORY_API_KEY"},
		{"relative url", func(c *Config) { c.MemoryURL = "hindsight:8888" }, "PODIUM_AGENT_MEMORY_URL"},
		{"url with a path", func(c *Config) { c.MemoryURL = "http://hindsight:8888/v1" }, "PODIUM_AGENT_MEMORY_URL"},
		{"bad scheme", func(c *Config) { c.MemoryURL = "ftp://hindsight" }, "PODIUM_AGENT_MEMORY_URL"},
		{"relative task url", func(c *Config) { c.MemoryTaskURL = "host.docker.internal:8888" },
			"PODIUM_AGENT_MEMORY_TASK_URL"},
		// The bank becomes a path segment in the MCP URL every turn is handed.
		{"empty bank", func(c *Config) { c.MemoryBank = "" }, "PODIUM_AGENT_MEMORY_BANK"},
		{"bank with a slash", func(c *Config) { c.MemoryBank = "podium/../default" }, "PODIUM_AGENT_MEMORY_BANK"},
		{"bank with a space", func(c *Config) { c.MemoryBank = "my bank" }, "PODIUM_AGENT_MEMORY_BANK"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := valid(t)
			cfg.MemoryURL = "http://hindsight:8888"
			cfg.MemoryTaskURL = DefaultMemoryTaskURL
			cfg.MemoryBank = DefaultMemoryBank
			cfg.MemoryAPIKey = "memtoken"
			tc.mut(&cfg)

			err := cfg.Validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

// The memory key is full read/write of every memory the organisation has, so it is one more
// credential the Config must never print.
func TestLoggingAConfigLeaksNoMemoryKey(t *testing.T) {
	cfg := valid(t)
	cfg.MemoryURL = "http://hindsight:8888"
	cfg.MemoryAPIKey = "memtoken-secret"

	var buf bytes.Buffer
	slog.New(slog.NewTextHandler(&buf, nil)).Info("configured", "config", cfg)

	out := buf.String()
	assert.NotContains(t, out, "memtoken-secret")
	assert.Contains(t, out, "memory_api_key_set=true")
	assert.Contains(t, out, "http://hindsight:8888")
}

// ---------------------------------------------------------------------------
// the GitHub App
// ---------------------------------------------------------------------------

// appKeyPEM writes a real RSA key to a file and returns the path. Real rather than a
// placeholder because Validate parses it: a fake would test the wrong branch.
func appKeyPEM(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "app.pem")
	require.NoError(t, os.WriteFile(path, pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	}), 0o600))
	return path
}

// withApp is a valid config that mints through an App. Listen is not the default: a
// loopback listener is unreachable from a task, which Validate refuses on purpose.
func withApp(t *testing.T) Config {
	t.Helper()
	cfg := valid(t)
	cfg.Listen = "0.0.0.0:8090"
	cfg.GitHubAppID = "123456"
	cfg.GitHubAppKeyFile = appKeyPEM(t)
	cfg.TaskURL = DefaultTaskURL
	return cfg
}

// The App is additive: an install that never heard of it keeps the PAT path untouched.
func TestGitHubAppIsOffUnlessConfigured(t *testing.T) {
	cfg := valid(t)
	assert.False(t, cfg.GitHubAppEnabled())
	require.NoError(t, cfg.Validate(), "no GitHub App is a supported configuration")
}

func TestGitHubAppDefaults(t *testing.T) {
	t.Setenv("PODIUM_AGENT_GITHUB_APP_ID", "  123456  ")
	cfg := FromEnv()

	assert.Equal(t, "123456", cfg.GitHubAppID, "an id pasted with whitespace is still an id")
	assert.Equal(t, DefaultTaskURL, cfg.TaskURL,
		"a task reaches this conductor through the bridge gateway, as it reaches Hindsight")
}

func TestGitHubAppAcceptsAWholeConfiguration(t *testing.T) {
	cfg := withApp(t)
	require.NoError(t, cfg.Validate())
	assert.True(t, cfg.GitHubAppEnabled())

	got, err := cfg.GitHubAppPrivateKey()
	require.NoError(t, err)
	assert.Contains(t, string(got), "BEGIN RSA PRIVATE KEY")
}

func TestGitHubAppReadsTheKeyFromTheEnvironmentToo(t *testing.T) {
	cfg := withApp(t)
	raw, err := os.ReadFile(cfg.GitHubAppKeyFile)
	require.NoError(t, err)
	cfg.GitHubAppKeyFile = ""
	cfg.GitHubAppKey = string(raw)

	require.NoError(t, cfg.Validate())
	assert.True(t, cfg.GitHubAppEnabled())
}

// Half-configured is the state worth refusing loudly: it leaves the App off and every turn
// quietly back on whatever PAT the playbook still names.
func TestGitHubAppValidationNamesTheVariableThatIsWrong(t *testing.T) {
	tests := []struct {
		name string
		mut  func(*Config)
		want string
	}{
		{"key without an id", func(c *Config) { c.GitHubAppID = "" }, "PODIUM_AGENT_GITHUB_APP_ID"},
		{"id without a key", func(c *Config) { c.GitHubAppKeyFile = "" },
			"PODIUM_AGENT_GITHUB_APP_KEY_FILE"},
		{"both key forms at once", func(c *Config) { c.GitHubAppKey = "-----BEGIN RSA PRIVATE KEY-----" },
			"both set"},
		{"key file that is not there", func(c *Config) { c.GitHubAppKeyFile = "/nope/app.pem" },
			"PODIUM_AGENT_GITHUB_APP_KEY_FILE"},
		{"relative task url", func(c *Config) { c.TaskURL = "host.docker.internal:8090" },
			"PODIUM_AGENT_TASK_URL"},
		{"empty task url", func(c *Config) { c.TaskURL = "" }, "PODIUM_AGENT_TASK_URL"},
		// The App would be configured and every clone would still fail, at the first
		// command of the turn, with a git error rather than a sentence.
		{"loopback listener", func(c *Config) { c.Listen = DefaultListen }, "PODIUM_AGENT_LISTEN"},
		{"localhost listener", func(c *Config) { c.Listen = "localhost:8090" }, "PODIUM_AGENT_LISTEN"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := withApp(t)
			tc.mut(&cfg)

			err := cfg.Validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

// A key that is not a key is an operator's typo, and Validate is where a typo should cost a
// restart rather than a turn.
func TestGitHubAppRefusesAKeyThatIsNotOne(t *testing.T) {
	cfg := withApp(t)
	require.NoError(t, os.WriteFile(cfg.GitHubAppKeyFile, []byte("ghp_this_is_a_pat"), 0o600))

	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "BEGIN RSA PRIVATE KEY")
}

// The private key mints a token for every repository the App is installed on, which makes
// it the most valuable thing this process holds. It must never be printed.
func TestLoggingAConfigLeaksNoAppKey(t *testing.T) {
	cfg := withApp(t)
	raw, err := os.ReadFile(cfg.GitHubAppKeyFile)
	require.NoError(t, err)
	cfg.GitHubAppKeyFile = ""
	cfg.GitHubAppKey = string(raw)

	var buf bytes.Buffer
	slog.New(slog.NewTextHandler(&buf, nil)).Info("configured", "config", cfg)

	out := buf.String()
	assert.NotContains(t, out, "BEGIN RSA PRIVATE KEY")
	assert.NotContains(t, out, "PRIVATE")
	assert.Contains(t, out, "github_app_key_set=true")
	assert.Contains(t, out, "github_app_id=123456")
}
