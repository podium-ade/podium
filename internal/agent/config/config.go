// Package config is the whole of podium-agent's configuration. It is environment only, the
// same rule the server follows: nothing is read from a file and nothing is discovered.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
)

// DefaultListen is where the conductor serves its API, health and metrics. Loopback,
// because podium-server is the only thing that should reach it and it runs on this host.
const DefaultListen = "127.0.0.1:8090"

// DefaultProfileDir holds profile.yaml, skills/ and prompts/.
const DefaultProfileDir = "/etc/podium/agent"

// ProfileFile is the one file a profile directory must contain.
const ProfileFile = "profile.yaml"

// Config is what podium-agent needs to run. Every field maps to one environment variable.
type Config struct {
	// Server is PODIUM_AGENT_SERVER: the Podium API base URL. Required.
	Server string
	// APIToken is PODIUM_AGENT_API_TOKEN, the dev transport's bearer for the Podium API.
	// Empty is correct on a tailnet, where WhoIs names the caller.
	// SENSITIVE: never log it.
	APIToken string
	// DatabaseURL is PODIUM_AGENT_DATABASE_URL: the conductor's own database, podium_agent.
	// It is never the server's database. Required.
	DatabaseURL string
	// Listen is PODIUM_AGENT_LISTEN, default 127.0.0.1:8090.
	Listen string
	// Token is PODIUM_AGENT_TOKEN: the bearer podium-server presents on proxied
	// AgentService calls. Required. SENSITIVE: never log it.
	Token string
	// ProfileDir is PODIUM_AGENT_PROFILE_DIR, default /etc/podium/agent.
	ProfileDir string
	// SlackAppToken is PODIUM_AGENT_SLACK_APP_TOKEN (xapp-…), the Socket Mode token.
	// SENSITIVE: never log it.
	SlackAppToken string
	// SlackBotToken is PODIUM_AGENT_SLACK_BOT_TOKEN (xoxb-…). SENSITIVE: never log it.
	SlackBotToken string
	// DevSource is PODIUM_AGENT_DEV_SOURCE. TEST ONLY: it mounts an in-process source and
	// two unauthenticated-by-anything-but-the-bearer routes that inject inbound events.
	DevSource bool
}

// FromEnv reads the canonical environment variables and applies the defaults.
func FromEnv() Config {
	return Config{
		Server:        os.Getenv("PODIUM_AGENT_SERVER"),
		APIToken:      os.Getenv("PODIUM_AGENT_API_TOKEN"),
		DatabaseURL:   os.Getenv("PODIUM_AGENT_DATABASE_URL"),
		Listen:        envOr("PODIUM_AGENT_LISTEN", DefaultListen),
		Token:         os.Getenv("PODIUM_AGENT_TOKEN"),
		ProfileDir:    envOr("PODIUM_AGENT_PROFILE_DIR", DefaultProfileDir),
		SlackAppToken: os.Getenv("PODIUM_AGENT_SLACK_APP_TOKEN"),
		SlackBotToken: os.Getenv("PODIUM_AGENT_SLACK_BOT_TOKEN"),
		DevSource:     envBool("PODIUM_AGENT_DEV_SOURCE"),
	}
}

// SlackEnabled reports whether both Slack tokens are present. Validate has already
// rejected exactly one of them.
func (c Config) SlackEnabled() bool {
	return c.SlackAppToken != "" && c.SlackBotToken != ""
}

// Validate reports the first thing that would stop the conductor from starting.
func (c Config) Validate() error {
	if c.Server == "" {
		return errors.New("PODIUM_AGENT_SERVER is required: the Podium API base URL")
	}
	u, err := url.Parse(c.Server)
	if err != nil {
		return fmt.Errorf("PODIUM_AGENT_SERVER=%q is not a URL: %w", c.Server, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("PODIUM_AGENT_SERVER=%q must be an absolute http:// or https:// URL", c.Server)
	}
	if u.Host == "" {
		return fmt.Errorf("PODIUM_AGENT_SERVER=%q has no host", c.Server)
	}
	// Over plain HTTP the Podium API is the dev transport, whose only credential is the
	// shared token. Over HTTPS it is a tailnet, where WhoIs names the caller and a token is
	// optional.
	if u.Scheme == "http" && c.APIToken == "" {
		return errors.New("PODIUM_AGENT_API_TOKEN is required when PODIUM_AGENT_SERVER is http:// " +
			"(the dev transport has no other credential)")
	}
	if c.DatabaseURL == "" {
		return errors.New("PODIUM_AGENT_DATABASE_URL is required: the conductor's own database, podium_agent")
	}
	if c.Token == "" {
		return errors.New("PODIUM_AGENT_TOKEN is required: the bearer podium-server presents on AgentService calls")
	}
	if c.Listen == "" {
		return errors.New("PODIUM_AGENT_LISTEN is empty")
	}
	switch {
	case c.SlackAppToken != "" && c.SlackBotToken == "":
		return errors.New("PODIUM_AGENT_SLACK_BOT_TOKEN is missing: Socket Mode needs both the " +
			"app-level token (xapp-…) and the bot token (xoxb-…)")
	case c.SlackBotToken != "" && c.SlackAppToken == "":
		return errors.New("PODIUM_AGENT_SLACK_APP_TOKEN is missing: Socket Mode needs both the " +
			"app-level token (xapp-…) and the bot token (xoxb-…)")
	}
	if c.ProfileDir == "" {
		return errors.New("PODIUM_AGENT_PROFILE_DIR is empty")
	}
	info, err := os.Stat(c.ProfileDir)
	switch {
	case err != nil:
		return fmt.Errorf("PODIUM_AGENT_PROFILE_DIR=%q: %w", c.ProfileDir, err)
	case !info.IsDir():
		return fmt.Errorf("PODIUM_AGENT_PROFILE_DIR=%q is not a directory", c.ProfileDir)
	}
	if _, err := os.Stat(filepath.Join(c.ProfileDir, ProfileFile)); err != nil {
		return fmt.Errorf("PODIUM_AGENT_PROFILE_DIR=%q has no %s: %w", c.ProfileDir, ProfileFile, err)
	}
	return nil
}

// LogValue is what slog prints for a Config. It is the RedactForLog discipline applied to a
// struct that holds four credentials: a redacting accessor is the only way a Config reaches
// a log statement, so no log site has to remember which fields are sensitive.
func (c Config) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("server", c.Server),
		slog.String("listen", c.Listen),
		slog.String("profile_dir", c.ProfileDir),
		slog.Bool("api_token_set", c.APIToken != ""),
		slog.Bool("token_set", c.Token != ""),
		slog.Bool("slack", c.SlackEnabled()),
		slog.Bool("dev_source", c.DevSource),
	)
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// envBool treats anything strconv understands as such and everything else as false: the
// same rule internal/server/config.go uses, and for the same reason — a knob that widens
// exposure must not turn itself on because somebody wrote "yes".
func envBool(key string) bool {
	v, err := strconv.ParseBool(os.Getenv(key))
	return err == nil && v
}
