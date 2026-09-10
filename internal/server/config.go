package server

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/podium-ade/podium/internal/server/artifacts"
	"github.com/podium-ade/podium/internal/server/logs"
	"github.com/podium-ade/podium/internal/transport/local"
	"github.com/podium-ade/podium/internal/transport/tailnet"
)

// The transports PODIUM_TRANSPORT accepts.
const (
	// TransportLocal is the shared bearer-token transport: one machine, no Tailscale.
	TransportLocal = "local"
	// TransportTailnet embeds a Tailscale device (tsnet) and serves HTTPS on its MagicDNS name.
	TransportTailnet = "tailnet"
	// TransportHost uses the machine's existing tailscaled instead of embedding a device.
	TransportHost = "host"
)

// Config is the whole of podium-server's configuration. The names are the canonical ones.
type Config struct {
	// DatabaseURL is PODIUM_DATABASE_URL. Required.
	DatabaseURL string
	// Transport is PODIUM_TRANSPORT: dev (default), tailnet or host.
	Transport string
	// LocalListen is PODIUM_LOCAL_LISTEN, default 127.0.0.1:8080. It must be loopback.
	LocalListen string
	// LocalToken is PODIUM_LOCAL_TOKEN, the shared bearer token of the local transport.
	LocalToken string
	// LocalAllowUnsafeListen is PODIUM_LOCAL_ALLOW_UNSAFE_LISTEN: let LocalListen bind an address
	// that is not loopback. A container deployment needs it — loopback inside a container is
	// the container's own, so nothing could reach the server — and the published port is the
	// boundary there. On a host it publishes the whole API to anything that can route to the
	// address, guarded by one static token, so the server warns loudly when it is on.
	LocalAllowUnsafeListen bool

	// MasterKeyFile is PODIUM_MASTER_KEY_FILE: the file holding the 32-byte AES-256 key
	// every stored secret is encrypted under. The file must not be readable by other
	// accounts on the machine or the server refuses to start. Empty disables secrets.
	MasterKeyFile string
	// MasterKey is PODIUM_MASTER_KEY, the same key inline. It is a development
	// convenience — an environment variable is visible in /proc and in `docker inspect` —
	// and the server warns loudly when it is used. MasterKeyFile wins if both are set.
	// SENSITIVE: never log it.
	MasterKey string

	// TSHostname is PODIUM_TS_HOSTNAME: the Tailscale device name, and therefore the first
	// label of the MagicDNS name the server is reached at.
	TSHostname string
	// TSStateDir is PODIUM_TS_STATE_DIR: where tsnet keeps the device's node key. It must
	// persist across restarts or the server registers a new device every time.
	TSStateDir string
	// TSAuthKey is TS_AUTHKEY, read on the first run only. This is the *Tailscale* auth key —
	// reusable, pre-approved, tagged tag:podium-server — not a Podium enrollment token.
	// SENSITIVE: never log it.
	TSAuthKey string
	// TSRequiredNodeTag is PODIUM_TS_REQUIRED_NODE_TAG: the ACL tag a device must carry to be
	// treated as a worker.
	TSRequiredNodeTag string
	// TSAllowUntaggedNodes is PODIUM_TS_ALLOW_UNTAGGED_NODES: let an untagged tailnet device
	// enroll as a node. It removes the network-level proof that a caller is an authorised
	// worker and exists only for a tailnet that has no ACL tags yet.
	TSAllowUntaggedNodes bool

	// AgentURL is PODIUM_AGENT_URL: where podium-agent, the conductor, listens. Setting it
	// makes the server reverse-proxy /podium.agent.v1.AgentService/ to that address behind
	// its own identity middleware, which is the only way a browser reaches the conductor.
	// Empty means this control plane has no conductor and nothing is mounted.
	AgentURL string
	// AgentToken is PODIUM_AGENT_TOKEN: the bearer the proxy presents to the conductor. It
	// is also the conductor's proof that a proxied request came through this server, which
	// is what makes the X-Podium-Login header trustworthy at the other end.
	// SENSITIVE: never log it.
	AgentToken string

	// S3 is the PODIUM_S3_* object store: where artifacts and rolled-up logs live. An
	// empty endpoint disables artifacts entirely, which is a supported configuration —
	// a task does not need artifacts to run.
	S3 artifacts.Config
	// Rollup is the log roll-up schedule.
	Rollup logs.RollupConfig
}

// ConfigFromEnv reads the canonical environment variables and applies the defaults.
func ConfigFromEnv() Config {
	return Config{
		DatabaseURL:            os.Getenv("PODIUM_DATABASE_URL"),
		Transport:              envOr("PODIUM_TRANSPORT", TransportLocal),
		LocalListen:            envOr("PODIUM_LOCAL_LISTEN", local.DefaultListen),
		LocalToken:             os.Getenv("PODIUM_LOCAL_TOKEN"),
		LocalAllowUnsafeListen: envBool(local.UnsafeListenVar),
		MasterKeyFile:          os.Getenv("PODIUM_MASTER_KEY_FILE"),
		MasterKey:              os.Getenv("PODIUM_MASTER_KEY"),
		TSHostname:             envOr("PODIUM_TS_HOSTNAME", tailnet.DefaultHostname),
		TSStateDir:             envOr("PODIUM_TS_STATE_DIR", tailnet.DefaultStateDir),
		TSAuthKey:              os.Getenv("TS_AUTHKEY"),
		TSRequiredNodeTag:      envOr("PODIUM_TS_REQUIRED_NODE_TAG", tailnet.DefaultNodeTag),
		TSAllowUntaggedNodes:   envBool("PODIUM_TS_ALLOW_UNTAGGED_NODES"),
		AgentURL:               os.Getenv("PODIUM_AGENT_URL"),
		AgentToken:             os.Getenv("PODIUM_AGENT_TOKEN"),
		S3:                     artifacts.ConfigFromEnv(),
		Rollup:                 logs.RollupConfigFromEnv(),
	}
}

// Validate reports the first thing that would stop the server from starting.
func (c Config) Validate() error {
	if c.DatabaseURL == "" {
		return errors.New("PODIUM_DATABASE_URL is required")
	}
	switch c.Transport {
	case TransportLocal:
		if c.LocalToken == "" {
			return errors.New("PODIUM_LOCAL_TOKEN is required for PODIUM_TRANSPORT=local")
		}
		if err := local.CheckListen(c.LocalListen, c.LocalAllowUnsafeListen); err != nil {
			return err
		}
	case TransportTailnet:
		if c.TSHostname == "" {
			return errors.New("PODIUM_TS_HOSTNAME is empty: it is the device name, and the first label of the MagicDNS name")
		}
		if c.TSStateDir == "" {
			return errors.New("PODIUM_TS_STATE_DIR is empty: tsnet needs a directory that survives restarts")
		}
	case TransportHost:
		// The host's tailscaled supplies the identity, the address and the certificate;
		// there is nothing left to configure.
	default:
		return fmt.Errorf("PODIUM_TRANSPORT=%q is not a transport (want %s, %s or %s)",
			c.Transport, TransportLocal, TransportTailnet, TransportHost)
	}
	if err := c.validateAgent(); err != nil {
		return err
	}
	return c.S3.Validate()
}

// AgentEnabled reports whether this control plane proxies the conductor's API.
func (c Config) AgentEnabled() bool { return c.AgentURL != "" }

// validateAgent refuses a half-configured proxy. A proxy that forwards an unauthenticated
// request into the conductor is worse than no proxy: the conductor's bearer is the whole
// reason it may trust the login header the proxy sets.
func (c Config) validateAgent() error {
	if c.AgentURL == "" {
		return nil
	}
	u, err := url.Parse(c.AgentURL)
	if err != nil {
		return fmt.Errorf("PODIUM_AGENT_URL=%q is not a URL: %w", c.AgentURL, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("PODIUM_AGENT_URL=%q must be an absolute http:// or https:// URL", c.AgentURL)
	}
	if u.Host == "" {
		return fmt.Errorf("PODIUM_AGENT_URL=%q has no host", c.AgentURL)
	}
	// The proxy owns the path: it forwards the Connect procedure path verbatim, so a base
	// URL with a path of its own would silently produce /prefix/podium.agent.v1…
	if p := strings.TrimSuffix(u.Path, "/"); p != "" || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("PODIUM_AGENT_URL=%q must be scheme://host:port with no path or query: "+
			"the proxy forwards the Connect procedure path itself", c.AgentURL)
	}
	if c.AgentToken == "" {
		return errors.New("PODIUM_AGENT_TOKEN is required when PODIUM_AGENT_URL is set: " +
			"a proxy that forwards an unauthenticated request into the conductor is worse than no proxy")
	}
	return nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// envBool treats anything strconv understands as such and everything else as false: a knob that
// weakens authentication should not turn itself on because someone wrote "yes".
func envBool(key string) bool {
	v, err := strconv.ParseBool(os.Getenv(key))
	return err == nil && v
}
