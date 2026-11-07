package desktop

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/coder/websocket"
)

// PortName is the exposed port a desktop playbook's task names voxl's port by.
const PortName = "desktop"

// TerminalPortName is the exposed port a desktop playbook's task names ttyd's port by: a
// terminal in the turn's workspace, beside its desktop.
const TerminalPortName = "terminal"

// servable is whether port is one this origin serves. Nothing else a task exposes is reached
// through it: a link names one of these, and only these.
func servable(port string) bool { return port == PortName || port == TerminalPortName }

// pathPrefix is where every desktop is served on the desktop origin: /t/<task>/<token>/.
const pathPrefix = "/t/"

// Upstream is where a desktop answers, and what each request to it has to carry for the
// provider in front of it to let it through (Modal's connect token, for one).
type Upstream struct {
	URL   *url.URL
	Apply func(*http.Request)
}

// ErrNoDesktop is a task that has no desktop this server can reach right now: not running,
// not a desktop playbook's, or exposed somewhere this server cannot route to.
var ErrNoDesktop = errors.New("desktop: no desktop to show")

// Resolver finds a running task's desktop, or its terminal: the exposed port named port.
type Resolver interface {
	Resolve(ctx context.Context, taskID, port string) (Upstream, error)
}

// Service mints desktop links and serves the desktop origin.
type Service struct {
	base      *url.URL
	uiOrigin  string
	resolver  Resolver
	signer    *signer
	logger    *slog.Logger
	viewers   controls
	transport http.RoundTripper
}

// New serves desktops on base (PODIUM_DESKTOP_URL), framed only by uiOrigin when it is known
// (PODIUM_PUBLIC_URL; empty allows any framer, which is the local, single-origin dev case).
func New(base *url.URL, uiOrigin string, resolver Resolver, logger *slog.Logger) (*Service, error) {
	if base == nil || base.Host == "" {
		return nil, errors.New("desktop: no desktop origin")
	}
	if logger == nil {
		logger = slog.Default()
	}
	sg, err := newSigner()
	if err != nil {
		return nil, err
	}
	return &Service{
		base:      base,
		uiOrigin:  strings.TrimSuffix(uiOrigin, "/"),
		resolver:  resolver,
		signer:    sg,
		logger:    logger,
		transport: http.DefaultTransport,
	}, nil
}

// Origin is the desktop origin, scheme://host[:port], which the UI's CSP must let it frame.
func (s *Service) Origin() string { return s.base.Scheme + "://" + s.base.Host }

// Mint is a link to a task's desktop (PortName) or terminal (TerminalPortName) for login. The
// caller has already decided login may see the task and that it is a running desktop; the
// link is refused again at use if it is not.
func (s *Service) Mint(taskID, port, login string) (string, time.Time, error) {
	if taskID == "" || strings.ContainsAny(taskID, "/?#") {
		return "", time.Time{}, fmt.Errorf("desktop: %q is not a task id", taskID)
	}
	if !servable(port) {
		return "", time.Time{}, fmt.Errorf("desktop: %q is not a port this origin serves", port)
	}
	tok, exp, err := s.signer.sign(taskID, port, login)
	if err != nil {
		return "", time.Time{}, err
	}
	return s.Origin() + pathPrefix + taskID + "/" + tok + "/", exp, nil
}

// readyTimeout bounds Ready's probe: the panel asks every couple of seconds while a desktop
// starts, and a desktop that takes longer than this to answer its health check is not up.
const readyTimeout = 3 * time.Second

// Ready reports whether a task's desktop answers yet: voxl's health check, through the same
// route and with the same credentials a viewer's requests take. podium-desktop starts the
// terminal before it waits for voxl, so a desktop that answers has its terminal too. A task
// is running before its desktop is up — the display, XFCE, Chromium and the stream take
// seconds, and the first run on a machine pulls the image — and a frame loaded in that window
// shows an error page it never recovers from: the panel waits on this instead.
func (s *Service) Ready(ctx context.Context, taskID string) bool {
	up, err := s.resolver.Resolve(ctx, taskID, PortName)
	if err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, readyTimeout)
	defer cancel()
	u := *up.URL
	u.Path = strings.TrimSuffix(up.URL.Path, "/") + "/healthz"
	u.RawPath = ""
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return false
	}
	if up.Apply != nil {
		up.Apply(req)
	}
	res, err := (&http.Client{Transport: s.transport}).Do(req)
	if err != nil {
		return false
	}
	defer func() { _ = res.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 4<<10))
	return res.StatusCode == http.StatusOK
}

// Handles reports whether r is addressed to the desktop origin. Everything that is, and only
// that, is the desktop's: the UI and the API are not served there.
func (s *Service) Handles(r *http.Request) bool {
	return strings.EqualFold(r.Host, s.base.Host)
}

// ServeHTTP serves /t/<task>/<token>/<rest>: <rest> is voxl's, or ttyd's, own path.
func (s *Service) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	task, token, rest, ok := splitPath(r.URL.Path)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if rest == "" && !strings.HasSuffix(r.URL.Path, "/") {
		// voxl's and ttyd's pages load everything relative to themselves, so it must end in a
		// slash.
		http.Redirect(w, r, r.URL.Path+"/", http.StatusFound)
		return
	}
	c, err := s.signer.verify(token, task)
	if err != nil {
		// The same answer for a forged, expired or wrong-task link: nothing here says
		// whether a desktop exists.
		http.NotFound(w, r)
		return
	}
	up, err := s.resolver.Resolve(r.Context(), task, c.Port)
	if err != nil {
		if errors.Is(err, ErrNoDesktop) {
			http.Error(w, "this desktop is not running any more", http.StatusGone)
			return
		}
		s.logger.WarnContext(r.Context(), "desktop unreachable", "task_id", task, "error", err)
		http.Error(w, "the desktop could not be reached", http.StatusBadGateway)
		return
	}
	if c.Port == PortName && rest == "ws" {
		s.bridge(w, r, task, c.Login, up)
		return
	}
	if isUpgrade(r) && !s.sameOrigin(r) {
		// A terminal's socket is piped as it is: it must come from the terminal's own page,
		// served from this origin, as the desktop's must (bridge).
		http.Error(w, "the socket must come from this origin", http.StatusForbidden)
		return
	}
	s.proxy(up).ServeHTTP(w, r.WithContext(withRest(r.Context(), rest)))
}

// isUpgrade is a WebSocket handshake. httputil.ReverseProxy carries one through as it is.
func isUpgrade(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
}

// sameOrigin is a request whose Origin is this origin.
func (s *Service) sameOrigin(r *http.Request) bool {
	o, err := url.Parse(r.Header.Get("Origin"))
	return err == nil && o.Host != "" && strings.EqualFold(o.Host, s.base.Host)
}

// splitPath takes /t/<task>/<token>/<rest> apart. rest is "" for the page itself.
func splitPath(p string) (task, token, rest string, ok bool) {
	tail, found := strings.CutPrefix(p, pathPrefix)
	if !found {
		return "", "", "", false
	}
	parts := strings.SplitN(tail, "/", 3)
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return "", "", "", false
	}
	if len(parts) == 3 {
		rest = parts[2]
	}
	return parts[0], parts[1], rest, true
}

type restKey struct{}

func withRest(ctx context.Context, rest string) context.Context {
	return context.WithValue(ctx, restKey{}, rest)
}

// stripped is every request header that is the person's, or Cloudflare's, and none of the
// desktop's business.
var stripped = []string{"Cookie", "Authorization", "X-Forwarded-For", "X-Forwarded-Host",
	"X-Forwarded-Proto", "X-Real-Ip", "Forwarded", "Cf-Connecting-Ip", "Cf-Ipcountry", "Cf-Ray", "Cf-Visitor"}

func (s *Service) proxy(up Upstream) *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			rest, _ := pr.In.Context().Value(restKey{}).(string)
			pr.SetURL(up.URL)
			pr.Out.URL.Path = strings.TrimSuffix(up.URL.Path, "/") + "/" + rest
			pr.Out.URL.RawPath = ""
			pr.Out.Host = up.URL.Host
			for _, h := range stripped {
				pr.Out.Header.Del(h)
			}
			if pr.In.Header.Get("Origin") != "" {
				// voxl answers its own page; through this proxy, its page is ours. ttyd checks
				// no origin, and is told the same.
				pr.Out.Header.Set("Origin", up.URL.Scheme+"://"+up.URL.Host)
			}
			if up.Apply != nil {
				up.Apply(pr.Out)
			}
		},
		FlushInterval: -1,
		Transport:     s.transport,
		ModifyResponse: func(resp *http.Response) error {
			// Nothing upstream gets to set state on this origin, or decide who frames it.
			resp.Header.Del("Set-Cookie")
			resp.Header.Set("Content-Security-Policy", "frame-ancestors "+s.frameAncestors())
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			s.logger.WarnContext(r.Context(), "desktop proxy", "path", r.URL.Path, "error", err)
			http.Error(w, "the desktop could not be reached", http.StatusBadGateway)
		},
	}
}

func (s *Service) frameAncestors() string {
	if s.uiOrigin == "" {
		return "*"
	}
	return s.uiOrigin
}

// bridge carries voxl's WebSocket frame by frame, rather than piping it, so that it can decide
// who may resize the desktop (control.go).
func (s *Service) bridge(w http.ResponseWriter, r *http.Request, task, login string, up Upstream) {
	client, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		// The page is voxl's, served from this origin: the socket must come from it.
		OriginPatterns:  []string{s.base.Host},
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		s.logger.DebugContext(r.Context(), "desktop websocket refused", "task_id", task, "error", err)
		return
	}
	defer func() { _ = client.CloseNow() }()
	client.SetReadLimit(64 << 20)

	target := *up.URL
	switch target.Scheme {
	case "https":
		target.Scheme = "wss"
	default:
		target.Scheme = "ws"
	}
	target.Path = strings.TrimSuffix(up.URL.Path, "/") + "/ws"
	target.RawQuery = r.URL.RawQuery
	header := http.Header{}
	header.Set("Origin", up.URL.Scheme+"://"+up.URL.Host)
	if up.Apply != nil {
		probe, _ := http.NewRequest(http.MethodGet, target.String(), nil)
		up.Apply(probe)
		for k, vs := range probe.Header {
			for _, v := range vs {
				header.Add(k, v)
			}
		}
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	upConn, _, err := websocket.Dial(ctx, target.String(), &websocket.DialOptions{
		HTTPHeader:      header,
		CompressionMode: websocket.CompressionDisabled,
		HTTPClient:      &http.Client{Transport: s.transport},
	})
	if err != nil {
		s.logger.WarnContext(r.Context(), "desktop websocket unreachable", "task_id", task, "error", err)
		client.Close(websocket.StatusInternalError, "the desktop could not be reached")
		return
	}
	defer func() { _ = upConn.CloseNow() }()
	upConn.SetReadLimit(64 << 20)

	v := &viewer{up: upConn}
	s.viewers.join(task, v)
	s.logger.InfoContext(r.Context(), "desktop viewer joined", "task_id", task, "login", login,
		"viewers", s.viewers.count(task))
	defer func() {
		s.viewers.leave(task, v)
		s.logger.InfoContext(context.Background(), "desktop viewer left", "task_id", task, "login", login,
			"viewers", s.viewers.count(task))
	}()

	// The desktop to the viewer, unchanged.
	go func() {
		defer cancel()
		for {
			typ, b, err := upConn.Read(ctx)
			if err != nil {
				return
			}
			if err := client.Write(ctx, typ, b); err != nil {
				return
			}
		}
	}()
	// The viewer to the desktop: everything but a resize from a viewer not in control.
	for {
		typ, b, err := client.Read(ctx)
		if err != nil {
			return
		}
		if typ == websocket.MessageText && strings.HasPrefix(string(b), resizePrefix) {
			v.remember(string(b))
			if !s.viewers.inControl(task, v) {
				continue
			}
		}
		if err := upConn.Write(ctx, typ, b); err != nil {
			return
		}
	}
}
