// Package sandboxes runs cloud sandboxes for podium-server.
//
// A node is a machine of your own. A cloud provider is not one, and nobody should have to run a
// process to use it: configuring the provider on the Sandboxes screen is the whole of the setup.
// So for every configured provider the server runs that provider's RUNNER itself — the node
// machinery (internal/node) driving the provider's API, with the same executor, relay, event
// buffering, previews and adoption a node has — connected to the server's own node service in
// memory. To the scheduler it is a node with the provider's labels, which is how the existing
// placement, capacity and lease logic covers it unchanged; to an operator it is the provider,
// on the Sandboxes screen, and it never appears among their nodes.
//
// A runner's sandboxes are recorded under the server's state directory, so a restarted server
// adopts them. A provider removed from the Sandboxes screen stops its runner; one whose pool
// or cap changes is restarted with the new settings, adopting what was running.
package sandboxes

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/podium-ade/podium/internal/ids"
	"github.com/podium-ade/podium/internal/node"
	"github.com/podium-ade/podium/internal/node/sandboxexec"
	"github.com/podium-ade/podium/internal/proto/podium/v1/podiumv1connect"
	"github.com/podium-ade/podium/internal/server/store"
	"github.com/podium-ade/podium/pkg/spec"
)

// ManagedLabel marks a node row that is a provider's runner rather than a machine.
const ManagedLabel = store.ManagedNodeLabel

// The provider settings that configure its runner rather than the provider itself.
const (
	SettingPoolSize  = "pool_size"
	SettingPoolImage = "pool_image"
)

// defaultMaxTasks is a runner's slot count when its provider has no cap of its own: the
// account's limit, which the scheduler reads from the provider, is the real bound.
const defaultMaxTasks = 100

// reconcileEvery is how often the configured providers are read again.
var reconcileEvery = 10 * time.Second

// Options configures a Manager.
type Options struct {
	Store *store.Store
	// NodeService is the server's node service handler, served to runners in memory.
	NodeService http.Handler
	// ServerID names this server; a runner's node row is this server's, for its provider.
	ServerID string
	// Dir is where runners record their sandboxes.
	Dir    string
	Logger *slog.Logger
}

// Manager runs one runner per configured sandbox provider.
type Manager struct {
	o Options

	mu      sync.Mutex
	runners map[string]*runner
	wake    chan struct{}
}

type runner struct {
	cfg    runnerConfig
	node   *node.Node
	cancel context.CancelFunc
	done   chan struct{}
	// retiring is set once its provider is removed: it takes no new work — the server told it
	// the provider is not configured — and stops when its last task has finished.
	retiring bool
}

// runnerConfig is what restarts a runner when it changes.
type runnerConfig struct {
	maxTasks int
	pool     sandboxexec.PoolOptions
}

// New returns a Manager. It runs nothing until Run.
func New(o Options) *Manager {
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	return &Manager{o: o, runners: map[string]*runner{}, wake: make(chan struct{}, 1)}
}

// Wake reads the configured providers again now, rather than on the next tick: a provider
// saved on the Sandboxes screen gets its runner at once.
func (m *Manager) Wake() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

// Run keeps a runner going for every configured provider until ctx ends, then stops them all.
func (m *Manager) Run(ctx context.Context) error {
	if m.o.Dir == "" {
		return errors.New("sandboxes: no state directory")
	}
	httpClient, stop := serveInMemory(m.o.NodeService)
	defer stop()
	client := podiumv1connect.NewNodeServiceClient(httpClient, memURL)
	for {
		m.reconcile(ctx, client)
		select {
		case <-ctx.Done():
			m.stopAll()
			return nil
		case <-m.wake:
		case <-time.After(reconcileEvery):
		}
	}
}

// Running is the providers that have a runner right now.
func (m *Manager) Running() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.runners))
	for name := range m.runners {
		out = append(out, name)
	}
	return out
}

func (m *Manager) reconcile(ctx context.Context, client podiumv1connect.NodeServiceClient) {
	providers, err := m.o.Store.ListSandboxProviders(ctx)
	if err != nil {
		if ctx.Err() == nil {
			m.o.Logger.WarnContext(ctx, "reading the configured sandbox providers failed", "error", err)
		}
		return
	}
	want := make(map[string]runnerConfig, len(providers))
	for _, p := range providers {
		want[p.Name] = configOf(p)
	}

	m.mu.Lock()
	var stale []*runner
	for name, r := range m.runners {
		cfg, ok := want[name]
		switch {
		case !ok && r.node.Running() > 0:
			// Removed, with tasks still in its sandboxes: they finish here rather than being
			// cut off from the relay. No new work reaches it.
			if !r.retiring {
				r.retiring = true
				m.o.Logger.InfoContext(ctx, "sandbox provider removed; its runner stops once its tasks finish",
					"provider", name, "running", r.node.Running())
			}
		case !ok || cfg != r.cfg:
			stale = append(stale, r)
			delete(m.runners, name)
		}
	}
	m.mu.Unlock()
	for _, r := range stale {
		r.cancel()
		<-r.done
	}

	for name, cfg := range want {
		m.mu.Lock()
		r, running := m.runners[name]
		if running && r.retiring {
			// Set up again before it stopped: it takes work again, as configured.
			r.retiring = false
		}
		m.mu.Unlock()
		if running {
			continue
		}
		if err := m.start(ctx, client, name, cfg); err != nil {
			m.o.Logger.ErrorContext(ctx, "starting a sandbox provider's runner failed",
				"provider", name, "error", err)
		}
	}
}

func configOf(p store.SandboxProvider) runnerConfig {
	cfg := runnerConfig{maxTasks: defaultMaxTasks}
	if p.MaxConcurrent > 0 {
		cfg.maxTasks = int(p.MaxConcurrent)
	}
	if n, err := strconv.Atoi(p.Settings[SettingPoolSize]); err == nil && n > 0 {
		cfg.pool = sandboxexec.PoolOptions{Size: n, Image: p.Settings[SettingPoolImage]}
	}
	return cfg
}

func (m *Manager) start(ctx context.Context, client podiumv1connect.NodeServiceClient, provider string, cfg runnerConfig) error {
	dir := filepath.Join(m.o.Dir, provider)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	id, err := m.identity(ctx, provider, dir)
	if err != nil {
		return err
	}
	logger := m.o.Logger.With("provider", provider)
	n, err := node.NewManaged(ctx, node.ManagedOptions{
		Provider: provider, DataDir: dir, MaxTasks: cfg.maxTasks, Pool: cfg.pool,
		Client: client, Identity: id,
	}, logger)
	if err != nil {
		return err
	}
	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	r := &runner{cfg: cfg, node: n, cancel: cancel, done: make(chan struct{})}
	m.mu.Lock()
	m.runners[provider] = r
	m.mu.Unlock()
	go func() {
		defer close(r.done)
		defer func() { _ = n.Close() }()
		if err := n.Run(runCtx); err != nil && runCtx.Err() == nil {
			logger.ErrorContext(runCtx, "a sandbox provider's runner stopped", "error", err)
		}
	}()
	logger.InfoContext(ctx, "sandbox provider running", "node_id", id.NodeID,
		"max_tasks", cfg.maxTasks, "pool", cfg.pool.Size)
	return nil
}

func (m *Manager) stopAll() {
	m.mu.Lock()
	all := m.runners
	m.runners = map[string]*runner{}
	m.mu.Unlock()
	for _, r := range all {
		r.cancel()
		<-r.done
	}
}

// identity is the node row a provider's runner on this server streams as: the one recorded in
// its directory while that row still exists, else a new one. The server writes the row itself —
// there is no enrollment, because there is nobody to enroll.
func (m *Manager) identity(ctx context.Context, provider, dir string) (node.Identity, error) {
	if id, ok, err := node.LoadIdentity(dir); err != nil {
		return node.Identity{}, err
	} else if ok {
		if _, err := m.o.Store.GetNode(ctx, id.NodeID); err == nil {
			return id, nil
		} else if !errors.Is(err, store.ErrNotFound) {
			return node.Identity{}, err
		}
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return node.Identity{}, err
	}
	id := node.Identity{NodeID: ids.NewNode(), NodeKey: base64.RawURLEncoding.EncodeToString(raw)}
	name := "sandbox/" + provider
	if m.o.ServerID != "" {
		name += " @ " + m.o.ServerID
	}
	if _, err := m.o.Store.CreateNode(ctx, store.NewNode{
		ID:          id.NodeID,
		Name:        name,
		Labels:      []string{spec.LabelSandbox, spec.LabelSandboxProvider + provider, ManagedLabel},
		NodeKeyHash: store.HashToken(id.NodeKey),
		Status:      store.NodeOffline,
	}); err != nil {
		return node.Identity{}, fmt.Errorf("record the runner of sandbox provider %s: %w", provider, err)
	}
	if err := node.SaveIdentity(dir, id); err != nil {
		return node.Identity{}, err
	}
	return id, nil
}
