package tailnet

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"tailscale.com/tsnet"
)

// DefaultPreviewTag is the tag a node's preview slot devices carry. The ACL grants people
// access to it and grants it nothing, so a task cannot use its preview as a way out onto
// the tailnet.
const DefaultPreviewTag = "tag:podium-preview"

// PreviewStateSubdir is where a node keeps its preview slot devices, one directory each,
// under its data_dir. They persist for the same reason the node's own device does: a slot
// that re-registers every restart drifts its name and fills the admin console with ghosts.
const PreviewStateSubdir = "ts-preview"

// PreviewDeviceOptions configures one preview slot device.
type PreviewDeviceOptions struct {
	Hostname string
	StateDir string
	// AuthKey is a pre-approved key tagged DefaultPreviewTag, read on first run only.
	// SENSITIVE: never logged.
	AuthKey string
	Logger  *slog.Logger
}

// PreviewDevice is a tailnet device a node lends to one preview at a time. Unlike the
// node's own device it listens: on the exposed ports of whichever task holds the slot,
// forwarding every connection from a person to that task's gateway. Its address is stable
// across tasks, which is also why it is kept rather than made ephemeral.
type PreviewDevice struct {
	ts     *tsnet.Server
	logger *slog.Logger
	up     atomic.Bool

	mu  sync.Mutex
	lns []net.Listener
}

// NewPreviewDevice prepares a slot device. Nothing touches the network until Up.
func NewPreviewDevice(opts PreviewDeviceOptions) (*PreviewDevice, error) {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	if err := os.MkdirAll(opts.StateDir, 0o700); err != nil {
		return nil, fmt.Errorf("tailnet: preview state dir %s is not usable: %w", opts.StateDir, err)
	}
	if opts.AuthKey == "" && !hasDevice(opts.StateDir) {
		return nil, fmt.Errorf("tailnet: preview slot %s has no device yet and no preview auth key is set; "+
			"create a pre-approved auth key tagged %s and set PODIUM_NODE_PREVIEW_TS_AUTHKEY", opts.Hostname, DefaultPreviewTag)
	}
	return &PreviewDevice{
		ts: &tsnet.Server{
			Hostname: opts.Hostname,
			Dir:      opts.StateDir,
			AuthKey:  opts.AuthKey,
			UserLogf: func(format string, args ...any) {
				logger.Info("tsnet preview: " + strings.TrimRight(fmt.Sprintf(format, args...), "\n"))
			},
			Logf: func(format string, args ...any) {
				logger.Debug("tsnet preview: " + strings.TrimRight(fmt.Sprintf(format, args...), "\n"))
			},
		},
		logger: logger.With("preview_device", opts.Hostname),
	}, nil
}

// Up joins the tailnet and returns the device's IPv4 address, which is what a preview's
// URLs are built from.
func (d *PreviewDevice) Up(ctx context.Context) (string, error) {
	upCtx, cancel := upContext(ctx, d.ts.AuthKey, d.ts.Dir)
	defer cancel()
	st, err := d.ts.Up(upCtx)
	if err != nil {
		return "", fmt.Errorf("tailnet: bringing up preview device %s failed: %w", d.ts.Hostname, err)
	}
	d.up.Store(true)
	ip4, _ := d.ts.TailscaleIPs()
	if !ip4.IsValid() {
		return "", fmt.Errorf("tailnet: preview device %s has no IPv4 address", d.ts.Hostname)
	}
	d.logger.InfoContext(ctx, "preview device up",
		"fqdn", strings.TrimSuffix(st.Self.DNSName, "."), "addr", ip4.String(), "tags", tagsOf(st))
	return ip4.String(), nil
}

// Serve listens on each port and forwards every connection a person opens to the host
// port it maps to on loopback, where the task's gateway is published. It replaces whatever
// the slot served before.
func (d *PreviewDevice) Serve(ports map[int]int, splice func(net.Conn, int)) error {
	d.Stop()
	lns := make([]net.Listener, 0, len(ports))
	for port, hostPort := range ports {
		ln, err := d.ts.Listen("tcp", ":"+strconv.Itoa(port))
		if err != nil {
			for _, l := range lns {
				_ = l.Close()
			}
			return fmt.Errorf("tailnet: preview device %s: listen on %d: %w", d.ts.Hostname, port, err)
		}
		lns = append(lns, ln)
		go d.accept(ln, hostPort, splice)
	}
	d.mu.Lock()
	d.lns = lns
	d.mu.Unlock()
	return nil
}

func (d *PreviewDevice) accept(ln net.Listener, hostPort int, splice func(net.Conn, int)) {
	for {
		c, err := ln.Accept()
		if err != nil {
			if !errors.Is(err, net.ErrClosed) {
				d.logger.Debug("preview listener stopped", "error", err)
			}
			return
		}
		go func() {
			login, err := d.viewer(c.RemoteAddr().String())
			if err != nil {
				d.logger.Warn("refused a preview connection", "remote", c.RemoteAddr().String(), "error", err)
				_ = c.Close()
				return
			}
			d.logger.Debug("preview connection", "login", login, "port", hostPort)
			splice(c, hostPort)
		}()
	}
}

// viewer is who opened a connection. A preview is for people: a tagged device — another
// node, a task on one, a control plane — is refused, which is the same line the control
// plane itself draws in classify.
func (d *PreviewDevice) viewer(remoteAddr string) (string, error) {
	lc, err := d.ts.LocalClient()
	if err != nil {
		return "", err
	}
	who, err := lc.WhoIs(context.Background(), remoteAddr)
	if err != nil {
		return "", fmt.Errorf("whois: %w", err)
	}
	if who == nil || who.Node == nil {
		return "", errors.New("not a tailnet peer")
	}
	if len(who.Node.Tags) > 0 {
		return "", fmt.Errorf("tagged device %s (%s)", who.Node.Name, strings.Join(who.Node.Tags, ","))
	}
	if who.UserProfile == nil || who.UserProfile.LoginName == "" {
		return "", errors.New("no login name")
	}
	return who.UserProfile.LoginName, nil
}

// Stop closes the slot's listeners. The device stays on the tailnet for the next preview.
func (d *PreviewDevice) Stop() {
	d.mu.Lock()
	lns := d.lns
	d.lns = nil
	d.mu.Unlock()
	for _, ln := range lns {
		_ = ln.Close()
	}
}

// Close takes the device off the tailnet until the next start.
func (d *PreviewDevice) Close() error {
	d.Stop()
	if !d.up.Load() {
		return nil
	}
	return d.ts.Close()
}
