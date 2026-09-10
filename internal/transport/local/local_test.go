package local

import (
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/transport"
)

func TestNewRefusesNonLoopbackListenAddress(t *testing.T) {
	for _, addr := range []string{"0.0.0.0:8080", ":8080", "192.0.2.10:8080", "[::]:8080", "example.com:8080"} {
		t.Run(addr, func(t *testing.T) {
			_, err := New(Options{Listen: addr, Token: "t"})
			require.Error(t, err)
			require.Contains(t, err.Error(), "local transport")
			require.Contains(t, err.Error(), addr)
		})
	}
}

func TestNewAcceptsLoopbackListenAddress(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:8080", "localhost:8080", "[::1]:8080", "127.0.0.1:0"} {
		t.Run(addr, func(t *testing.T) {
			l, err := New(Options{Listen: addr, Token: "t"})
			require.NoError(t, err)
			require.Equal(t, addr, l.Addr())
		})
	}
}

func TestNewRequiresToken(t *testing.T) {
	_, err := New(Options{Listen: "127.0.0.1:8080"})
	require.ErrorContains(t, err, "PODIUM_LOCAL_TOKEN")
}

func TestNewDefaultsToLoopback(t *testing.T) {
	l, err := New(Options{Token: "t"})
	require.NoError(t, err)
	require.Equal(t, DefaultListen, l.Addr())
}

func TestIdentify(t *testing.T) {
	l, err := New(Options{Listen: "127.0.0.1:0", Token: "s3cret"})
	require.NoError(t, err)

	t.Run("valid token", func(t *testing.T) {
		r, _ := http.NewRequest(http.MethodPost, "/podium.v1.TaskService/GetTask", nil)
		r.Header.Set("Authorization", "Bearer s3cret")
		r.RemoteAddr = "127.0.0.1:54321"
		id, err := l.Identify(r)
		require.NoError(t, err)
		require.Equal(t, transport.KindLocalToken, id.Kind)
		require.Equal(t, "local", id.Login)
		require.Equal(t, "127.0.0.1:54321", id.RemoteAddr)
	})

	for name, header := range map[string]string{
		"missing":    "",
		"wrong":      "Bearer nope",
		"not bearer": "Basic s3cret",
		"bare":       "s3cret",
	} {
		t.Run(name, func(t *testing.T) {
			r, _ := http.NewRequest(http.MethodPost, "/podium.v1.TaskService/GetTask", nil)
			if header != "" {
				r.Header.Set("Authorization", header)
			}
			_, err := l.Identify(r)
			require.True(t, errors.Is(err, transport.ErrUnauthenticated), "got %v", err)
		})
	}
}

// TestNewAcceptsANonLoopbackAddressOnlyWithTheWaiver is the shipped container deployment:
// inside a container loopback is the container's own, so a server bound to it is unreachable
// even from the compose network. The waiver is an environment variable an operator sets on
// purpose, and the boundary moves to the published port.
func TestNewAcceptsANonLoopbackAddressOnlyWithTheWaiver(t *testing.T) {
	for _, addr := range []string{"0.0.0.0:8080", ":8080", "[::]:8080"} {
		t.Run(addr, func(t *testing.T) {
			_, err := New(Options{Listen: addr, Token: "t"})
			require.Error(t, err)
			require.Contains(t, err.Error(), UnsafeListenVar, "the error has to name the way out")

			l, err := New(Options{Listen: addr, Token: "t", AllowNonLoopback: true})
			require.NoError(t, err)
			require.Equal(t, addr, l.Addr())
		})
	}
}

// The waiver waives the loopback rule and nothing else: an address that is not a host:port
// at all is still a configuration error.
func TestTheWaiverStillRequiresAHostPort(t *testing.T) {
	for _, addr := range []string{"0.0.0.0", "", "8080"} {
		t.Run(addr, func(t *testing.T) {
			_, err := New(Options{Listen: addr, Token: "t", AllowNonLoopback: true})
			if addr == "" {
				require.NoError(t, err, "empty means the default, which is loopback")
				return
			}
			require.ErrorContains(t, err, "local transport")
		})
	}
}
