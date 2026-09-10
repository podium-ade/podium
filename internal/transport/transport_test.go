package transport_test

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/transport"
)

// fakeListener identifies anyone presenting the header, and nobody else.
type fakeListener struct{ ok bool }

func (f fakeListener) Listen(context.Context) (net.Listener, error) { return nil, nil }

func (f fakeListener) Identify(*http.Request) (transport.Identity, error) {
	if !f.ok {
		return transport.Identity{}, transport.ErrUnauthenticated
	}
	return transport.Identity{Kind: transport.KindLocalToken, Login: "local"}, nil
}

func TestWithIdentityPassesIdentityThrough(t *testing.T) {
	var seen transport.Identity
	var ok bool
	h := transport.WithIdentity(fakeListener{ok: true}, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen, ok = transport.From(r.Context())
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	require.Equal(t, http.StatusOK, rec.Code)
	require.True(t, ok)
	require.Equal(t, "local", seen.Login)
	require.Equal(t, transport.KindLocalToken, seen.Kind)
}

func TestWithIdentityRejectsUnauthenticated(t *testing.T) {
	called := false
	h := transport.WithIdentity(fakeListener{ok: false}, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		called = true
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	require.Equal(t, http.StatusUnauthorized, rec.Code)
	require.Equal(t, "Bearer", rec.Header().Get("WWW-Authenticate"))
	require.False(t, called)
}

func TestFromReportsMissingIdentity(t *testing.T) {
	_, ok := transport.From(context.Background())
	require.False(t, ok)
}
