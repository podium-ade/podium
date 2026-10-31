package sandboxexec

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/node/docker"
	"github.com/podium-ade/podium/internal/sandbox"
	"github.com/podium-ade/podium/internal/sandboxagent/wire"
	"github.com/podium-ade/podium/pkg/spec"
)

// poolProvider makes sandboxes that become ready, take an agent and expose a port, all at once.
type poolProvider struct {
	sandbox.Provider
	mu       sync.Mutex
	next     int
	created  []sandbox.CreateRequest
	deleted  []string
	extended map[string]time.Duration
}

func (p *poolProvider) Name() string { return "fake" }
func (p *poolProvider) Capabilities() sandbox.Capabilities {
	return sandbox.Capabilities{NativeImage: true}
}
func (p *poolProvider) Create(_ context.Context, req sandbox.CreateRequest) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.next++
	p.created = append(p.created, req)
	return fmt.Sprintf("sb-%d", p.next), nil
}
func (p *poolProvider) WaitReady(context.Context, string) error { return nil }
func (p *poolProvider) PutFile(context.Context, string, string, io.Reader, int64) error {
	return nil
}
func (p *poolProvider) Exec(context.Context, string, string, bool) (sandbox.ExecResult, error) {
	return sandbox.ExecResult{}, nil
}
func (p *poolProvider) Expose(_ context.Context, id string, port int) (sandbox.Endpoint, error) {
	return sandbox.Endpoint{URL: fmt.Sprintf("https://%s:%d", id, port)}, nil
}
func (p *poolProvider) ExtendTTL(_ context.Context, id string, d time.Duration) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.extended == nil {
		p.extended = map[string]time.Duration{}
	}
	p.extended[id] = d
	return nil
}
func (p *poolProvider) Delete(_ context.Context, id string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.deleted = append(p.deleted, id)
	return nil
}
func (p *poolProvider) madeCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.created)
}

func poolExecutor(t *testing.T, p *poolProvider, dir string, size int) *Executor {
	t.Helper()
	e, err := New(Options{
		Provider: p, DataDir: dir, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Agent: func() ([]byte, error) { return []byte("agent"), nil },
		Pool:  PoolOptions{Size: size, Image: "podium-agent-runtime:1"},
	})
	require.NoError(t, err)
	return e
}

func waitReady(t *testing.T, e *Executor, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for e.PoolReady() != n {
		if time.Now().After(deadline) {
			t.Fatalf("pool has %d ready, want %d", e.PoolReady(), n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestThePoolKeepsItsSizeAndATaskOfItsShapeTakesOne: the pool fills to its size, a task whose
// sandbox would be exactly the pool's shape takes one — already made, agent started, port
// exposed — and the pool makes a replacement.
func TestThePoolKeepsItsSizeAndATaskOfItsShapeTakesOne(t *testing.T) {
	p := &poolProvider{}
	e := poolExecutor(t, p, t.TempDir(), 2)
	t.Cleanup(func() { _ = e.Close() })
	waitReady(t, e, 2)
	for _, req := range p.created {
		assert.Equal(t, "podium-agent-runtime:1", req.Image)
		assert.Equal(t, poolTTL, req.TTL, "a waiting sandbox gets the pool's lifetime")
		assert.Contains(t, req.Key, "pool-")
	}

	sp := spec.TaskSpec{Image: "podium-agent-runtime:1", Timeout: spec.Duration(20 * time.Minute)}
	sp.ApplyDefaults()
	strategy, caps, creq, err := e.plan(sp)
	require.NoError(t, err)
	tk := &task{rec: record{TaskID: "t1", LeaseID: "l1", Provider: "fake"}}
	em := newEmitter(make(chan docker.Event, 16), 0)
	id, err := e.sandboxFor(context.Background(), tk, em, strategy, caps, creq)
	require.NoError(t, err)
	assert.Contains(t, []string{"sb-1", "sb-2"}, id, "a pooled sandbox, not a new one")
	assert.NotEmpty(t, tk.rec.Token)
	assert.Equal(t, fmt.Sprintf("https://%s:%d", id, wire.AgentPort), tk.rec.URL)
	assert.Equal(t, creq.TTL, p.extended[id], "a taken sandbox gets its task's lifetime")
	_, err = os.Stat(filepath.Join(e.TaskDir("t1"), recordFile))
	require.NoError(t, err, "recorded before anything can leave it behind")

	waitReady(t, e, 2)
	assert.Equal(t, 3, p.madeCount(), "the pool replaced what was taken")
}

// TestATaskOfAnotherShapeMakesItsOwn: anything but an exact match is made the ordinary way.
func TestATaskOfAnotherShapeMakesItsOwn(t *testing.T) {
	p := &poolProvider{}
	e := poolExecutor(t, p, t.TempDir(), 1)
	t.Cleanup(func() { _ = e.Close() })
	waitReady(t, e, 1)

	for name, sp := range map[string]spec.TaskSpec{
		"another image": {Image: "alpine:3"},
		"bigger":        {Image: "podium-agent-runtime:1", Resources: spec.Resources{CPU: 8, MemoryMB: 16384}},
		"exposes ports": {Image: "podium-agent-runtime:1", Expose: &spec.Expose{Ports: map[string]spec.ExposedPort{"web": {Port: 3000}}}},
	} {
		sp.ApplyDefaults()
		strategy, _, creq, err := e.plan(sp)
		require.NoError(t, err, name)
		_, took := e.pool.take(strategy, creq)
		assert.False(t, took, name)
	}
	assert.Equal(t, 1, e.PoolReady())
}

// TestAPoolIsDeletedWhenTheNodeStopsAndAfterItDies: closing deletes what is waiting, and a node
// that died with a pool deletes it when it starts again.
func TestAPoolIsDeletedWhenTheNodeStopsAndAfterItDies(t *testing.T) {
	dir := t.TempDir()
	p := &poolProvider{}
	e := poolExecutor(t, p, dir, 2)
	waitReady(t, e, 2)
	require.NoError(t, e.Close())
	assert.ElementsMatch(t, []string{"sb-1", "sb-2"}, p.deleted)

	// A node that died rather than stopped: its pool file still names two sandboxes.
	require.NoError(t, os.WriteFile(filepath.Join(dir, poolFile), []byte(`["sb-old-1","sb-old-2"]`), 0o600))
	again := &poolProvider{}
	e2 := poolExecutor(t, again, dir, 0)
	t.Cleanup(func() { _ = e2.Close() })
	assert.Empty(t, again.deleted, "no pool configured, so nothing to start or clean")
	e3 := poolExecutor(t, again, dir, 1)
	t.Cleanup(func() { _ = e3.Close() })
	assert.Contains(t, again.deleted, "sb-old-1")
	assert.Contains(t, again.deleted, "sb-old-2")
}
