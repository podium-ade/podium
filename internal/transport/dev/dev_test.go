package dev

import (
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/alvaroibarguen/podium/internal/transport"
)

func TestNewRefusesNonLoopbackListenAddress(t *testing.T) {
	for _, addr := range []string{"0.0.0.0:8080", ":8080", "192.168.1.10:8080", "[::]:8080", "example.com:8080"} {
		t.Run(addr, func(t *testing.T) {
			_, err := New(Options{Listen: addr, Token: "t"})
			require.Error(t, err)
			require.Contains(t, err.Error(), "dev transport")
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
	require.ErrorContains(t, err, "PODIUM_DEV_TOKEN")
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
		require.Equal(t, transport.KindDevToken, id.Kind)
		require.Equal(t, "dev", id.Login)
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
