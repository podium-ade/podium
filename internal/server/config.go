package server

import (
	"errors"
	"fmt"
	"os"
	"strconv"

	"github.com/alvaroibarguen/podium/internal/server/artifacts"
	"github.com/alvaroibarguen/podium/internal/server/logs"
	"github.com/alvaroibarguen/podium/internal/transport/dev"
	"github.com/alvaroibarguen/podium/internal/transport/tailnet"
)

// The transports PODIUM_TRANSPORT accepts.
const (
	// TransportDev is the loopback bearer-token transport: one machine, no Tailscale.
	TransportDev = "dev"
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
	// DevListen is PODIUM_DEV_LISTEN, default 127.0.0.1:8080. It must be loopback.
	DevListen string
	// DevToken is PODIUM_DEV_TOKEN, the shared bearer token of the dev transport.
	DevToken string

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
		DatabaseURL:          os.Getenv("PODIUM_DATABASE_URL"),
		Transport:            envOr("PODIUM_TRANSPORT", TransportDev),
		DevListen:            envOr("PODIUM_DEV_LISTEN", dev.DefaultListen),
		DevToken:             os.Getenv("PODIUM_DEV_TOKEN"),
		MasterKeyFile:        os.Getenv("PODIUM_MASTER_KEY_FILE"),
		MasterKey:            os.Getenv("PODIUM_MASTER_KEY"),
		TSHostname:           envOr("PODIUM_TS_HOSTNAME", tailnet.DefaultHostname),
		TSStateDir:           envOr("PODIUM_TS_STATE_DIR", tailnet.DefaultStateDir),
		TSAuthKey:            os.Getenv("TS_AUTHKEY"),
		TSRequiredNodeTag:    envOr("PODIUM_TS_REQUIRED_NODE_TAG", tailnet.DefaultNodeTag),
		TSAllowUntaggedNodes: envBool("PODIUM_TS_ALLOW_UNTAGGED_NODES"),
		S3:                   artifacts.ConfigFromEnv(),
		Rollup:               logs.RollupConfigFromEnv(),
	}
}

// Validate reports the first thing that would stop the server from starting.
func (c Config) Validate() error {
	if c.DatabaseURL == "" {
		return errors.New("PODIUM_DATABASE_URL is required")
	}
	switch c.Transport {
	case TransportDev:
		if c.DevToken == "" {
			return errors.New("PODIUM_DEV_TOKEN is required for PODIUM_TRANSPORT=dev")
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
			c.Transport, TransportDev, TransportTailnet, TransportHost)
	}
	return c.S3.Validate()
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
