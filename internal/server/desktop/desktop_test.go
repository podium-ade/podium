package desktop

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/require"
)

// fakeVoxl stands in for voxl: a page, a script, and a WebSocket that records every text
// message a viewer's resize reaches it with.
type fakeVoxl struct {
	srv *httptest.Server

	mu       sync.Mutex
	got      []string
	headers  http.Header
	upgrades int
}

func newFakeVoxl(t *testing.T) *fakeVoxl {
	t.Helper()
	f := &fakeVoxl{}
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.upgrades++
		f.headers = r.Header.Clone()
		f.mu.Unlock()
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer func() { _ = c.CloseNow() }()
		_ = c.Write(r.Context(), websocket.MessageText, []byte(`{"t":"hello"}`))
		for {
			typ, b, err := c.Read(r.Context())
			if err != nil {
				return
			}
			if typ == websocket.MessageText {
				f.mu.Lock()
				f.got = append(f.got, string(b))
				f.mu.Unlock()
			}
		}
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.headers = r.Header.Clone()
		f.mu.Unlock()
		http.SetCookie(w, &http.Cookie{Name: "voxl", Value: "x"})
		_, _ = io.WriteString(w, "voxl page at "+r.URL.Path)
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeVoxl) received() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.got...)
}

func (f *fakeVoxl) lastHeaders() http.Header {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.headers.Clone()
}

type resolverFunc func(ctx context.Context, taskID, port string) (Upstream, error)

func (f resolverFunc) Resolve(ctx context.Context, taskID, port string) (Upstream, error) {
	return f(ctx, taskID, port)
}

// harness is a desktop Service in front of a fake voxl and a fake ttyd for task_1, served on
// its own test server the way podium-server serves the desktop origin.
type harness struct {
	svc   *Service
	voxl  *fakeVoxl
	tty   *fakeVoxl
	front *httptest.Server
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	v, tty := newFakeVoxl(t), newFakeVoxl(t)
	upURL, err := url.Parse(v.srv.URL)
	require.NoError(t, err)
	ttyURL, err := url.Parse(tty.srv.URL)
	require.NoError(t, err)
	h := &harness{voxl: v, tty: tty}
	front := httptest.NewUnstartedServer(nil)
	front.Start()
	t.Cleanup(front.Close)
	base, err := url.Parse(front.URL)
	require.NoError(t, err)
	svc, err := New(base, "https://podium.example", resolverFunc(func(_ context.Context, taskID, port string) (Upstream, error) {
		if taskID != "task_1" {
			return Upstream{}, ErrNoDesktop
		}
		u := upURL
		if port == TerminalPortName {
			u = ttyURL
		}
		return Upstream{URL: u, Apply: func(r *http.Request) {
			r.AddCookie(&http.Cookie{Name: "_modal_connect_token", Value: "provider-secret"})
		}}, nil
	}), nil)
	require.NoError(t, err)
	front.Config.Handler = svc
	h.svc, h.front = svc, front
	return h
}

func (h *harness) link(t *testing.T, task string) string {
	t.Helper()
	return h.linkTo(t, task, PortName)
}

func (h *harness) linkTo(t *testing.T, task, port string) string {
	t.Helper()
	link, exp, err := h.svc.Mint(task, port, "alice@example.com")
	require.NoError(t, err)
	require.WithinDuration(t, time.Now().Add(TokenTTL), exp, 2*time.Second)
	return link
}

func get(t *testing.T, u string, hdr map[string]string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, u, nil)
	require.NoError(t, err)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	res, err := (&http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}).Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = res.Body.Close() })
	return res
}

func TestALinkServesVoxlUnderItsPathWithThePersonsCredentialsLeftBehind(t *testing.T) {
	h := newHarness(t)
	link := h.link(t, "task_1")
	require.True(t, strings.HasPrefix(link, h.svc.Origin()+"/t/task_1/"), link)

	res := get(t, link+"client.js", map[string]string{
		"Cookie":           "podium_session=the-persons",
		"Authorization":    "Bearer the-persons",
		"Cf-Connecting-Ip": "203.0.113.9",
		"Origin":           h.svc.Origin(),
	})
	require.Equal(t, http.StatusOK, res.StatusCode)
	body, _ := io.ReadAll(res.Body)
	// The path after the token is voxl's own.
	require.Equal(t, "voxl page at /client.js", string(body))

	up := h.voxl.lastHeaders()
	require.Empty(t, up.Get("Authorization"))
	require.Empty(t, up.Get("Cf-Connecting-Ip"))
	// The provider's cookie replaces the person's.
	require.Equal(t, "_modal_connect_token=provider-secret", up.Get("Cookie"))
	// voxl answers only its own page; through the proxy, its page is the upstream's.
	require.Equal(t, h.voxl.srv.URL, up.Get("Origin"))

	// Nothing upstream sets state on the desktop origin, and only the UI may frame it.
	require.Empty(t, res.Header.Values("Set-Cookie"))
	require.Equal(t, "frame-ancestors https://podium.example", res.Header.Get("Content-Security-Policy"))
	require.Equal(t, "no-referrer", res.Header.Get("Referrer-Policy"))
}

func TestThePageItselfEndsInASlashSoVoxlsRelativeURLsResolve(t *testing.T) {
	h := newHarness(t)
	link := strings.TrimSuffix(h.link(t, "task_1"), "/")
	res := get(t, link, nil)
	require.Equal(t, http.StatusFound, res.StatusCode)
	require.Equal(t, strings.TrimPrefix(link, h.svc.Origin())+"/", res.Header.Get("Location"))
}

func TestABadLinkSaysNothingAboutWhetherADesktopExists(t *testing.T) {
	h := newHarness(t)
	link := h.link(t, "task_1")
	_, tok, _, ok := splitPath(strings.TrimPrefix(link, h.svc.Origin()))
	require.True(t, ok)

	for name, u := range map[string]string{
		"forged":          h.svc.Origin() + "/t/task_1/" + tok + "x/",
		"another task's":  h.svc.Origin() + "/t/task_2/" + tok + "/",
		"no token":        h.svc.Origin() + "/t/task_1/",
		"outside the /t/": h.svc.Origin() + "/",
	} {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, http.StatusNotFound, get(t, u, nil).StatusCode)
		})
	}
	require.Empty(t, h.voxl.lastHeaders(), "a refused request must not reach the desktop")
}

func TestAnExpiredLinkIsRefused(t *testing.T) {
	h := newHarness(t)
	link := h.link(t, "task_1")
	h.svc.signer.now = func() time.Time { return time.Now().Add(TokenTTL + time.Minute) }
	require.Equal(t, http.StatusNotFound, get(t, link, nil).StatusCode)
}

func TestALinkToADesktopThatStoppedSaysSo(t *testing.T) {
	h := newHarness(t)
	link := h.link(t, "task_gone")
	res := get(t, link, nil)
	require.Equal(t, http.StatusGone, res.StatusCode)
}

func TestOnlyTheDesktopOriginIsTheDesktops(t *testing.T) {
	h := newHarness(t)
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Host = h.front.Listener.Addr().String()
	require.True(t, h.svc.Handles(r))
	r.Host = "podium.example"
	require.False(t, h.svc.Handles(r))
}

func TestMintRefusesWhatIsNotATaskID(t *testing.T) {
	h := newHarness(t)
	for _, id := range []string{"", "a/b", "a?b", "a#b"} {
		_, _, err := h.svc.Mint(id, PortName, "alice")
		require.Error(t, err, id)
	}
	// Only the desktop and the terminal are served here, whatever else a task exposes.
	_, _, err := h.svc.Mint("task_1", "web", "alice")
	require.Error(t, err)
}

func TestATerminalLinkServesTheTerminalAndItsSocket(t *testing.T) {
	h := newHarness(t)
	link := h.linkTo(t, "task_1", TerminalPortName)
	require.NotEqual(t, h.link(t, "task_1"), link, "each port has a link of its own")

	res := get(t, link, map[string]string{"Cookie": "podium_session=the-persons"})
	require.Equal(t, http.StatusOK, res.StatusCode)
	body, _ := io.ReadAll(res.Body)
	require.Equal(t, "voxl page at /", string(body))
	require.Empty(t, h.voxl.lastHeaders(), "a terminal link reaches the terminal, not the desktop")
	require.Equal(t, "_modal_connect_token=provider-secret", h.tty.lastHeaders().Get("Cookie"))
	require.Equal(t, "frame-ancestors https://podium.example", res.Header.Get("Content-Security-Policy"))

	// The socket is piped through as it is, from the terminal's own page.
	c := dial(t, h, link)
	send(t, c, "0ls\r")
	require.Eventually(t, func() bool { return len(h.tty.received()) == 1 }, 3*time.Second, 10*time.Millisecond)
	require.Equal(t, []string{"0ls\r"}, h.tty.received())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, refused, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(link, "http")+"ws", &websocket.DialOptions{
		HTTPHeader: http.Header{"Origin": []string{"https://evil.example"}},
	})
	require.Error(t, err)
	require.NotNil(t, refused)
	require.Equal(t, http.StatusForbidden, refused.StatusCode)
}

// dial opens a viewer's socket the way voxl's page does: from the desktop origin.
func dial(t *testing.T, h *harness, link string) *websocket.Conn {
	t.Helper()
	ws := "ws" + strings.TrimPrefix(link, "http") + "ws"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, ws, &websocket.DialOptions{
		HTTPHeader: http.Header{"Origin": []string{h.svc.Origin()}},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.CloseNow() })
	// voxl's hello, through the bridge.
	_, b, err := c.Read(ctx)
	require.NoError(t, err)
	require.Equal(t, `{"t":"hello"}`, string(b))
	return c
}

func send(t *testing.T, c *websocket.Conn, msg string) {
	t.Helper()
	require.NoError(t, c.Write(context.Background(), websocket.MessageText, []byte(msg)))
}

func TestOnlyTheNewestViewerResizesTheDesktop(t *testing.T) {
	h := newHarness(t)
	link := h.link(t, "task_1")

	older := dial(t, h, link)
	send(t, older, "sz,1680,1600")
	require.Eventually(t, func() bool { return len(h.voxl.received()) == 1 }, 3*time.Second, 10*time.Millisecond)

	newer := dial(t, h, link)
	require.Eventually(t, func() bool { return h.svc.viewers.count("task_1") == 2 }, 3*time.Second, 10*time.Millisecond)
	send(t, newer, "sz,1200,900")
	require.Eventually(t, func() bool { return len(h.voxl.received()) == 2 }, 3*time.Second, 10*time.Millisecond)
	// The older viewer's resize is dropped; its input is not. (Each viewer is its own
	// connection to voxl, so only one viewer's messages arrive in a known order.)
	send(t, older, "sz,2560,1600")
	send(t, older, "m,10,10")
	require.Eventually(t, func() bool { return len(h.voxl.received()) == 3 }, 3*time.Second, 10*time.Millisecond)
	require.Equal(t, []string{"sz,1680,1600", "sz,1200,900", "m,10,10"}, h.voxl.received())

	// The newer viewer leaves: the older one has control again, at the size it last asked for.
	newer.Close(websocket.StatusNormalClosure, "")
	require.Eventually(t, func() bool {
		got := h.voxl.received()
		return len(got) == 4 && got[3] == "sz,2560,1600"
	}, 3*time.Second, 10*time.Millisecond)
}

func TestTheSocketMustComeFromTheDesktopsOwnPage(t *testing.T) {
	h := newHarness(t)
	link := h.link(t, "task_1")
	ws := "ws" + strings.TrimPrefix(link, "http") + "ws"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, res, err := websocket.Dial(ctx, ws, &websocket.DialOptions{
		HTTPHeader: http.Header{"Origin": []string{"https://evil.example"}},
	})
	require.Error(t, err)
	require.NotNil(t, res)
	require.Equal(t, http.StatusForbidden, res.StatusCode)
}

func TestTokensAreBoundToTheirTask(t *testing.T) {
	s, err := newSigner()
	require.NoError(t, err)
	tok, _, err := s.sign("task_1", PortName, "alice")
	require.NoError(t, err)
	c, err := s.verify(tok, "task_1")
	require.NoError(t, err)
	require.Equal(t, "alice", c.Login)
	require.Equal(t, PortName, c.Port)
	_, err = s.verify(tok, "task_2")
	require.ErrorIs(t, err, errBadToken)

	other, err := newSigner()
	require.NoError(t, err)
	_, err = other.verify(tok, "task_1")
	require.ErrorIs(t, err, errBadToken, "another process's key must not verify it")
}
