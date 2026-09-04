package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alvaroibarguen/podium/internal/transport"
)

const (
	agentProcedure = "/podium.agent.v1.AgentService/GetSettings"
	agentToken     = "agenttoken"
)

// echoHeaders is a stand-in conductor that reports what actually arrived.
func echoHeaders(t *testing.T) (*httptest.Server, *[]*http.Request) {
	t.Helper()
	var seen []*http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Clone(r.Context()))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"authorization":` + quote(r.Header.Get("Authorization")) +
			`,"login":` + quote(r.Header.Get(AgentLoginHeader)) +
			`,"path":` + quote(r.URL.Path) + `}`))
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

func quote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// call sends one request through the proxy with id as the authenticated identity, the way
// transport.WithIdentity would have.
func call(t *testing.T, h http.Handler, id *transport.Identity, mutate func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, agentProcedure, strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	if mutate != nil {
		mutate(req)
	}
	if id != nil {
		req = req.WithContext(transport.NewContext(req.Context(), *id))
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestAgentProxyReplacesTheCredentialAndAssertsTheLogin(t *testing.T) {
	upstream, seen := echoHeaders(t)
	proxy, err := NewAgentProxy(upstream.URL, agentToken, nil)
	require.NoError(t, err)

	rec := call(t, proxy, &transport.Identity{Kind: transport.KindUser, Login: "alice@example.com"},
		func(r *http.Request) {
			// What a browser actually sends: the dev token. The conductor must never see it.
			r.Header.Set("Authorization", "Bearer devtoken")
		})
	require.Equal(t, http.StatusOK, rec.Code)

	var got map[string]string
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.Equal(t, "Bearer "+agentToken, got["authorization"])
	assert.Equal(t, "alice@example.com", got["login"])
	assert.Equal(t, agentProcedure, got["path"], "the Connect procedure path is forwarded verbatim")
	require.Len(t, *seen, 1)
}

// The spoofing case. A client that writes its own X-Podium-Login must not be able to tell
// the conductor it is somebody else: the header is the server's word, and the server's only.
func TestAgentProxyDropsAClientSuppliedLogin(t *testing.T) {
	upstream, _ := echoHeaders(t)
	proxy, err := NewAgentProxy(upstream.URL, agentToken, nil)
	require.NoError(t, err)

	rec := call(t, proxy, &transport.Identity{Kind: transport.KindUser, Login: "alice@example.com"},
		func(r *http.Request) {
			r.Header.Set(AgentLoginHeader, "root")
			r.Header.Add(AgentLoginHeader, "admin")
		})
	require.Equal(t, http.StatusOK, rec.Code)

	var got map[string]string
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.Equal(t, "alice@example.com", got["login"])
	assert.NotContains(t, got["login"], "root")
	assert.NotContains(t, got["login"], "admin")
}

func TestAgentProxyLoginPerIdentityKind(t *testing.T) {
	upstream, _ := echoHeaders(t)
	proxy, err := NewAgentProxy(upstream.URL, agentToken, nil)
	require.NoError(t, err)

	tests := []struct {
		name string
		id   *transport.Identity
		want string
	}{
		{"a tailnet user", &transport.Identity{Kind: transport.KindUser, Login: "bob@example.com"}, "bob@example.com"},
		{"the dev token", &transport.Identity{Kind: transport.KindDevToken, Login: "dev"}, "dev"},
		// Only reachable by calling the proxy without the middleware, which is a wiring bug
		// rather than a request; it must still not produce an empty header.
		{"no identity at all", nil, "unknown"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := call(t, proxy, tc.id, nil)
			require.Equal(t, http.StatusOK, rec.Code)
			var got map[string]string
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
			assert.Equal(t, tc.want, got["login"])
		})
	}
}

func TestAgentProxyRefusesANode(t *testing.T) {
	upstream, seen := echoHeaders(t)
	proxy, err := NewAgentProxy(upstream.URL, agentToken, nil)
	require.NoError(t, err)

	rec := call(t, proxy, &transport.Identity{Kind: transport.KindNode, Login: "worker-1"}, nil)
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Empty(t, *seen, "the request must not reach the conductor at all")
}

// A conductor that is down has to look like a Connect error to a Connect client, or the
// page renders a JSON parse failure instead of "the conductor is down".
func TestAgentProxyAnswersAConnectErrorWhenTheConductorIsDown(t *testing.T) {
	upstream, _ := echoHeaders(t)
	url := upstream.URL
	upstream.Close()

	proxy, err := NewAgentProxy(url, agentToken, nil)
	require.NoError(t, err)
	rec := call(t, proxy, &transport.Identity{Kind: transport.KindDevToken, Login: "dev"}, nil)

	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	var body struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "unavailable", body.Code)
	assert.Equal(t, "podium-agent is not reachable", body.Message)
}

func TestAgentProxyStreamsWithoutBuffering(t *testing.T) {
	// FlushInterval -1 is what step 21's server-streaming StreamChat needs. The observable
	// part here is that the response is not answered with a Content-Length, i.e. it was
	// streamed rather than collected.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/connect+json")
		w.WriteHeader(http.StatusOK)
		for range 3 {
			_, _ = w.Write([]byte("frame\n"))
			w.(http.Flusher).Flush()
		}
	}))
	defer upstream.Close()

	proxy, err := NewAgentProxy(upstream.URL, agentToken, nil)
	require.NoError(t, err)
	srv := httptest.NewServer(proxy)
	defer srv.Close()

	res, err := srv.Client().Post(srv.URL+agentProcedure, "application/connect+json", strings.NewReader("{}"))
	require.NoError(t, err)
	defer func() { _ = res.Body.Close() }()
	body, err := io.ReadAll(res.Body)
	require.NoError(t, err)
	assert.Equal(t, "frame\nframe\nframe\n", string(body))
	assert.Equal(t, int64(-1), res.ContentLength, "a streamed response has no length")
}

// TestAgentProxyDeliversAFrameBeforeTheStreamEnds is the one that matters for the chat.
//
// The test above proves the response is not collected into a Content-Length; this proves the
// stronger thing: a frame written and flushed upstream is READABLE THROUGH THE PROXY while
// the upstream handler is still running. A buffered proxy would make a chat look dead until
// the turn finished, which is the whole failure mode this pins.
//
// Two cases, because they are pinned by different things. A Connect stream never declares a
// length, and httputil.ReverseProxy flushes every write of a response whose ContentLength is
// -1 whatever FlushInterval says — so the first case is what StreamChat actually does and it
// would pass with the field unset. The second declares a length, which is the only shape
// where FlushInterval is consulted, and it is what stops somebody "tidying away" the
// assignment in agentproxy.go.
func TestAgentProxyDeliversAFrameBeforeTheStreamEnds(t *testing.T) {
	for _, tc := range []struct {
		name          string
		declareLength bool
	}{
		{name: "a Connect stream, with no declared length", declareLength: false},
		{name: "a response that declares its length", declareLength: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			release := make(chan struct{})
			// wrote is closed after the first frame is flushed, so the reader below cannot
			// be racing the handler's first write.
			wrote := make(chan struct{})
			frames := []string{"first\n", "second\n"}
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/connect+json")
				if tc.declareLength {
					w.Header().Set("Content-Length", strconv.Itoa(len(frames[0])+len(frames[1])))
				}
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(frames[0]))
				w.(http.Flusher).Flush()
				close(wrote)
				<-release
				_, _ = w.Write([]byte(frames[1]))
				w.(http.Flusher).Flush()
			}))
			defer upstream.Close()

			proxy, err := NewAgentProxy(upstream.URL, agentToken, nil)
			require.NoError(t, err)
			srv := httptest.NewServer(proxy)
			defer srv.Close()

			res, err := srv.Client().Post(srv.URL+agentProcedure,
				"application/connect+json", strings.NewReader("{}"))
			require.NoError(t, err)
			defer func() { _ = res.Body.Close() }()
			<-wrote

			// Read exactly the first frame. If the proxy buffered, this blocks until the
			// handler returns — which it cannot, because nothing has released it yet — and
			// the test times out rather than passing quietly.
			first := make([]byte, len(frames[0]))
			done := make(chan error, 1)
			go func() {
				_, err := io.ReadFull(res.Body, first)
				done <- err
			}()
			select {
			case err := <-done:
				require.NoError(t, err)
			case <-time.After(10 * time.Second):
				close(release)
				t.Fatal("the first frame did not arrive through the proxy while the stream was " +
					"still open: the response is being buffered, and a chat would look dead " +
					"until its turn finished")
			}
			assert.Equal(t, frames[0], string(first))

			// And the rest arrives once the upstream carries on.
			close(release)
			rest, err := io.ReadAll(res.Body)
			require.NoError(t, err)
			assert.Equal(t, frames[1], string(rest))
		})
	}
}

func TestNewAgentProxyRefusesAnUnparseableURL(t *testing.T) {
	_, err := NewAgentProxy("http://[::1", agentToken, nil)
	require.Error(t, err)
}
