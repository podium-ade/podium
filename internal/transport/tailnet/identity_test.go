package tailnet

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"tailscale.com/client/tailscale/apitype"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/tailcfg"

	"github.com/podium-ade/podium/internal/transport"
)

// who builds a fabricated WhoIs answer. Everything the transport decides on is in here, so the
// decision table below needs no tailnet.
func who(name string, tags []string, login, display string) *apitype.WhoIsResponse {
	return &apitype.WhoIsResponse{
		Node: &tailcfg.Node{
			Name:     name + ".tail0a1b2c.ts.net.",
			StableID: tailcfg.StableNodeID("n" + name + "CNTRL"),
			Tags:     tags,
		},
		UserProfile: &tailcfg.UserProfile{LoginName: login, DisplayName: display},
	}
}

func TestClassify(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		who     *apitype.WhoIsResponse
		opts    IdentityOptions
		want    transport.Identity
		wantErr error
	}{
		{
			name: "node tag is a node",
			who:  who("podiumbot1", []string{"tag:podium-node"}, "tagged-devices", ""),
			want: transport.Identity{
				Kind:         transport.KindNode,
				Login:        "podiumbot1.tail0a1b2c.ts.net",
				NodeTags:     []string{"tag:podium-node"},
				NodeStableID: "npodiumbot1CNTRL",
				RemoteAddr:   "100.105.227.25:41234",
			},
		},
		{
			name: "extra tags alongside the node tag are kept",
			who:  who("podiumbot1", []string{"tag:prod", "tag:podium-node"}, "tagged-devices", ""),
			want: transport.Identity{
				Kind:         transport.KindNode,
				Login:        "podiumbot1.tail0a1b2c.ts.net",
				NodeTags:     []string{"tag:prod", "tag:podium-node"},
				NodeStableID: "npodiumbot1CNTRL",
				RemoteAddr:   "100.105.227.25:41234",
			},
		},
		{
			name:    "server tag is refused",
			who:     who("podium", []string{"tag:podium-server"}, "tagged-devices", ""),
			wantErr: transport.ErrForbidden,
		},
		{
			name:    "server tag wins over the node tag",
			who:     who("podium", []string{"tag:podium-node", "tag:podium-server"}, "", ""),
			wantErr: transport.ErrForbidden,
		},
		{
			name:    "some other tag is 403, not a user",
			who:     who("ci-runner", []string{"tag:ci"}, "alvaro@affiniti.com", "Alvaro"),
			wantErr: transport.ErrForbidden,
		},
		{
			name: "untagged device with a login is a user",
			who:  who("alvaros-macbook-pro", nil, "alvaro@affiniti.com", "Alvaro Ibarguen"),
			want: transport.Identity{
				Kind:         transport.KindUser,
				Login:        "alvaro@affiniti.com",
				DisplayName:  "Alvaro Ibarguen",
				NodeStableID: "nalvaros-macbook-proCNTRL",
				RemoteAddr:   "100.105.227.25:41234",
			},
		},
		{
			name:    "untagged device with no login is unauthenticated",
			who:     who("mystery", nil, "", ""),
			wantErr: transport.ErrUnauthenticated,
		},
		{
			name:    "no whois answer at all",
			who:     nil,
			wantErr: transport.ErrUnauthenticated,
		},
		{
			name:    "whois answer with no node",
			who:     &apitype.WhoIsResponse{UserProfile: &tailcfg.UserProfile{LoginName: "a@b.c"}},
			wantErr: transport.ErrUnauthenticated,
		},
		{
			name: "a custom required node tag replaces the default",
			who:  who("podiumbot1", []string{"tag:worker"}, "", ""),
			opts: IdentityOptions{NodeTag: "tag:worker"},
			want: transport.Identity{
				Kind:         transport.KindNode,
				Login:        "podiumbot1.tail0a1b2c.ts.net",
				NodeTags:     []string{"tag:worker"},
				NodeStableID: "npodiumbot1CNTRL",
				RemoteAddr:   "100.105.227.25:41234",
			},
		},
		{
			name:    "the default node tag stops working once a custom one is set",
			who:     who("podiumbot1", []string{"tag:podium-node"}, "", ""),
			opts:    IdentityOptions{NodeTag: "tag:worker"},
			wantErr: transport.ErrForbidden,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := classify(tc.who, "100.105.227.25:41234", tc.opts)
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
				require.Empty(t, got.Kind)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

// fakeUsers records what the transport wrote, and can fail on demand.
type fakeUsers struct {
	mu   sync.Mutex
	got  [][2]string
	fail error
}

func (f *fakeUsers) UpsertUser(_ context.Context, login, display string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail != nil {
		return f.fail
	}
	f.got = append(f.got, [2]string{login, display})
	return nil
}

func (f *fakeUsers) calls() [][2]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][2]string(nil), f.got...)
}

func identifierFor(answer *apitype.WhoIsResponse, err error, users UserStore) *identifier {
	return &identifier{
		who: func(context.Context, string) (*apitype.WhoIsResponse, error) {
			return answer, err
		},
		users:  users,
		logger: discardLogger(),
	}
}

func TestIdentifyRecordsAUserOnce(t *testing.T) {
	t.Parallel()
	users := &fakeUsers{}
	id := identifierFor(who("laptop", nil, "alvaro@affiniti.com", "Alvaro"), nil, users)

	req := httptest.NewRequest(http.MethodPost, "/podium.v1.TaskService/ListTasks", nil)
	req.RemoteAddr = "100.84.71.97:52000"
	for range 3 {
		got, err := id.Identify(req)
		require.NoError(t, err)
		require.Equal(t, transport.KindUser, got.Kind)
		require.Equal(t, "alvaro@affiniti.com", got.Login)
	}
	require.Equal(t, [][2]string{{"alvaro@affiniti.com", "Alvaro"}}, users.calls())
}

func TestIdentifyDoesNotRecordANode(t *testing.T) {
	t.Parallel()
	users := &fakeUsers{}
	id := identifierFor(who("podiumbot1", []string{"tag:podium-node"}, "tagged-devices", ""), nil, users)

	req := httptest.NewRequest(http.MethodPost, "/podium.v1.NodeService/Enroll", nil)
	req.RemoteAddr = "100.105.227.25:41234"
	got, err := id.Identify(req)
	require.NoError(t, err)
	require.Equal(t, transport.KindNode, got.Kind)
	require.Empty(t, users.calls())
}

func TestIdentifyStillAuthenticatesWhenTheUserWriteFails(t *testing.T) {
	t.Parallel()
	users := &fakeUsers{fail: errors.New("postgres is down")}
	id := identifierFor(who("laptop", nil, "alvaro@affiniti.com", "Alvaro"), nil, users)

	req := httptest.NewRequest(http.MethodPost, "/podium.v1.TaskService/ListTasks", nil)
	req.RemoteAddr = "100.84.71.97:52000"
	got, err := id.Identify(req)
	require.NoError(t, err)
	require.Equal(t, "alvaro@affiniti.com", got.Login)
}

func TestIdentifyWhoIsFailureIsUnauthenticated(t *testing.T) {
	t.Parallel()
	id := identifierFor(nil, errors.New("peer not found"), nil)
	req := httptest.NewRequest(http.MethodPost, "/podium.v1.TaskService/ListTasks", nil)
	req.RemoteAddr = "192.0.2.7:9"
	_, err := id.Identify(req)
	require.ErrorIs(t, err, transport.ErrUnauthenticated)
}

func TestWithIdentityAnswers403ForAForbiddenTag(t *testing.T) {
	t.Parallel()
	id := identifierFor(who("ci", []string{"tag:ci"}, "", ""), nil, nil)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/podium.v1.TaskService/ListTasks", nil)
	req.RemoteAddr = "100.64.0.9:1"

	transport.WithIdentity(stubListener{id}, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("a forbidden caller reached the handler")
	})).ServeHTTP(rec, req)

	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Empty(t, rec.Header().Get("WWW-Authenticate"))
}

// stubListener adapts an identifier to transport.Listener for the middleware test.
type stubListener struct{ *identifier }

func (stubListener) Listen(context.Context) (net.Listener, error) { return nil, nil }

func TestReadyDetail(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	self := func(expiry *time.Time) *ipnstate.Status {
		return &ipnstate.Status{
			BackendState: "Running",
			Self:         &ipnstate.PeerStatus{DNSName: "podium.tail0a1b2c.ts.net.", KeyExpiry: expiry},
		}
	}

	t.Run("a tagged device never expires", func(t *testing.T) {
		t.Parallel()
		detail, err := readyDetail(self(nil), now)
		require.NoError(t, err)
		require.Equal(t, "tailnet podium.tail0a1b2c.ts.net", detail)
	})

	t.Run("a distant expiry is reported and healthy", func(t *testing.T) {
		t.Parallel()
		exp := now.Add(90 * 24 * time.Hour)
		detail, err := readyDetail(self(&exp), now)
		require.NoError(t, err)
		require.Contains(t, detail, "expires in 90d")
	})

	t.Run("an expiry inside 30 days is not ready", func(t *testing.T) {
		t.Parallel()
		exp := now.Add(10 * 24 * time.Hour)
		_, err := readyDetail(self(&exp), now)
		require.ErrorContains(t, err, "node key expires in")
	})

	t.Run("a backend that is not Running is not ready", func(t *testing.T) {
		t.Parallel()
		st := self(nil)
		st.BackendState = "NeedsLogin"
		_, err := readyDetail(st, now)
		require.ErrorContains(t, err, "NeedsLogin")
	})
}

func TestNodeHostname(t *testing.T) {
	t.Parallel()
	require.Equal(t, "podium-node-podiumbot1", NodeHostname("podiumbot1"))
	require.Equal(t, "podium-node-podiumbot1", NodeHostname("podiumbot1.local"))
	require.Equal(t, "podium-node-alvaros-macbook-pro", NodeHostname("Alvaros-MacBook-Pro.local"))
	require.Equal(t, "podium-node-worker", NodeHostname("!!!"))
	require.Equal(t, "podium-node-web-01", NodeHostname("web_01"))
}

func TestCheckHTTPSExplainsWhatToEnable(t *testing.T) {
	t.Parallel()
	base := &ipnstate.Status{
		Self:           &ipnstate.PeerStatus{HostName: "podium", DNSName: "podium.tail0a1b2c.ts.net."},
		CurrentTailnet: &ipnstate.TailnetStatus{MagicDNSEnabled: true},
		CertDomains:    []string{"podium.tail0a1b2c.ts.net"},
	}
	require.NoError(t, checkHTTPS(base))

	noCerts := *base
	noCerts.CertDomains = nil
	require.ErrorContains(t, checkHTTPS(&noCerts), "HTTPS certificates are not enabled")

	noMagicDNS := *base
	noMagicDNS.CurrentTailnet = &ipnstate.TailnetStatus{}
	require.ErrorContains(t, checkHTTPS(&noMagicDNS), "MagicDNS is not enabled")
}

func TestRequireAuthKey(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.ErrorContains(t, requireAuthKey(dir, ""), "TS_AUTHKEY")
	require.NoError(t, requireAuthKey(dir, "tskey-auth-notreal"))

	// tsnet writes the machine key before it has registered anything, so a first run that
	// failed on a bad key leaves this behind. It must not count as a device: the next run
	// would skip the key check and then block forever on an interactive login.
	require.NoError(t, os.WriteFile(filepath.Join(dir, stateFile),
		[]byte(`{"_machinekey":"privkey:38"}`), 0o600))
	require.ErrorContains(t, requireAuthKey(dir, ""), "TS_AUTHKEY",
		"a machine key alone is a failed first run, not a device")

	require.NoError(t, os.WriteFile(filepath.Join(dir, stateFile),
		[]byte(`{"_machinekey":"privkey:38","_current-profile":"profile-abc"}`), 0o600))
	require.NoError(t, requireAuthKey(dir, ""), "an existing device needs no key")
}

func TestHasDevice(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.False(t, hasDevice(dir), "no state file at all")

	require.NoError(t, os.WriteFile(filepath.Join(dir, stateFile), []byte("{}"), 0o600))
	require.False(t, hasDevice(dir), "an empty state file")

	require.NoError(t, os.WriteFile(filepath.Join(dir, stateFile), []byte("not json"), 0o600))
	require.False(t, hasDevice(dir), "unreadable state is not proof of a device")

	require.NoError(t, os.WriteFile(filepath.Join(dir, stateFile),
		[]byte(`{"_machinekey":"privkey:38","profile-abc":{}}`), 0o600))
	require.True(t, hasDevice(dir))
}

// Without an auth key tsnet prints a login URL and waits forever, which for an unattended
// daemon is a process that is neither up nor dead.
func TestUpContextIsBoundedOnlyWhenThereIsNothingToLogInWith(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	ctx, cancel := upContext(t.Context(), "", dir)
	defer cancel()
	_, ok := ctx.Deadline()
	require.True(t, ok, "a first run with no key must not hang")

	ctx, cancel = upContext(t.Context(), "tskey-auth-notreal", dir)
	defer cancel()
	_, ok = ctx.Deadline()
	require.False(t, ok, "registration with a key may take as long as it takes")

	require.NoError(t, os.WriteFile(filepath.Join(dir, stateFile),
		[]byte(`{"_machinekey":"privkey:38","profile-abc":{}}`), 0o600))
	ctx, cancel = upContext(t.Context(), "", dir)
	defer cancel()
	_, ok = ctx.Deadline()
	require.False(t, ok, "an existing device is not logging in")
}

func TestNodeClientRequiresAStateDir(t *testing.T) {
	t.Parallel()
	_, err := NewClient(ClientOptions{AuthKey: "tskey-auth-notreal"})
	require.ErrorContains(t, err, "state dir is required")
}

func TestNodeClientAuthKeyMessageNamesBothKeys(t *testing.T) {
	t.Parallel()
	_, err := NewClient(ClientOptions{StateDir: t.TempDir()})
	require.ErrorContains(t, err, "PODIUM_NODE_TS_AUTHKEY")
	require.ErrorContains(t, err, "not the Podium enrollment token")
}

// The node's stream is Connect bidi, which Connect refuses on HTTP/1.1. tsnet's own
// HTTPClient sets DialContext, which turns Go's automatic HTTP/2 upgrade off, so the
// protocol set has to be declared — this asserts it is.
func TestNodeHTTPClientSpeaksHTTP2Only(t *testing.T) {
	t.Parallel()
	c, err := NewClient(ClientOptions{StateDir: t.TempDir(), AuthKey: "tskey-auth-notreal"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })

	tr, ok := c.HTTPClient().Transport.(*http.Transport)
	require.True(t, ok)
	require.NotNil(t, tr.Protocols)
	require.True(t, tr.Protocols.HTTP2(), "bidi streams need HTTP/2")
	require.False(t, tr.Protocols.HTTP1(), "HTTP/1.1 would silently break the node stream")
	require.NotNil(t, tr.DialContext, "the node must dial through the tailnet")
}

func TestUserClientSpeaksBothProtocolsAndCarriesNoCredential(t *testing.T) {
	t.Parallel()
	tr, ok := NewUserClient().Transport.(*http.Transport)
	require.True(t, ok)
	require.True(t, tr.Protocols.HTTP1())
	require.True(t, tr.Protocols.HTTP2())
}

func TestTLSConfigAdvertisesH2First(t *testing.T) {
	t.Parallel()
	cfg := tlsConfig(nil)
	require.Equal(t, []string{"h2", "http/1.1"}, cfg.NextProtos)
	require.EqualValues(t, tls.VersionTLS12, cfg.MinVersion)
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
