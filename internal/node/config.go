// Package node is the podium-node daemon: it holds the node's identity, keeps one
// bidirectional stream to the control plane, and turns each Assign into a container run
// whose events are batched, buffered and replayed until the server acks them.
package node

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	yaml "go.yaml.in/yaml/v3"

	"github.com/alvaroibarguen/podium/internal/transport/tailnet"
)

// DefaultConfigPath is where podium-node looks for its config file when --config is not
// given. A missing file there is not an error: the daemon is fully configurable by
// environment.
const DefaultConfigPath = "/etc/podium/node.yaml"

// Config defaults. Mirrored in docs/node-setup.md; keep the two in step.
const (
	DefaultServer                  = "http://127.0.0.1:8080"
	DefaultTransport               = "dev"
	DefaultDataDir                 = "/var/lib/podium-node"
	DefaultMaxTasks                = 4
	DefaultImageCacheHighWatermark = 0.80
	DefaultMetricsListen           = "127.0.0.1:9091"
)

// The transports podium-node understands.
const (
	// TransportDev dials a loopback server with a shared bearer token.
	TransportDev = "dev"
	// TransportTailnet embeds the node's own Tailscale device (tsnet) and dials the control
	// plane's MagicDNS name over it. The node listens for nothing.
	TransportTailnet = "tailnet"
	// TransportHost dials over the machine's existing tailscaled instead of embedding a device.
	TransportHost = "host"
)

// Config is the whole of podium-node's configuration: /etc/podium/node.yaml overlaid by
// PODIUM_NODE_* environment variables, which in turn are overlaid by flags.
type Config struct {
	// Server is the control plane base URL, PODIUM_NODE_SERVER.
	Server string `yaml:"server"`
	// Transport is PODIUM_NODE_TRANSPORT: dev only in MVP-0.
	Transport string `yaml:"transport"`
	// DataDir holds the node identity and each task's state, PODIUM_NODE_DATA_DIR.
	DataDir string `yaml:"data_dir"`
	// Labels are advertised at enrollment and used for scheduling, PODIUM_NODE_LABELS
	// (comma-separated).
	Labels []string `yaml:"labels"`
	// MaxTasks is the concurrency budget the scheduler assigns against,
	// PODIUM_NODE_MAX_TASKS. Zero would mean this node never gets work.
	MaxTasks int `yaml:"max_tasks"`
	// EnrollToken is consumed on the first run only, PODIUM_NODE_ENROLL_TOKEN.
	EnrollToken string `yaml:"enroll_token"`
	// DevToken is the shared bearer token of the dev transport, PODIUM_NODE_DEV_TOKEN.
	DevToken string `yaml:"dev_token"`
	// TSAuthKey is the *Tailscale* auth key, PODIUM_NODE_TS_AUTHKEY or TS_AUTHKEY: reusable,
	// pre-approved, tagged tag:podium-node. It is read on the first run only, and it is a
	// different thing from EnrollToken, which is Podium's own single-use secret.
	// SENSITIVE: never log it.
	TSAuthKey string `yaml:"ts_auth_key"`
	// TSHostname overrides the Tailscale device name, PODIUM_NODE_TS_HOSTNAME. Empty derives
	// it from the machine's hostname.
	TSHostname string `yaml:"ts_hostname"`
	// ImageCachePrune turns on the LRU image cache prune, PODIUM_NODE_IMAGE_CACHE_PRUNE.
	// It is off by default and must be turned on deliberately: a developer running a node
	// on a laptop shares that Docker engine with the rest of their work, and no amount of
	// disk pressure justifies deleting an image out from under it. See docs/node-setup.md.
	ImageCachePrune bool `yaml:"image_cache_prune"`
	// ImageCacheHighWatermark is the disk-usage fraction above which pruning starts, when
	// pruning is enabled at all: PODIUM_NODE_IMAGE_CACHE_HIGH_WATERMARK.
	ImageCacheHighWatermark float64 `yaml:"image_cache_high_watermark"`
	// MetricsListen serves /healthz, /readyz and /metrics, PODIUM_NODE_METRICS_LISTEN.
	MetricsListen string `yaml:"metrics_listen"`
	// DockerHost overrides the engine endpoint, PODIUM_NODE_DOCKER_HOST. Empty means
	// the usual DOCKER_HOST / docker context / default socket resolution.
	DockerHost string `yaml:"docker_host"`
	// ExitOnDrain makes the daemon exit 0 once a drain has been requested and the last
	// running task has finished, PODIUM_NODE_EXIT_ON_DRAIN or --exit-on-drain. It is the
	// upgrade path: a supervisor restarts the process on the new binary. Without it a
	// drained node stays connected and idle, which is what an operator taking a machine
	// out of service for maintenance wants.
	ExitOnDrain bool `yaml:"exit_on_drain"`
	// AllowPrivilegedSidecars honours a spec's `privileged: true` on a sidecar,
	// PODIUM_NODE_ALLOW_PRIVILEGED_SIDECARS or --allow-privileged-sidecars. It is off by
	// default because such a container is root on this machine's kernel: none of the
	// sandbox every other task runs under applies to it, and a spec author must not be
	// able to opt into that from a YAML file. It is what a docker-in-docker sidecar
	// needs. Turn it on only on a machine dedicated to that, and pair it with a label
	// (--labels privileged) so the specs that need it are the only ones that land here.
	// See docs/security.md.
	AllowPrivilegedSidecars bool `yaml:"allow_privileged_sidecars"`
}

// DefaultConfig is the configuration a node with no file and no environment runs with.
func DefaultConfig() Config {
	return Config{
		Server:                  DefaultServer,
		Transport:               DefaultTransport,
		DataDir:                 DefaultDataDir,
		MaxTasks:                DefaultMaxTasks,
		ImageCacheHighWatermark: DefaultImageCacheHighWatermark,
		MetricsListen:           DefaultMetricsListen,
	}
}

// LoadConfig reads path (or DefaultConfigPath when path is empty) over the defaults and
// then applies the environment. A file that was explicitly asked for must exist; the
// default one need not.
func LoadConfig(path string) (Config, error) {
	cfg := DefaultConfig()
	explicit := path != ""
	if !explicit {
		path = DefaultConfigPath
	}

	raw, err := os.ReadFile(path) //nolint:gosec // the operator names their own config file
	switch {
	case err == nil:
		if err := yaml.Unmarshal(raw, &cfg); err != nil {
			return Config{}, fmt.Errorf("parse node config %s: %w", path, err)
		}
	case errors.Is(err, os.ErrNotExist) && !explicit:
		// Environment-only configuration is a supported deployment.
	default:
		return Config{}, fmt.Errorf("read node config %s: %w", path, err)
	}

	applyEnv(&cfg)
	return cfg, nil
}

// applyEnv overlays the PODIUM_NODE_* variables. An unset or empty variable leaves the
// file's value alone, so a config file and a partial environment compose.
func applyEnv(cfg *Config) {
	envString("PODIUM_NODE_SERVER", &cfg.Server)
	envString("PODIUM_NODE_TRANSPORT", &cfg.Transport)
	envString("PODIUM_NODE_DATA_DIR", &cfg.DataDir)
	envString("PODIUM_NODE_ENROLL_TOKEN", &cfg.EnrollToken)
	envString("PODIUM_NODE_DEV_TOKEN", &cfg.DevToken)
	envString("PODIUM_NODE_TS_HOSTNAME", &cfg.TSHostname)
	// TS_AUTHKEY is the name tsnet itself documents, so it is honoured as a fallback; the
	// PODIUM_NODE_ prefixed name wins when both are set.
	envString("TS_AUTHKEY", &cfg.TSAuthKey)
	envString("PODIUM_NODE_TS_AUTHKEY", &cfg.TSAuthKey)
	envString("PODIUM_NODE_METRICS_LISTEN", &cfg.MetricsListen)
	envString("PODIUM_NODE_DOCKER_HOST", &cfg.DockerHost)

	if v := os.Getenv("PODIUM_NODE_LABELS"); v != "" {
		cfg.Labels = splitLabels(v)
	}
	if v := os.Getenv("PODIUM_NODE_MAX_TASKS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.MaxTasks = n
		}
	}
	if v := os.Getenv("PODIUM_NODE_IMAGE_CACHE_HIGH_WATERMARK"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			cfg.ImageCacheHighWatermark = f
		}
	}
	envBool("PODIUM_NODE_IMAGE_CACHE_PRUNE", &cfg.ImageCachePrune)
	envBool("PODIUM_NODE_EXIT_ON_DRAIN", &cfg.ExitOnDrain)
	envBool("PODIUM_NODE_ALLOW_PRIVILEGED_SIDECARS", &cfg.AllowPrivilegedSidecars)
}

func envBool(key string, dst *bool) {
	if v := os.Getenv(key); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			*dst = b
		}
	}
}

func envString(key string, dst *string) {
	if v := os.Getenv(key); v != "" {
		*dst = v
	}
}

func splitLabels(v string) []string {
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// Validate reports the first thing that would stop the daemon from starting, in words an
// operator can act on. It also creates the data directory, because "is it writable" has no
// answer that does not involve trying.
func (c *Config) Validate() error {
	if strings.TrimSpace(c.Server) == "" {
		return errors.New("server is empty: set PODIUM_NODE_SERVER (for example http://127.0.0.1:8080) " +
			"or `server:` in " + DefaultConfigPath)
	}
	if !strings.HasPrefix(c.Server, "http://") && !strings.HasPrefix(c.Server, "https://") {
		return fmt.Errorf("server %q must be an http:// or https:// URL", c.Server)
	}
	switch c.Transport {
	case TransportDev:
		if c.DevToken == "" {
			return errors.New("dev_token is empty: set PODIUM_NODE_DEV_TOKEN to the server's PODIUM_DEV_TOKEN")
		}
		if !strings.HasPrefix(c.Server, "http://") {
			return fmt.Errorf("transport dev dials %q, but the dev transport is loopback HTTP; "+
				"an https:// control plane means transport: tailnet", c.Server)
		}
	case TransportTailnet, TransportHost:
		if !strings.HasPrefix(c.Server, "https://") {
			return fmt.Errorf("transport %s dials %q, but a tailnet control plane serves HTTPS "+
				"on its MagicDNS name; set PODIUM_NODE_SERVER to https://podium.<tailnet>.ts.net",
				c.Transport, c.Server)
		}
	default:
		return fmt.Errorf("transport %q is not a transport (want %s, %s or %s)",
			c.Transport, TransportDev, TransportTailnet, TransportHost)
	}
	if c.MaxTasks < 1 {
		return fmt.Errorf("max_tasks is %d: a node with no slots never gets work; set PODIUM_NODE_MAX_TASKS to 1 or more", c.MaxTasks)
	}
	if strings.TrimSpace(c.DataDir) == "" {
		return errors.New("data_dir is empty: set PODIUM_NODE_DATA_DIR (for example /var/lib/podium-node)")
	}
	if err := checkWritableDir(c.DataDir); err != nil {
		return err
	}
	if c.ImageCachePrune && (c.ImageCacheHighWatermark <= 0 || c.ImageCacheHighWatermark >= 1) {
		return fmt.Errorf("image_cache_high_watermark is %v: it is a fraction of the disk and must be between 0 and 1",
			c.ImageCacheHighWatermark)
	}
	return nil
}

// checkWritableDir creates dir if it is missing and proves the daemon can write in it.
func checkWritableDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("data_dir %s is not usable: %w (create it, or point PODIUM_NODE_DATA_DIR somewhere writable)", dir, err)
	}
	probe := filepath.Join(dir, ".podium-write-probe")
	f, err := os.OpenFile(probe, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("data_dir %s is not writable: %w (fix its ownership, or point PODIUM_NODE_DATA_DIR somewhere writable)", dir, err)
	}
	_ = f.Close()
	_ = os.Remove(probe)
	return nil
}

// TSStateDir is where the node's own Tailscale device identity lives. It sits under the data
// dir because it is exactly as durable as identity.json: lose it and the node re-registers as a
// new Tailscale device, leaving a ghost in the admin console.
func (c *Config) TSStateDir() string {
	return filepath.Join(c.DataDir, tailnet.NodeStateSubdir)
}
