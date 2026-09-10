package tailnet

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"tailscale.com/client/tailscale/apitype"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/tsnet"

	"github.com/podium-ade/podium/internal/transport"
)

// Ports the control plane serves on inside the tailnet. They are fixed because the ACL in
// deploy/tailscale-acl.example.json names 443, and because MagicDNS URLs carry no port.
const (
	httpsPort = ":443"
	httpPort  = ":80"
)

// DefaultHostname is PODIUM_TS_HOSTNAME's fallback: the MagicDNS name becomes
// podium.<tailnet>.ts.net.
const DefaultHostname = "podium"

// DefaultStateDir is where tsnet keeps the device's node key. It must persist across restarts:
// a fresh state dir registers a brand new device every time and fills the admin console with
// ghosts that all answer to the same hostname.
const DefaultStateDir = "/var/lib/podium/tsnet"

// keyExpiryWarning is how close a node key may come to expiring before /readyz says so.
const keyExpiryWarning = 30 * 24 * time.Hour

// stateFile is the file tsnet writes the device identity to.
const stateFile = "tailscaled.state"

// machineKeyField is the only thing tsnet writes to the state file before a device has actually
// registered. A file holding just that is a first run that failed, not a device.
const machineKeyField = "_machinekey"

// loginTimeout bounds Up when no auth key was supplied. Without a key tsnet prints a login URL
// and waits forever, which for an unattended daemon means a process that is neither up nor
// dead. With a key, registration is allowed to take as long as it takes.
const loginTimeout = 90 * time.Second

// UserStore is the slice of the store this transport needs: the first time a human is seen on
// the tailnet, their login is recorded so the rest of Podium can attach roles to it.
type UserStore interface {
	UpsertUser(ctx context.Context, login, displayName string) error
}

// Options configures the tsnet transport.
type Options struct {
	// Hostname is the device name to register, PODIUM_TS_HOSTNAME. Empty means DefaultHostname.
	Hostname string
	// StateDir holds the tsnet node key, PODIUM_TS_STATE_DIR. Empty means DefaultStateDir.
	StateDir string
	// AuthKey is the Tailscale auth key, TS_AUTHKEY. It is only read on the first run; after
	// that the state dir is the identity. SENSITIVE: never logged.
	AuthKey string
	// Identity is the tag policy.
	Identity IdentityOptions
	// Users records humans on first sight. Optional: a nil store simply skips the write.
	Users UserStore
	// Logger receives both Podium's own lines and the ones tsnet wants an operator to read.
	Logger *slog.Logger
}

// Listener is the tsnet transport.Listener: its own Tailscale device, HTTPS on the MagicDNS
// name, and identity from WhoIs.
type Listener struct {
	*identifier
	ts       *tsnet.Server
	logger   *slog.Logger
	hostname string

	up       atomic.Bool
	mu       sync.Mutex
	fqdn     string
	redirect *http.Server
}

// New prepares the embedded Tailscale device. Nothing touches the network until Listen.
func New(opts Options) (*Listener, error) {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	hostname := opts.Hostname
	if hostname == "" {
		hostname = DefaultHostname
	}
	stateDir := opts.StateDir
	if stateDir == "" {
		stateDir = DefaultStateDir
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return nil, fmt.Errorf("tailnet: PODIUM_TS_STATE_DIR %s is not usable: %w", stateDir, err)
	}
	if err := requireAuthKey(stateDir, opts.AuthKey); err != nil {
		return nil, err
	}

	ts := &tsnet.Server{
		Hostname: hostname,
		Dir:      stateDir,
		AuthKey:  opts.AuthKey,
		// UserLogf is what tsnet wants a human to see: which device it registered as, and
		// the login URL when an auth key was not accepted. Logf is the very verbose
		// backend log, which goes to debug so it is available without being in the way.
		UserLogf: func(format string, args ...any) {
			logger.Info("tsnet: " + strings.TrimRight(fmt.Sprintf(format, args...), "\n"))
		},
		Logf: func(format string, args ...any) {
			logger.Debug("tsnet: " + strings.TrimRight(fmt.Sprintf(format, args...), "\n"))
		},
	}
	l := &Listener{ts: ts, logger: logger, hostname: hostname}
	l.identifier = &identifier{
		who:    lazyWhoIs(ts),
		opts:   opts.Identity,
		users:  opts.Users,
		logger: logger,
	}
	return l, nil
}

// requireAuthKey turns the commonest first-run mistake into a sentence instead of a process
// that hangs printing a login URL nobody is watching.
func requireAuthKey(stateDir, authKey string) error {
	if authKey != "" || hasDevice(stateDir) {
		return nil
	}
	return fmt.Errorf("tailnet: %s holds no Tailscale device yet and TS_AUTHKEY is not set; "+
		"create a reusable, pre-approved auth key tagged %s in the Tailscale admin console "+
		"(https://login.tailscale.com/admin/settings/keys) and pass it as TS_AUTHKEY",
		stateDir, DefaultServerTag)
}

// hasDevice reports whether stateDir holds a Tailscale device that actually registered.
//
// File existence is not enough: tsnet writes the state file with a machine key as soon as it
// starts, so a first run that failed on a bad auth key leaves a file behind. Treating that as a
// registered device would make the *next* run skip the auth-key check and then block forever on
// an interactive login. Anything beyond the machine key means a real profile.
func hasDevice(stateDir string) bool {
	raw, err := os.ReadFile(filepath.Join(stateDir, stateFile)) //nolint:gosec // the operator names their own state dir
	if err != nil {
		return false
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		// Unreadable state is not proof of a device, and demanding a key is the safe way
		// to be wrong: the key is ignored when the device turns out to exist.
		return false
	}
	for k := range fields {
		if k != machineKeyField {
			return true
		}
	}
	return false
}

// upContext bounds Up when there is nothing to log in with. See loginTimeout.
func upContext(ctx context.Context, authKey, stateDir string) (context.Context, context.CancelFunc) {
	if authKey != "" || hasDevice(stateDir) {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, loginTimeout)
}

// Listen brings the device up, serves HTTPS on the MagicDNS name and redirects plain HTTP.
func (l *Listener) Listen(ctx context.Context) (net.Listener, error) {
	upCtx, cancel := upContext(ctx, l.ts.AuthKey, l.ts.Dir)
	defer cancel()
	st, err := l.ts.Up(upCtx)
	if err != nil {
		return nil, fmt.Errorf("tailnet: joining the tailnet as %q failed: %w "+
			"(is TS_AUTHKEY valid, reusable and pre-approved?)", l.hostname, err)
	}
	l.up.Store(true)
	if err := checkHTTPS(st); err != nil {
		return nil, err
	}
	fqdn := strings.TrimSuffix(st.Self.DNSName, ".")
	l.mu.Lock()
	l.fqdn = fqdn
	l.mu.Unlock()

	lc, err := l.ts.LocalClient()
	if err != nil {
		return nil, fmt.Errorf("tailnet: local client: %w", err)
	}
	raw, err := l.ts.Listen("tcp", httpsPort)
	if err != nil {
		return nil, fmt.Errorf("tailnet: listen on %s: %w", httpsPort, err)
	}

	l.logger.InfoContext(ctx, "tailnet device up",
		"fqdn", fqdn, "addrs", ipsOf(st), "tags", tagsOf(st), "url", "https://"+fqdn)
	l.startRedirect(ctx, fqdn)
	return tls.NewListener(raw, tlsConfig(lc.GetCertificate)), nil
}

// tlsConfig is the one place ALPN is set, and it is load-bearing. tsnet.ListenTLS builds its
// TLS config without NextProtos, so a client would never negotiate h2 — and NodeService.Stream
// is a Connect bidirectional stream, which Connect refuses on HTTP/1.1. Advertising h2 first is
// what makes a node's stream work at all.
func tlsConfig(getCert func(*tls.ClientHelloInfo) (*tls.Certificate, error)) *tls.Config {
	return &tls.Config{
		GetCertificate: getCert,
		NextProtos:     []string{"h2", "http/1.1"},
		MinVersion:     tls.VersionTLS12,
	}
}

// checkHTTPS rejects the two tailnet settings that make certificate provisioning impossible,
// before the first browser sees an unexplained handshake failure.
func checkHTTPS(st *ipnstate.Status) error {
	const docs = "https://tailscale.com/kb/1153/enabling-https"
	if st == nil || st.Self == nil {
		return errors.New("tailnet: the Tailscale backend reported no status for this device")
	}
	if st.CurrentTailnet == nil || !st.CurrentTailnet.MagicDNSEnabled {
		return fmt.Errorf("tailnet: MagicDNS is not enabled on this tailnet, so %s has no name to "+
			"put in a certificate; enable it under DNS in the admin console (%s)", st.Self.HostName, docs)
	}
	if len(st.CertDomains) == 0 {
		return fmt.Errorf("tailnet: HTTPS certificates are not enabled on this tailnet, so Podium "+
			"cannot serve https://%s; enable HTTPS under DNS in the admin console (%s)",
			strings.TrimSuffix(st.Self.DNSName, "."), docs)
	}
	return nil
}

// startRedirect answers plain HTTP with a redirect to the HTTPS name. It is a convenience for
// anyone who types the hostname without a scheme, so a failure to bind :80 is logged and
// ignored rather than taken as fatal.
func (l *Listener) startRedirect(ctx context.Context, fqdn string) {
	raw, err := l.ts.Listen("tcp", httpPort)
	if err != nil {
		l.logger.WarnContext(ctx, "tailnet: no http redirect", "port", httpPort, "error", err)
		return
	}
	srv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "https://"+fqdn+r.URL.RequestURI(), http.StatusPermanentRedirect)
		}),
		ReadHeaderTimeout: 10 * time.Second,
	}
	l.mu.Lock()
	l.redirect = srv
	l.mu.Unlock()
	go func() {
		if err := srv.Serve(raw); err != nil && !errors.Is(err, http.ErrServerClosed) {
			l.logger.Warn("tailnet: http redirect stopped", "error", err)
		}
	}()
}

// BaseURL is the URL a node or a CLI should dial. It is only meaningful after Listen.
func (l *Listener) BaseURL() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.fqdn == "" {
		return ""
	}
	return "https://" + l.fqdn
}

// Ready reports whether the tailnet device is still usable, for /readyz. A tagged device does
// not key-expire by default, but one enrolled with an untagged key does, and it does so
// silently — so the remaining validity is surfaced rather than discovered.
func (l *Listener) Ready(ctx context.Context) (string, error) {
	lc, err := l.ts.LocalClient()
	if err != nil {
		return "", fmt.Errorf("tailnet: local client: %w", err)
	}
	st, err := lc.StatusWithoutPeers(ctx)
	if err != nil {
		return "", fmt.Errorf("tailnet: status: %w", err)
	}
	return readyDetail(st, time.Now())
}

func readyDetail(st *ipnstate.Status, now time.Time) (string, error) {
	if st == nil || st.Self == nil {
		return "", errors.New("tailnet: no device status")
	}
	if st.BackendState != "Running" {
		return "", fmt.Errorf("tailnet: backend is %s, not Running", st.BackendState)
	}
	detail := "tailnet " + strings.TrimSuffix(st.Self.DNSName, ".")
	if exp := st.Self.KeyExpiry; exp != nil && !exp.IsZero() {
		left := exp.Sub(now)
		detail += fmt.Sprintf(", node key expires in %dd", int(left.Hours()/24))
		if left < keyExpiryWarning {
			return detail, fmt.Errorf("tailnet: this device's node key expires in %s; "+
				"tag it (tagged devices do not expire) or disable key expiry in the admin console",
				left.Round(time.Hour))
		}
	}
	return detail, nil
}

// Close shuts the redirect down and leaves the tailnet.
func (l *Listener) Close() error {
	l.mu.Lock()
	redirect := l.redirect
	l.redirect = nil
	l.mu.Unlock()
	if redirect != nil {
		_ = redirect.Close()
	}
	if !l.up.Load() {
		return nil
	}
	return l.ts.Close()
}

// lazyWhoIs defers LocalClient() to the first request: it starts the tsnet server, which must
// not happen while New is still validating options.
func lazyWhoIs(ts *tsnet.Server) whoIsFunc {
	return func(ctx context.Context, remoteAddr string) (*apitype.WhoIsResponse, error) {
		lc, err := ts.LocalClient()
		if err != nil {
			return nil, err
		}
		return lc.WhoIs(ctx, remoteAddr)
	}
}

func ipsOf(st *ipnstate.Status) []string {
	out := make([]string, 0, len(st.TailscaleIPs))
	for _, ip := range st.TailscaleIPs {
		out = append(out, ip.String())
	}
	return out
}

func tagsOf(st *ipnstate.Status) []string {
	if st.Self == nil || st.Self.Tags == nil {
		return nil
	}
	return st.Self.Tags.AsSlice()
}

// Compile-time proof that both modes satisfy the seam the server is written against.
var (
	_ transport.Listener = (*Listener)(nil)
	_ transport.Listener = (*HostListener)(nil)
)
