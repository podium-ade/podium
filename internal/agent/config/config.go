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
	"regexp"
	"strconv"
)

// DefaultListen is where the conductor serves its API, health and metrics. Loopback,
// because podium-server is the only thing that should reach it and it runs on this host.
const DefaultListen = "127.0.0.1:8090"

// DefaultProfileDir holds profile.yaml, skills/ and prompts/.
const DefaultProfileDir = "/etc/podium/agent"

// ProfileFile is the one file a profile directory must contain.
const ProfileFile = "profile.yaml"

// DefaultAnthropicBaseURL is where a provider key is validated. It is overridable so a test
// can point validation at an httptest server, and so an install behind an egress proxy can
// name it. It is not a BYOK knob: the provider is still Anthropic.
const DefaultAnthropicBaseURL = "https://api.anthropic.com"

// DefaultMemoryTaskURL is where a TASK CONTAINER reaches Hindsight. It is not where the
// conductor reaches it: a task runs on a node, on its own bridge network, and gets to the
// host through the bridge gateway. Correct when the node runs on the same host as the
// compose stack; with workers elsewhere it has to be an address every node can route to.
const DefaultMemoryTaskURL = "http://host.docker.internal:8888"

// DefaultMemoryBank is the one memory bank every turn shares. Hindsight creates a bank on
// its first write, so nothing provisions this.
const DefaultMemoryBank = "podium"

// bankNameRE constrains the bank name because it becomes a path segment in the MCP URL the
// runtime is handed, and in the REST paths this process builds.
var bankNameRE = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

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
	// AnthropicBaseURL is PODIUM_AGENT_ANTHROPIC_BASE_URL, default
	// https://api.anthropic.com. Only SetProviderKey reads it: the model itself is called
	// from inside a task container, never from this process.
	AnthropicBaseURL string
	// MemoryURL is PODIUM_AGENT_MEMORY_URL: Hindsight's base URL as seen from THIS
	// process. Empty turns memory off entirely — briefs carry no memory block, nothing is
	// retained, readyz does not probe it and the memory RPCs answer FailedPrecondition.
	MemoryURL string
	// MemoryTaskURL is PODIUM_AGENT_MEMORY_TASK_URL: Hindsight's base URL as seen from a
	// TASK CONTAINER, which is a different vantage point. Default DefaultMemoryTaskURL.
	MemoryTaskURL string
	// MemoryBank is PODIUM_AGENT_MEMORY_BANK, default podium. One bank, shared by every
	// turn: there is no per-user or per-skill scoping in this track.
	MemoryBank string
	// MemoryAPIKey is PODIUM_AGENT_MEMORY_API_KEY, the bearer Hindsight requires for both
	// REST and MCP. The conductor also writes it into Podium's secret store at startup so
	// every turn's container gets it. SENSITIVE: never log it.
	MemoryAPIKey string
	// DevSource is PODIUM_AGENT_DEV_SOURCE. TEST ONLY: it mounts an in-process source and
	// two unauthenticated-by-anything-but-the-bearer routes that inject inbound events.
	DevSource bool
}

// FromEnv reads the canonical environment variables and applies the defaults.
func FromEnv() Config {
	return Config{
		Server:           os.Getenv("PODIUM_AGENT_SERVER"),
		APIToken:         os.Getenv("PODIUM_AGENT_API_TOKEN"),
		DatabaseURL:      os.Getenv("PODIUM_AGENT_DATABASE_URL"),
		Listen:           envOr("PODIUM_AGENT_LISTEN", DefaultListen),
		Token:            os.Getenv("PODIUM_AGENT_TOKEN"),
		ProfileDir:       envOr("PODIUM_AGENT_PROFILE_DIR", DefaultProfileDir),
		SlackAppToken:    os.Getenv("PODIUM_AGENT_SLACK_APP_TOKEN"),
		SlackBotToken:    os.Getenv("PODIUM_AGENT_SLACK_BOT_TOKEN"),
		AnthropicBaseURL: envOr("PODIUM_AGENT_ANTHROPIC_BASE_URL", DefaultAnthropicBaseURL),
		MemoryURL:        os.Getenv("PODIUM_AGENT_MEMORY_URL"),
		MemoryTaskURL:    envOr("PODIUM_AGENT_MEMORY_TASK_URL", DefaultMemoryTaskURL),
		MemoryBank:       envOr("PODIUM_AGENT_MEMORY_BANK", DefaultMemoryBank),
		MemoryAPIKey:     os.Getenv("PODIUM_AGENT_MEMORY_API_KEY"),
		DevSource:        envBool("PODIUM_AGENT_DEV_SOURCE"),
	}
}

// SlackEnabled reports whether both Slack tokens are present. Validate has already
// rejected exactly one of them.
func (c Config) SlackEnabled() bool {
	return c.SlackAppToken != "" && c.SlackBotToken != ""
}

// MemoryEnabled reports whether shared memory is configured. Memory is optional: an
// install with no Hindsight runs every turn without one, and says so in the UI.
func (c Config) MemoryEnabled() bool {
	return c.MemoryURL != ""
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
	// Empty means the default: FromEnv already applied it, so an empty value here can only
	// come from a hand-built Config, and the handler defaults it again.
	if c.AnthropicBaseURL != "" {
		u, err := url.Parse(c.AnthropicBaseURL)
		if err != nil {
			return fmt.Errorf("PODIUM_AGENT_ANTHROPIC_BASE_URL=%q is not a URL: %w", c.AnthropicBaseURL, err)
		}
		if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("PODIUM_AGENT_ANTHROPIC_BASE_URL=%q must be an absolute http:// or https:// URL",
				c.AnthropicBaseURL)
		}
	}
	if err := c.validateMemory(); err != nil {
		return err
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
		slog.String("anthropic_base_url", c.AnthropicBaseURL),
		slog.Bool("api_token_set", c.APIToken != ""),
		slog.Bool("token_set", c.Token != ""),
		slog.Bool("slack", c.SlackEnabled()),
		slog.String("memory_url", c.MemoryURL),
		slog.String("memory_task_url", c.MemoryTaskURL),
		slog.String("memory_bank", c.MemoryBank),
		slog.Bool("memory_api_key_set", c.MemoryAPIKey != ""),
		slog.Bool("dev_source", c.DevSource),
	)
}

// validateMemory refuses a half-configured memory: a URL with no key would mean every
// call to Hindsight 401s, and an install that meant to have no memory should have no URL.
func (c Config) validateMemory() error {
	if !c.MemoryEnabled() {
		return nil
	}
	if err := absoluteURL("PODIUM_AGENT_MEMORY_URL", c.MemoryURL); err != nil {
		return err
	}
	if err := absoluteURL("PODIUM_AGENT_MEMORY_TASK_URL", c.MemoryTaskURL); err != nil {
		return err
	}
	if c.MemoryAPIKey == "" {
		return errors.New("PODIUM_AGENT_MEMORY_API_KEY is required when PODIUM_AGENT_MEMORY_URL " +
			"is set: Hindsight has no authentication until it is given a key, so an install " +
			"without one is a memory anybody who can reach the port can read and rewrite")
	}
	if !bankNameRE.MatchString(c.MemoryBank) {
		return fmt.Errorf("PODIUM_AGENT_MEMORY_BANK=%q must match %s: it becomes a path segment "+
			"in the MCP URL every turn is handed", c.MemoryBank, bankNameRE)
	}
	return nil
}

// absoluteURL is the shape check every base URL in this file needs: scheme, host, and no
// path — a base URL with a path silently changes what a client appends to it.
func absoluteURL(name, raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%s=%q is not a URL: %w", name, raw, err)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("%s=%q must be an absolute http:// or https:// URL", name, raw)
	}
	if u.Path != "" && u.Path != "/" {
		return fmt.Errorf("%s=%q must be scheme://host:port with no path", name, raw)
	}
	return nil
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
