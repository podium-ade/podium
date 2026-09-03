package tailnet

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"tailscale.com/client/local"
)

// HostOptions configures the host transport, which borrows the machine's own tailscaled
// instead of embedding a device. Same identity model, one fewer device in the admin console.
type HostOptions struct {
	// Identity is the tag policy, shared with the tsnet mode.
	Identity IdentityOptions
	// Users records humans on first sight. Optional.
	Users UserStore
	// Logger receives the transport's own lines.
	Logger *slog.Logger
}

// HostListener serves on the machine's existing tailnet address. It needs a running
// tailscaled, a tagged device, and the privilege to bind port 443 on that address.
type HostListener struct {
	*identifier
	lc     *local.Client
	logger *slog.Logger

	mu       sync.Mutex
	fqdn     string
	redirect *http.Server
}

// NewHost prepares the host-mode transport. Nothing talks to tailscaled until Listen.
func NewHost(opts HostOptions) (*HostListener, error) {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	lc := &local.Client{}
	h := &HostListener{lc: lc, logger: logger}
	h.identifier = &identifier{
		who:    lc.WhoIs,
		opts:   opts.Identity,
		users:  opts.Users,
		logger: logger,
	}
	return h, nil
}

// Listen binds the machine's tailnet address on 443 and serves TLS with the certificate
// tailscaled provisions for its MagicDNS name.
func (h *HostListener) Listen(ctx context.Context) (net.Listener, error) {
	st, err := h.lc.Status(ctx)
	if err != nil {
		return nil, fmt.Errorf("tailnet(host): talking to the local tailscaled failed: %w "+
			"(is Tailscale installed and running? PODIUM_TRANSPORT=tailnet embeds its own device instead)", err)
	}
	if st.BackendState != "Running" {
		return nil, fmt.Errorf("tailnet(host): tailscaled is %s, not Running; run `tailscale up` first", st.BackendState)
	}
	if err := checkHTTPS(st); err != nil {
		return nil, err
	}
	if len(st.TailscaleIPs) == 0 {
		return nil, errors.New("tailnet(host): tailscaled reports no Tailscale IP for this machine")
	}
	fqdn := strings.TrimSuffix(st.Self.DNSName, ".")
	h.mu.Lock()
	h.fqdn = fqdn
	h.mu.Unlock()

	addr := net.JoinHostPort(st.TailscaleIPs[0].String(), "443")
	var lcfg net.ListenConfig
	raw, err := lcfg.Listen(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("tailnet(host): listen on %s: %w "+
			"(443 is privileged: run as root, or grant CAP_NET_BIND_SERVICE)", addr, err)
	}

	h.logger.InfoContext(ctx, "tailnet device up (host mode)",
		"fqdn", fqdn, "addr", addr, "tags", tagsOf(st), "url", "https://"+fqdn)
	h.startRedirect(ctx, fqdn, st.TailscaleIPs[0].String())
	return tls.NewListener(raw, tlsConfig(h.lc.GetCertificate)), nil
}

func (h *HostListener) startRedirect(ctx context.Context, fqdn, ip string) {
	addr := net.JoinHostPort(ip, "80")
	var lcfg net.ListenConfig
	raw, err := lcfg.Listen(ctx, "tcp", addr)
	if err != nil {
		h.logger.WarnContext(ctx, "tailnet(host): no http redirect", "addr", addr, "error", err)
		return
	}
	srv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "https://"+fqdn+r.URL.RequestURI(), http.StatusPermanentRedirect)
		}),
		ReadHeaderTimeout: 10 * time.Second,
	}
	h.mu.Lock()
	h.redirect = srv
	h.mu.Unlock()
	go func() {
		if err := srv.Serve(raw); err != nil && !errors.Is(err, http.ErrServerClosed) {
			h.logger.Warn("tailnet(host): http redirect stopped", "error", err)
		}
	}()
}

// BaseURL is the URL a node or a CLI should dial, valid after Listen.
func (h *HostListener) BaseURL() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.fqdn == "" {
		return ""
	}
	return "https://" + h.fqdn
}

// Ready reports whether the host's tailscaled is still up, for /readyz.
func (h *HostListener) Ready(ctx context.Context) (string, error) {
	st, err := h.lc.StatusWithoutPeers(ctx)
	if err != nil {
		return "", fmt.Errorf("tailnet(host): status: %w", err)
	}
	return readyDetail(st, time.Now())
}

// Close stops the redirect. The host's tailscaled is not ours to shut down.
func (h *HostListener) Close() error {
	h.mu.Lock()
	redirect := h.redirect
	h.redirect = nil
	h.mu.Unlock()
	if redirect != nil {
		_ = redirect.Close()
	}
	return nil
}
