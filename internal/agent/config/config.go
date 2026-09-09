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
	"time"

	"github.com/alvaroibarguen/podium/internal/agent/skills"
)

// DefaultListen is where the conductor serves its API, health and metrics. Loopback,
// because podium-server is the only thing that should reach it and it runs on this host.
const DefaultListen = "127.0.0.1:8090"

// DefaultProfileDir holds profile.yaml, playbooks/ and prompts/.
const DefaultProfileDir = "/etc/podium/agent"

// ProfileFile is the one file a profile directory must contain.
const ProfileFile = "profile.yaml"

// DefaultAnthropicBaseURL is where an Anthropic key is validated. It is overridable so a
// test can point validation at an httptest server, and so an install behind an egress proxy
// can name it. It is not a BYOK knob: the provider is still Anthropic.
const DefaultAnthropicBaseURL = "https://api.anthropic.com"

// DefaultXAIBaseURL is the same for xAI. It is also what a Grok turn's container is told to
// point the agent SDK at, because xAI serves an Anthropic-shaped /v1/messages there.
const DefaultXAIBaseURL = "https://api.x.ai"

// DefaultXAIOAuthIssuer is the OIDC issuer a subscription sign-in discovers its endpoints
// from. The endpoints themselves are never hard-coded: they come from
// {issuer}/.well-known/openid-configuration and are checked back against this host.
const DefaultXAIOAuthIssuer = "https://auth.x.ai"

// DefaultXAIOAuthScopes is what a sign-in asks for.
//
// Two of these carry weight. offline_access is what makes the provider issue a refresh
// token; without it a human signs in again every hour. grok-cli:access is what xAI's own
// CLI asks for, and the reports of the OAuth surface answering 403 to otherwise valid
// subscribers point at the scope set rather than the subscription — so it is asked for too.
const DefaultXAIOAuthScopes = "openid profile email offline_access grok-cli:access api:access"

// DefaultMemoryTaskURL is where a TASK CONTAINER reaches Hindsight. It is not where the
// conductor reaches it: a task runs on a node, on its own bridge network, and gets to the
// host through the bridge gateway. Correct when the node runs on the same host as the
// compose stack; with workers elsewhere it has to be an address every node can route to.
const DefaultMemoryTaskURL = "http://host.docker.internal:8888"

// DefaultMemoryBank is the one memory bank every turn shares. Hindsight creates a bank on
// its first write, so nothing provisions this.
const DefaultMemoryBank = "podium"

// DefaultLinearURL is Linear's GraphQL endpoint. It is overridable so a test can point the
// source at an httptest stub and so an install behind an egress proxy can name one; it is
// not a "which Linear" knob.
const DefaultLinearURL = "https://api.linear.app/graphql"

// DefaultLinearPollInterval is how often the Linear source asks for issues that changed.
// Linear bills 2,500 requests an hour against a personal API key, so 30s of one query is
// two orders of magnitude inside the budget.
const DefaultLinearPollInterval = 30 * time.Second

// MinLinearPollInterval is the floor. Polling harder than this buys nothing a human would
// notice and spends a quota shared by every key that user owns.
const MinLinearPollInterval = 10 * time.Second

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
	// SkillsDir is PODIUM_AGENT_SKILLS_DIR: a directory on THIS host holding one
	// subdirectory per Agent Skill, each with a SKILL.md. It has no default. Empty means
	// this conductor delivers no skills at all, and a playbook that names one fails its
	// turns saying so — which is the right answer for an install that has never heard of
	// skills, and a loud one for an install that meant to set this.
	SkillsDir string
	// HostRuntime is PODIUM_AGENT_HOST_RUNTIME: the built agent runtime's entrypoint on
	// THIS host (agent/runtime/dist/main.js). It has no default, and empty is what turns
	// host turns off — every turn is then a task, which is what a conductor whose host has
	// no Node and no harness must do. Set, it is what answers a conversation, and a
	// container is what that conversation delegates to. See internal/agent/conductor/host.go.
	HostRuntime string
	// HostMaxTurns is PODIUM_AGENT_HOST_MAX_TURNS: how many turns this host answers at
	// once. Zero means the conductor's own default. A host turn is a process on this
	// machine, and with Slack threads answered here too it is a channel's traffic that
	// decides how many conversations there are.
	HostMaxTurns int
	// HostNode is PODIUM_AGENT_HOST_NODE, the node binary that runs HostRuntime. Default
	// "node", found on PATH.
	HostNode string
	// RunnerBin is PODIUM_AGENT_RUNNER_BIN: podium-runner on this host. A host turn has no
	// node to bind-mount one in, and it is how the runtime says anything at all, so a host
	// runtime without it cannot run.
	RunnerBin string
	// HostDir is PODIUM_AGENT_HOST_DIR, the directory a host turn's own HOME, working
	// directory and event socket are made under. Default: the OS temp directory.
	HostDir string
	// SlackAppToken is PODIUM_AGENT_SLACK_APP_TOKEN (xapp-…), the Socket Mode token.
	// SENSITIVE: never log it.
	SlackAppToken string
	// SlackBotToken is PODIUM_AGENT_SLACK_BOT_TOKEN (xoxb-…). SENSITIVE: never log it.
	SlackBotToken string
	// AnthropicBaseURL is PODIUM_AGENT_ANTHROPIC_BASE_URL, default
	// https://api.anthropic.com. Only SetProviderKey reads it: the model itself is called
	// from inside a task container, never from this process.
	AnthropicBaseURL string
	// XAIBaseURL is PODIUM_AGENT_XAI_BASE_URL, default https://api.x.ai. Unlike the
	// Anthropic one this is read twice: SetProviderKey validates against it, and a Grok
	// turn's brief carries it so the container knows where to send the SDK's requests.
	XAIBaseURL string
	// XAIOAuthIssuer is PODIUM_AGENT_XAI_OAUTH_ISSUER, default https://auth.x.ai.
	XAIOAuthIssuer string
	// XAIOAuthClientID is PODIUM_AGENT_XAI_OAUTH_CLIENT_ID: the OAuth client id of a public
	// desktop client registered with xAI. Empty — the default — turns the subscription
	// sign-in off and leaves the API key path, which is a supported configuration. It is
	// public OAuth client metadata and not a secret, so it is logged like any other field.
	XAIOAuthClientID string
	// XAIOAuthScopes is PODIUM_AGENT_XAI_OAUTH_SCOPES, default DefaultXAIOAuthScopes.
	XAIOAuthScopes string
	// MemoryURL is PODIUM_AGENT_MEMORY_URL: Hindsight's base URL as seen from THIS
	// process. Empty turns memory off entirely — briefs carry no memory block, nothing is
	// retained, readyz does not probe it and the memory RPCs answer FailedPrecondition.
	MemoryURL string
	// MemoryTaskURL is PODIUM_AGENT_MEMORY_TASK_URL: Hindsight's base URL as seen from a
	// TASK CONTAINER, which is a different vantage point. Default DefaultMemoryTaskURL.
	MemoryTaskURL string
	// MemoryBank is PODIUM_AGENT_MEMORY_BANK, default podium. One bank, shared by every
	// turn: there is no per-user or per-playbook scoping in this track.
	MemoryBank string
	// MemoryAPIKey is PODIUM_AGENT_MEMORY_API_KEY, the bearer Hindsight requires for both
	// REST and MCP. The conductor also writes it into Podium's secret store at startup so
	// every turn's container gets it. SENSITIVE: never log it.
	MemoryAPIKey string
	// LinearAPIKey is PODIUM_AGENT_LINEAR_API_KEY: a PERSONAL API key belonging to the bot
	// user, sent bare in Authorization (Linear reserves Bearer for OAuth tokens). Empty
	// turns the Linear source off entirely. SENSITIVE: never log it.
	LinearAPIKey string
	// LinearURL is PODIUM_AGENT_LINEAR_URL, default DefaultLinearURL.
	LinearURL string
	// LinearPollInterval is PODIUM_AGENT_LINEAR_POLL_INTERVAL, default 30s, floor 10s.
	// Linear is polled and never listened to: the conductor dials out, like every other
	// thing on this host.
	LinearPollInterval time.Duration
	// UIURL is PODIUM_AGENT_UI_URL: the Podium web UI as a HUMAN reaches it, which is not
	// always how this process reaches the API (a tailnet name, a reverse proxy). Default
	// Server. It is used only to build the fallback link to a task page when an
	// attachment cannot be uploaded into the conversation.
	UIURL string
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
		SkillsDir:        os.Getenv(skills.DirEnv),
		HostRuntime:      os.Getenv("PODIUM_AGENT_HOST_RUNTIME"),
		HostMaxTurns:     envInt("PODIUM_AGENT_HOST_MAX_TURNS"),
		HostNode:         envOr("PODIUM_AGENT_HOST_NODE", "node"),
		RunnerBin:        os.Getenv("PODIUM_AGENT_RUNNER_BIN"),
		HostDir:          os.Getenv("PODIUM_AGENT_HOST_DIR"),
		SlackAppToken:    os.Getenv("PODIUM_AGENT_SLACK_APP_TOKEN"),
		SlackBotToken:    os.Getenv("PODIUM_AGENT_SLACK_BOT_TOKEN"),
		AnthropicBaseURL: envOr("PODIUM_AGENT_ANTHROPIC_BASE_URL", DefaultAnthropicBaseURL),
		XAIBaseURL:       envOr("PODIUM_AGENT_XAI_BASE_URL", DefaultXAIBaseURL),
		XAIOAuthIssuer:   envOr("PODIUM_AGENT_XAI_OAUTH_ISSUER", DefaultXAIOAuthIssuer),
		XAIOAuthClientID: os.Getenv("PODIUM_AGENT_XAI_OAUTH_CLIENT_ID"),
		XAIOAuthScopes:   envOr("PODIUM_AGENT_XAI_OAUTH_SCOPES", DefaultXAIOAuthScopes),
		MemoryURL:        os.Getenv("PODIUM_AGENT_MEMORY_URL"),
		MemoryTaskURL:    envOr("PODIUM_AGENT_MEMORY_TASK_URL", DefaultMemoryTaskURL),
		MemoryBank:       envOr("PODIUM_AGENT_MEMORY_BANK", DefaultMemoryBank),
		MemoryAPIKey:     os.Getenv("PODIUM_AGENT_MEMORY_API_KEY"),
		LinearAPIKey:     os.Getenv("PODIUM_AGENT_LINEAR_API_KEY"),
		LinearURL:        envOr("PODIUM_AGENT_LINEAR_URL", DefaultLinearURL),
		// A malformed duration is left at zero here and named by Validate, which is where
		// every other bad value is reported too.
		LinearPollInterval: envDuration("PODIUM_AGENT_LINEAR_POLL_INTERVAL", DefaultLinearPollInterval),
		UIURL:              os.Getenv("PODIUM_AGENT_UI_URL"),
		DevSource:          envBool("PODIUM_AGENT_DEV_SOURCE"),
	}
}

// SlackEnabled reports whether both Slack tokens are present. Validate has already
// rejected exactly one of them.
func (c Config) SlackEnabled() bool {
	return c.SlackAppToken != "" && c.SlackBotToken != ""
}

// LinearEnabled reports whether the Linear source should be started. One variable turns it
// on: an API key belonging to the bot user.
func (c Config) LinearEnabled() bool { return c.LinearAPIKey != "" }

// WebURL is the base URL a human uses for the Podium web UI. PODIUM_AGENT_UI_URL when set,
// the API base URL otherwise — which is right for every dev install and wrong for exactly
// the case the variable exists for.
func (c Config) WebURL() string {
	if c.UIURL != "" {
		return c.UIURL
	}
	return c.Server
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
	for _, u := range []struct{ name, value string }{
		{"PODIUM_AGENT_ANTHROPIC_BASE_URL", c.AnthropicBaseURL},
		{"PODIUM_AGENT_XAI_BASE_URL", c.XAIBaseURL},
		{"PODIUM_AGENT_XAI_OAUTH_ISSUER", c.XAIOAuthIssuer},
	} {
		if u.value == "" {
			continue
		}
		if err := absoluteURL(u.name, u.value); err != nil {
			return err
		}
	}
	if err := c.validateMemory(); err != nil {
		return err
	}
	if err := c.validateLinear(); err != nil {
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
	// A directory that is set and is not there is a typo, and every turn of every playbook
	// that names a skill would fail on it. It is cheaper to say so at start-up.
	if c.SkillsDir != "" {
		info, err := os.Stat(c.SkillsDir)
		switch {
		case err != nil:
			return fmt.Errorf("%s=%q: %w", skills.DirEnv, c.SkillsDir, err)
		case !info.IsDir():
			return fmt.Errorf("%s=%q is not a directory", skills.DirEnv, c.SkillsDir)
		}
	}
	// Same reasoning as the skills directory, and a stronger case for it: with a host
	// runtime configured, EVERY conversation runs on it, so a path that is not there is
	// every turn failing rather than some of them.
	if c.HostMaxTurns < 0 {
		return errors.New("PODIUM_AGENT_HOST_MAX_TURNS must be a whole number of turns")
	}
	if c.HostRuntime != "" {
		if _, err := os.Stat(c.HostRuntime); err != nil {
			return fmt.Errorf("PODIUM_AGENT_HOST_RUNTIME=%q: %w", c.HostRuntime, err)
		}
		if c.RunnerBin == "" {
			return errors.New("PODIUM_AGENT_HOST_RUNTIME needs PODIUM_AGENT_RUNNER_BIN: " +
				"a host turn has no node to bind-mount podium-runner in, and it is how the " +
				"runtime says anything at all")
		}
		if _, err := os.Stat(c.RunnerBin); err != nil {
			return fmt.Errorf("PODIUM_AGENT_RUNNER_BIN=%q: %w", c.RunnerBin, err)
		}
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
		slog.String("skills_dir", c.SkillsDir),
		slog.String("host_runtime", c.HostRuntime),
		slog.Int("host_max_turns", c.HostMaxTurns),
		slog.String("anthropic_base_url", c.AnthropicBaseURL),
		slog.String("xai_base_url", c.XAIBaseURL),
		slog.String("xai_oauth_issuer", c.XAIOAuthIssuer),
		// Public OAuth client metadata, not a secret: it is the one thing an operator needs
		// to see to know why the sign-in button is disabled.
		slog.String("xai_oauth_client_id", c.XAIOAuthClientID),
		slog.Bool("api_token_set", c.APIToken != ""),
		slog.Bool("token_set", c.Token != ""),
		slog.Bool("slack", c.SlackEnabled()),
		slog.String("memory_url", c.MemoryURL),
		slog.String("memory_task_url", c.MemoryTaskURL),
		slog.String("memory_bank", c.MemoryBank),
		slog.Bool("memory_api_key_set", c.MemoryAPIKey != ""),
		slog.Bool("linear", c.LinearEnabled()),
		slog.String("linear_url", c.LinearURL),
		slog.Duration("linear_poll_interval", c.LinearPollInterval),
		slog.String("ui_url", c.WebURL()),
		slog.Bool("dev_source", c.DevSource),
	)
}

// validateLinear checks the endpoint and the interval. An empty value means the default:
// FromEnv has already applied it, so an empty one here can only come from a hand-built
// Config, and it only matters when a key makes the source start.
func (c Config) validateLinear() error {
	if c.LinearURL != "" {
		u, err := url.Parse(c.LinearURL)
		if err != nil {
			return fmt.Errorf("PODIUM_AGENT_LINEAR_URL=%q is not a URL: %w", c.LinearURL, err)
		}
		if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("PODIUM_AGENT_LINEAR_URL=%q must be an absolute http:// or https:// "+
				"URL ending in the GraphQL path (default %s)", c.LinearURL, DefaultLinearURL)
		}
	}
	switch {
	case c.LinearPollInterval < 0:
		// envDuration reports an unparseable value this way, so the name is reported here
		// rather than the process running on a number nobody chose.
		return errors.New("PODIUM_AGENT_LINEAR_POLL_INTERVAL is not a duration: write it as " +
			"30s, 2m or 1h")
	case c.LinearPollInterval > 0 && c.LinearPollInterval < MinLinearPollInterval:
		return fmt.Errorf("PODIUM_AGENT_LINEAR_POLL_INTERVAL=%s is below the %s floor: Linear's "+
			"per-hour request budget is shared by every key the bot user owns",
			c.LinearPollInterval, MinLinearPollInterval)
	}
	if c.LinearEnabled() {
		if c.LinearURL == "" {
			return errors.New("PODIUM_AGENT_LINEAR_URL is empty")
		}
		if c.LinearPollInterval == 0 {
			return errors.New("PODIUM_AGENT_LINEAR_POLL_INTERVAL is empty")
		}
	}
	if c.UIURL != "" {
		if err := absoluteURL("PODIUM_AGENT_UI_URL", c.UIURL); err != nil {
			return err
		}
	}
	return nil
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
// envDuration parses a Go duration, falling back to the default when the variable is
// absent. An unparseable value becomes -1 rather than the default, so Validate names it
// instead of the process quietly running on a number nobody asked for.
func envDuration(key string, fallback time.Duration) time.Duration {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return -1
	}
	return d
}

// envInt parses a count, 0 when the variable is absent. An unparseable value becomes -1
// rather than 0, so Validate names it instead of the process quietly running on a default
// nobody asked for — the same rule envDuration follows.
func envInt(key string) int {
	raw := os.Getenv(key)
	if raw == "" {
		return 0
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return -1
	}
	return n
}

func envBool(key string) bool {
	v, err := strconv.ParseBool(os.Getenv(key))
	return err == nil && v
}
