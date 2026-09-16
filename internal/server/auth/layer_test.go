package auth

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/proto/podium/v1/podiumv1connect"
	"github.com/podium-ade/podium/internal/transport"
)

type fakeListener struct {
	id  transport.Identity
	err error
}

func (f fakeListener) Listen(context.Context) (net.Listener, error) { return nil, nil }
func (f fakeListener) Identify(*http.Request) (transport.Identity, error) {
	return f.id, f.err
}

func TestWrapNilStoreIsInner(t *testing.T) {
	t.Parallel()
	inner := fakeListener{id: transport.Identity{Kind: transport.KindLocalToken, Login: "local"}}
	require.Equal(t, inner, Wrap(inner, nil))
}

func TestRestrictUnclaimedNoopWhenGoogleOff(t *testing.T) {
	t.Parallel()
	called := false
	h := RestrictUnclaimed(nil, false, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		called = true
	}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/podium.v1.TaskService/CreateTask", nil)
	h.ServeHTTP(rec, req)
	require.True(t, called)
}

func TestUnclaimedPath(t *testing.T) {
	t.Parallel()
	require.True(t, unclaimedPath(podiumv1connect.IdentityServiceWhoAmIProcedure))
	require.True(t, unclaimedPath(podiumv1connect.IdentityServiceClaimProcedure))
	require.False(t, unclaimedPath("/podium.v1.TaskService/CreateTask"))
}

func TestSessionTokenPrefersCookie(t *testing.T) {
	t.Parallel()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: CookieName, Value: "sess-cookie"})
	req.Header.Set("Authorization", "Bearer sess-header")
	require.Equal(t, "sess-cookie", sessionToken(req))
}

func TestSessionTokenIgnoresBearer(t *testing.T) {
	t.Parallel()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer sess-header")
	require.Empty(t, sessionToken(req), "v1 sessions are cookies; the bearer is the local token")
}

func TestGoogleEnabled(t *testing.T) {
	t.Parallel()
	require.False(t, Google{ClientID: "x"}.Enabled())
	require.False(t, Google{ClientSecret: "y"}.Enabled())
	require.True(t, Google{ClientID: "x", ClientSecret: "y"}.Enabled())
}
