package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/sandbox"
	"github.com/podium-ade/podium/internal/server/desktop"
	"github.com/podium-ade/podium/internal/server/store"
)

// fakeProvider is a cloud provider whose ports, like Modal's, answer only with a credential the
// preview URL does not carry.
type fakeProvider struct {
	sandbox.Provider
	gotID   string
	gotPort int
	err     error
}

func (f *fakeProvider) Expose(_ context.Context, id string, port int) (sandbox.Endpoint, error) {
	f.gotID, f.gotPort = id, port
	if f.err != nil {
		return sandbox.Endpoint{}, f.err
	}
	return sandbox.Endpoint{
		URL:     "https://ta-01abc-6901.w.modal.host",
		Cookies: []*http.Cookie{{Name: "_modal_connect_token", Value: "minted-for-the-server"}},
	}, nil
}

func TestADesktopInACloudSandboxIsReachedWithTheProvidersCredentials(t *testing.T) {
	fake := &fakeProvider{}
	var asked string
	d := &desktopUpstreams{provider: func(_ context.Context, name string) (sandbox.Provider, error) {
		asked = name
		return fake, nil
	}}

	up, err := d.sandboxUpstream(context.Background(),
		store.Task{ID: "task_1", SandboxProvider: "modal", SandboxID: "sb-123"}, 6901)
	require.NoError(t, err)
	require.Equal(t, "modal", asked, "the provider the task ran on, from the server's own configuration")
	require.Equal(t, "sb-123", fake.gotID)
	require.Equal(t, 6901, fake.gotPort)
	require.Equal(t, "https://ta-01abc-6901.w.modal.host", up.URL.String())

	r := httptest.NewRequest(http.MethodGet, "https://ta-01abc-6901.w.modal.host/", nil)
	up.Apply(r)
	c, err := r.Cookie("_modal_connect_token")
	require.NoError(t, err, "every request carries the provider's credential")
	require.Equal(t, "minted-for-the-server", c.Value)
}

func TestADesktopWhoseSandboxIsNotRecordedHasNoDesktop(t *testing.T) {
	d := &desktopUpstreams{provider: func(context.Context, string) (sandbox.Provider, error) {
		t.Fatal("no provider should be built for a task with no sandbox")
		return nil, nil
	}}
	_, err := d.sandboxUpstream(context.Background(), store.Task{ID: "task_1"}, 6901)
	require.Error(t, err)
}

func TestAProviderThatCannotExposeThePortSaysWhich(t *testing.T) {
	d := &desktopUpstreams{provider: func(context.Context, string) (sandbox.Provider, error) {
		return &fakeProvider{err: errors.New("sandbox is gone")}, nil
	}}
	_, err := d.sandboxUpstream(context.Background(),
		store.Task{ID: "task_1", SandboxProvider: "modal", SandboxID: "sb-123"}, 6901)
	require.ErrorContains(t, err, "the modal sandbox's port 6901: sandbox is gone")
}

func TestALANDesktopIsItsPreviewAddress(t *testing.T) {
	up, err := plainUpstream("http://192.168.1.201:6901")
	require.NoError(t, err)
	require.Equal(t, "192.168.1.201:6901", up.URL.Host)
	require.Nil(t, up.Apply, "a LAN preview has no credential to add")

	_, err = plainUpstream("")
	require.Error(t, err)
}

func TestATaskThatStoppedIsForgottenOnEveryPort(t *testing.T) {
	now := time.Now()
	d := &desktopUpstreams{cache: map[string]cachedUpstream{}, now: func() time.Time { return now }}
	for _, k := range []string{"task_1/desktop", "task_1/terminal", "task_10/desktop"} {
		d.cache[k] = cachedUpstream{up: desktop.Upstream{}, expires: now.Add(time.Minute)}
	}
	d.forget("task_1")
	_, desk := d.cached("task_1/desktop")
	_, term := d.cached("task_1/terminal")
	_, other := d.cached("task_10/desktop")
	require.False(t, desk)
	require.False(t, term)
	require.True(t, other, "a task whose id starts with another's is its own")
}
