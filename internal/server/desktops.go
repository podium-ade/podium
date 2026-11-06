package server

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sync"
	"time"

	"github.com/podium-ade/podium/internal/sandbox"
	"github.com/podium-ade/podium/internal/server/desktop"
	"github.com/podium-ade/podium/internal/server/secrets"
	"github.com/podium-ade/podium/internal/server/store"
	"github.com/podium-ade/podium/pkg/spec"
)

// desktopEndpointTTL is how long a desktop's upstream is reused before it is looked up again.
// For a cloud sandbox the lookup asks the provider for a fresh endpoint (Modal mints a connect
// token), which is far too slow for every request a page makes.
const desktopEndpointTTL = 5 * time.Minute

// desktopUpstreams finds a running task's desktop: the "desktop" port its preview published.
//
//   - A cloud sandbox's port is behind the provider's proxy, and the URL the preview records
//     carries none of the provider's credentials (Modal's connect token is a cookie). The
//     server builds the provider from its own configuration and asks it for the endpoint, as
//     the snapshot deleter does: nothing about the run has to be kept for it.
//   - A LAN preview is a plain address this server may route to.
//   - A tailnet preview is on a device that refuses tagged callers, which this server is, by
//     design: it is refused here with that reason rather than timing out.
type desktopUpstreams struct {
	store *store.Store
	// provider builds a sandbox provider from the server's configuration: buildSandboxProvider,
	// replaced in tests.
	provider func(ctx context.Context, name string) (sandbox.Provider, error)

	mu    sync.Mutex
	cache map[string]cachedUpstream
	now   func() time.Time
}

type cachedUpstream struct {
	up      desktop.Upstream
	expires time.Time
}

func newDesktopUpstreams(st *store.Store, sec *secrets.Service) *desktopUpstreams {
	return &desktopUpstreams{
		store: st,
		provider: func(ctx context.Context, name string) (sandbox.Provider, error) {
			return buildSandboxProvider(ctx, sec, name)
		},
		cache: map[string]cachedUpstream{},
		now:   time.Now,
	}
}

func (d *desktopUpstreams) Resolve(ctx context.Context, taskID string) (desktop.Upstream, error) {
	task, err := d.store.GetTask(ctx, taskID)
	if errors.Is(err, store.ErrNotFound) {
		return desktop.Upstream{}, desktop.ErrNoDesktop
	}
	if err != nil {
		return desktop.Upstream{}, err
	}
	// Checked on every request, not only when the link was made: a link outlives its task.
	if task.Status != store.StatusRunning || task.Spec.Expose == nil {
		d.forget(taskID)
		return desktop.Upstream{}, desktop.ErrNoDesktop
	}
	port, ok := task.Spec.Expose.Ports[desktop.PortName]
	if !ok || port.Port == 0 {
		return desktop.Upstream{}, desktop.ErrNoDesktop
	}
	if up, ok := d.cached(taskID); ok {
		return up, nil
	}
	p, err := d.store.GetPreview(ctx, taskID)
	if errors.Is(err, store.ErrNotFound) {
		return desktop.Upstream{}, desktop.ErrNoDesktop
	}
	if err != nil {
		return desktop.Upstream{}, err
	}
	if !p.Live() {
		return desktop.Upstream{}, desktop.ErrNoDesktop
	}
	var up desktop.Upstream
	switch p.Via {
	case spec.ExposeViaSandbox:
		up, err = d.sandboxUpstream(ctx, task, port.Port)
	case spec.ExposeViaLAN:
		up, err = plainUpstream(p.URLs[desktop.PortName])
	default:
		err = fmt.Errorf("task %s published its desktop on %q, which podium-server cannot reach: tailnet previews refuse tagged devices, and the server is one", taskID, p.Via)
	}
	if err != nil {
		return desktop.Upstream{}, err
	}
	d.mu.Lock()
	d.cache[taskID] = cachedUpstream{up: up, expires: d.now().Add(desktopEndpointTTL)}
	d.mu.Unlock()
	return up, nil
}

func (d *desktopUpstreams) cached(taskID string) (desktop.Upstream, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	c, ok := d.cache[taskID]
	if !ok || !d.now().Before(c.expires) {
		delete(d.cache, taskID)
		return desktop.Upstream{}, false
	}
	return c.up, true
}

func (d *desktopUpstreams) forget(taskID string) {
	d.mu.Lock()
	delete(d.cache, taskID)
	d.mu.Unlock()
}

func (d *desktopUpstreams) sandboxUpstream(ctx context.Context, task store.Task, port int) (desktop.Upstream, error) {
	if task.SandboxProvider == "" || task.SandboxID == "" {
		return desktop.Upstream{}, desktop.ErrNoDesktop
	}
	p, err := d.provider(ctx, task.SandboxProvider)
	if err != nil {
		return desktop.Upstream{}, err
	}
	ep, err := p.Expose(ctx, task.SandboxID, port)
	if err != nil {
		return desktop.Upstream{}, fmt.Errorf("the %s sandbox's desktop port: %w", task.SandboxProvider, err)
	}
	u, err := url.Parse(ep.URL)
	if err != nil || u.Host == "" {
		return desktop.Upstream{}, fmt.Errorf("the %s sandbox's desktop port has no usable URL", task.SandboxProvider)
	}
	return desktop.Upstream{URL: u, Apply: ep.Apply}, nil
}

func plainUpstream(raw string) (desktop.Upstream, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return desktop.Upstream{}, fmt.Errorf("the desktop's preview URL %q is not usable", raw)
	}
	return desktop.Upstream{URL: u}, nil
}
