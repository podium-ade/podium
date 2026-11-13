package webhooks

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// said is a handler that answers with its own name, so a test can tell which one served.
func said(name string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, name)
	})
}

// mounted runs Serve on its own goroutine until the returned stop is called, and waits for
// the path to be served before returning.
func mounted(t *testing.T, s *Server, addr, path string, h http.Handler) (stop func() error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx, addr, path, h) }()
	require.Eventually(t, func() bool {
		for _, p := range s.Paths(addr) {
			if p == path {
				return true
			}
		}
		select {
		case err := <-done:
			done <- err
			return true
		default:
			return false
		}
	}, 5*time.Second, 5*time.Millisecond)
	select {
	case err := <-done:
		cancel()
		require.NoError(t, err, "serving %s on %s", path, addr)
	default:
	}
	var stopped bool
	stop = func() error {
		if stopped {
			return nil
		}
		stopped = true
		cancel()
		return <-done
	}
	t.Cleanup(func() { _ = stop() })
	return stop
}

func get(t *testing.T, addr, path string) (int, string) {
	t.Helper()
	res, err := http.Post("http://"+addr+path, "application/json", strings.NewReader("{}"))
	require.NoError(t, err)
	defer func() { _ = res.Body.Close() }()
	body, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(body)
}

func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())
	return addr
}

func TestTwoSourcesOnOneAddressShareOneListener(t *testing.T) {
	s := New(nil)
	addr := freeAddr(t)
	stopGitHub := mounted(t, s, addr, "/webhooks/github", said("github"))
	mounted(t, s, addr, "/webhooks/linear", said("linear"))

	assert.Equal(t, []string{"/webhooks/github", "/webhooks/linear"}, s.Paths(addr))
	_, body := get(t, addr, "/webhooks/github")
	assert.Equal(t, "github", body)
	_, body = get(t, addr, "/webhooks/linear")
	assert.Equal(t, "linear", body)
	status, _ := get(t, addr, "/agent.v1.AgentService/GetConnections")
	assert.Equal(t, http.StatusNotFound, status, "only the mounted paths are served")

	// One source going leaves the other listening on the same port.
	require.NoError(t, stopGitHub())
	assert.True(t, s.Holds(addr))
	status, _ = get(t, addr, "/webhooks/github")
	assert.Equal(t, http.StatusNotFound, status)
	_, body = get(t, addr, "/webhooks/linear")
	assert.Equal(t, "linear", body)
}

func TestTwoAddressesAreTwoListeners(t *testing.T) {
	s := New(nil)
	gh, lin := freeAddr(t), freeAddr(t)
	mounted(t, s, gh, "/webhooks/github", said("github"))
	mounted(t, s, lin, "/webhooks/linear", said("linear"))

	_, body := get(t, gh, "/webhooks/github")
	assert.Equal(t, "github", body)
	status, _ := get(t, gh, "/webhooks/linear")
	assert.Equal(t, http.StatusNotFound, status, "each address serves only what was mounted on it")
	_, body = get(t, lin, "/webhooks/linear")
	assert.Equal(t, "linear", body)
}

// A source swapped for a new one on the same address: the listener another source holds
// never closes, and the new handler is the one that answers.
func TestASourceIsSwappedWhileTheListenerRuns(t *testing.T) {
	s := New(nil)
	addr := freeAddr(t)
	mounted(t, s, addr, "/webhooks/linear", said("linear"))
	old := mounted(t, s, addr, "/webhooks/github", said("old"))
	require.NoError(t, old())
	mounted(t, s, addr, "/webhooks/github", said("new"))

	_, body := get(t, addr, "/webhooks/github")
	assert.Equal(t, "new", body)
	_, body = get(t, addr, "/webhooks/linear")
	assert.Equal(t, "linear", body)
}

// The last path going closes the port, so a listener of somebody else's, or the same source
// moved back, can bind it again.
func TestTheLastPathToGoFreesTheAddress(t *testing.T) {
	s := New(nil)
	addr := freeAddr(t)
	stop := mounted(t, s, addr, "/webhooks/github", said("github"))
	assert.True(t, s.Holds(addr))
	require.NoError(t, stop())
	assert.False(t, s.Holds(addr))
	assert.Empty(t, s.Paths(addr))

	ln, err := net.Listen("tcp", addr)
	require.NoError(t, err, "the address is free once Serve has returned")
	require.NoError(t, ln.Close())

	mounted(t, s, addr, "/webhooks/github", said("again"))
	_, body := get(t, addr, "/webhooks/github")
	assert.Equal(t, "again", body)
}

func TestAPathIsServedOncePerAddress(t *testing.T) {
	s := New(nil)
	addr := freeAddr(t)
	mounted(t, s, addr, "/webhooks/linear", said("first"))
	err := s.Serve(context.Background(), addr, "/webhooks/linear", said("second"))
	require.ErrorContains(t, err, "already served")
	_, body := get(t, addr, "/webhooks/linear")
	assert.Equal(t, "first", body)
}

func TestAnAddressSomebodyElseHoldsIsAnError(t *testing.T) {
	held, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = held.Close() })

	s := New(nil)
	err = s.Serve(context.Background(), held.Addr().String(), "/webhooks/linear", said("linear"))
	require.ErrorContains(t, err, "listen on")
	assert.False(t, s.Holds(held.Addr().String()))
}

// Port 0 is what a test asks for; Addr is how it finds the port it got.
func TestAddrIsWhereAPortZeroListenerLanded(t *testing.T) {
	s := New(nil)
	mounted(t, s, "127.0.0.1:0", "/webhooks/linear", said("linear"))
	bound := s.Addr("127.0.0.1:0")
	require.NotEmpty(t, bound)
	_, body := get(t, bound, "/webhooks/linear")
	assert.Equal(t, "linear", body)
	assert.Empty(t, s.Addr("127.0.0.1:1"))
}
