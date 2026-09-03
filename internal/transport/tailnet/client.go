package tailnet

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"tailscale.com/tsnet"
)

// NodeHostnamePrefix is prepended to a worker's short hostname to form its Tailscale device
// name, so a fleet is obvious in the admin console.
const NodeHostnamePrefix = "podium-node-"

// NodeStateSubdir is where a node keeps its tsnet device identity, under its data_dir.
const NodeStateSubdir = "ts"

// ClientOptions configures a node's own tailnet device.
type ClientOptions struct {
	// Hostname is the device name to register. Empty means NodeHostnamePrefix + the
	// machine's short hostname.
	Hostname string
	// StateDir holds the node's tsnet identity; it must persist across restarts.
	StateDir string
	// AuthKey is a reusable, pre-approved key tagged tag:podium-node,
	// PODIUM_NODE_TS_AUTHKEY or TS_AUTHKEY. SENSITIVE: never logged.
	AuthKey string
	// Logger receives the transport's own lines.
	Logger *slog.Logger
}

// Client is a node's outbound-only tailnet device: it dials the control plane and never
// listens. Nothing on the tailnet can reach a worker, which is the whole point of the design's
// outbound-only guarantee.
type Client struct {
	ts     *tsnet.Server
	logger *slog.Logger
	up     atomic.Bool
}

// NewClient prepares the device. Nothing touches the network until Up.
func NewClient(opts ClientOptions) (*Client, error) {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	hostname := opts.Hostname
	if hostname == "" {
		hostname = NodeHostname("")
	}
	if opts.StateDir == "" {
		return nil, fmt.Errorf("tailnet: node state dir is required (it lives under data_dir/%s)", NodeStateSubdir)
	}
	if err := os.MkdirAll(opts.StateDir, 0o700); err != nil {
		return nil, fmt.Errorf("tailnet: node state dir %s is not usable: %w", opts.StateDir, err)
	}
	if err := requireNodeAuthKey(opts.StateDir, opts.AuthKey); err != nil {
		return nil, err
	}
	return &Client{
		ts: &tsnet.Server{
			Hostname: hostname,
			Dir:      opts.StateDir,
			AuthKey:  opts.AuthKey,
			UserLogf: func(format string, args ...any) {
				logger.Info("tsnet: " + strings.TrimRight(fmt.Sprintf(format, args...), "\n"))
			},
			Logf: func(format string, args ...any) {
				logger.Debug("tsnet: " + strings.TrimRight(fmt.Sprintf(format, args...), "\n"))
			},
		},
		logger: logger,
	}, nil
}

func requireNodeAuthKey(stateDir, authKey string) error {
	if authKey != "" || hasDevice(stateDir) {
		return nil
	}
	return fmt.Errorf("tailnet: %s holds no Tailscale device yet and neither PODIUM_NODE_TS_AUTHKEY "+
		"nor TS_AUTHKEY is set; create a reusable, pre-approved auth key tagged %s in the Tailscale "+
		"admin console (https://login.tailscale.com/admin/settings/keys). Note this is the *Tailscale* "+
		"auth key, not the Podium enrollment token", stateDir, DefaultNodeTag)
}

// Up joins the tailnet and logs the device's name and addresses.
func (c *Client) Up(ctx context.Context) error {
	upCtx, cancel := upContext(ctx, c.ts.AuthKey, c.ts.Dir)
	defer cancel()
	st, err := c.ts.Up(upCtx)
	if err == nil {
		c.up.Store(true)
	}
	if err != nil {
		return fmt.Errorf("tailnet: joining the tailnet failed: %w "+
			"(is the auth key valid, reusable and pre-approved?)", err)
	}
	c.logger.InfoContext(ctx, "tailnet device up",
		"fqdn", strings.TrimSuffix(st.Self.DNSName, "."), "addrs", ipsOf(st), "tags", tagsOf(st))
	return nil
}

// HTTPClient is the client every RPC to the control plane goes through.
//
// tsnet's own HTTPClient sets Transport.DialContext, which switches Go's automatic HTTP/2
// upgrade off, and NodeService.Stream is a Connect bidirectional stream that HTTP/1.1 cannot
// carry. So the protocol set is declared explicitly, and HTTP/1.1 is left off: a server that
// does not advertise h2 over ALPN then fails the handshake loudly instead of silently
// downgrading and breaking the stream on the first Assign.
func (c *Client) HTTPClient() *http.Client {
	tr := &http.Transport{
		DialContext:           c.ts.Dial,
		Protocols:             http2Only(),
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 0,
	}
	return &http.Client{Transport: tr}
}

// Close leaves the tailnet. It is a no-op before Up, because tsnet panics when a server that
// was never started is closed.
func (c *Client) Close() error {
	if !c.up.Load() {
		return nil
	}
	return c.ts.Close()
}

// NewHostClient is the host-mode node client: the machine is already on the tailnet, so the
// operating system routes to the control plane and no device is embedded. Identity still comes
// from WhoIs — of the host's own device, which must carry the node tag.
func NewHostClient() *http.Client {
	return &http.Client{Transport: &http.Transport{Protocols: http2Only()}}
}

// NewUserClient is what the CLI uses on a user's own machine: a plain HTTPS client with no
// credential at all. The user's device is on the tailnet, so WhoIs names them.
func NewUserClient() *http.Client {
	tr := &http.Transport{Protocols: new(http.Protocols)}
	tr.Protocols.SetHTTP1(true)
	tr.Protocols.SetHTTP2(true)
	return &http.Client{Transport: tr}
}

func http2Only() *http.Protocols {
	p := new(http.Protocols)
	p.SetHTTP2(true)
	return p
}

// NodeHostname turns a machine hostname into a Tailscale device name. Tailscale accepts
// letters, digits and dashes, and takes the first DNS label only.
func NodeHostname(machine string) string {
	if machine == "" {
		machine, _ = os.Hostname()
	}
	short, _, _ := strings.Cut(machine, ".")
	var b strings.Builder
	for _, r := range strings.ToLower(short) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_' || r == ' ':
			b.WriteByte('-')
		}
	}
	name := strings.Trim(b.String(), "-")
	if name == "" {
		name = "worker"
	}
	return NodeHostnamePrefix + name
}
