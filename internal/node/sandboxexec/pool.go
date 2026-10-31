package sandboxexec

// A pool of sandboxes made before any task asks for one.
//
// A task on a sandbox node pays, before its first instruction runs, for the provider to make a
// sandbox, for it to become ready, for the agent to be uploaded and started, and for its port to
// be exposed. For a turn a person is waiting on — the assistant, placed on a provider — that is
// most of the wait. So a node can keep a few sandboxes of ONE shape already through all of that,
// and a task whose sandbox would be exactly that shape takes one and goes straight to its run.
//
// Only an exact match is taken: the same image or snapshot, the same size, no provider overrides
// and no ports beyond the agent's. Anything else is made the ordinary way, so a pool can only
// ever make a task start sooner, never start it in the wrong place. The pool is behind
// sandbox.Provider like everything else here: it works the same on every provider.
//
// A pooled sandbox is the node's own until a task takes it, and costs what any sandbox costs
// while it waits — the reason the pool is small, off by default, and deleted when the node stops.
// Its ids are kept on disk, so a node that died with a pool deletes it when it starts again.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/podium-ade/podium/internal/sandbox"
	"github.com/podium-ade/podium/internal/sandboxagent/wire"
	"github.com/podium-ade/podium/pkg/spec"
)

// PoolOptions configures a pool.
type PoolOptions struct {
	// Size is how many ready sandboxes to keep. Zero is no pool.
	Size int
	// Image is the image the pooled sandboxes boot, on a provider that boots images. Empty
	// with a provider that cannot is the node's template.
	Image string
	// TTL is how long an unclaimed sandbox lives before the provider stops it; a taken one
	// gets its task's own. Zero is poolTTL.
	TTL time.Duration
}

const (
	poolFile = "pool.json"
	poolTTL  = 6 * time.Hour
	// poolRetry is how long the pool waits after failing to make a sandbox.
	poolRetry = 30 * time.Second
)

// pooled is one ready sandbox: made, its agent started under token, its port exposed.
type pooled struct {
	ID      string              `json:"id"`
	Token   string              `json:"token"`
	URL     string              `json:"url"`
	Header  map[string][]string `json:"header,omitempty"`
	Cookies map[string]string   `json:"cookies,omitempty"`
}

// pool keeps Size sandboxes of one shape ready.
type pool struct {
	e        *Executor
	opts     PoolOptions
	strategy string
	caps     sandbox.Capabilities
	shape    sandbox.CreateRequest

	mu      sync.Mutex
	planned bool
	ready   []pooled
	making  int
	wake    chan struct{}
	stop    context.CancelFunc
	done    chan struct{}
}

// startPool begins filling a pool, after deleting whatever a previous run of this node left in
// one. It returns nil when the pool is off or its shape cannot be planned.
func (e *Executor) startPool(o PoolOptions) *pool {
	if o.Size <= 0 {
		return nil
	}
	if o.TTL <= 0 {
		o.TTL = poolTTL
	}
	p := &pool{e: e, opts: o, wake: make(chan struct{}, 1), done: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	p.stop = cancel
	p.deleteLeftovers(ctx)
	go p.fill(ctx)
	return p
}

// planShape works out the pool's shape. It is done when the pool first fills rather than when
// the node starts, because a provider configured from the control plane has no capabilities
// until its configuration arrives.
func (p *pool) planShape() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.planned {
		return true
	}
	strategy, caps, shape, err := p.e.plan(poolSpec(p.opts.Image))
	if err != nil {
		p.e.log.Warn("sandbox pool waits: its shape cannot run here yet", "error", err)
		return false
	}
	p.strategy, p.caps, p.shape, p.planned = strategy, caps, shape, true
	return true
}

// matches reports whether a task's sandbox would be exactly the pool's.
func (p *pool) matches(strategy string, req sandbox.CreateRequest) bool {
	if p == nil {
		return false
	}
	p.mu.Lock()
	planned := p.planned
	p.mu.Unlock()
	return planned && strategy == p.strategy &&
		req.Image == p.shape.Image && req.Snapshot == p.shape.Snapshot &&
		req.CPU == p.shape.CPU && req.MemoryMB == p.shape.MemoryMB &&
		len(req.Settings) == 0 && req.Registry == nil &&
		slices.Equal(req.Ports, []int{wire.AgentPort})
}

// take hands one ready sandbox to a task, and starts making its replacement.
func (p *pool) take(strategy string, req sandbox.CreateRequest) (pooled, bool) {
	if !p.matches(strategy, req) {
		return pooled{}, false
	}
	p.mu.Lock()
	if len(p.ready) == 0 {
		p.mu.Unlock()
		return pooled{}, false
	}
	got := p.ready[0]
	p.ready = p.ready[1:]
	p.persistLocked()
	p.mu.Unlock()
	select {
	case p.wake <- struct{}{}:
	default:
	}
	return got, true
}

// fill keeps the pool at its size until it is stopped.
func (p *pool) fill(ctx context.Context) {
	defer close(p.done)
	for !p.planShape() {
		select {
		case <-ctx.Done():
			return
		case <-time.After(poolRetry):
		}
	}
	for {
		p.mu.Lock()
		short := p.opts.Size - len(p.ready) - p.making
		if short > 0 {
			p.making++
		}
		p.mu.Unlock()
		if short <= 0 {
			select {
			case <-ctx.Done():
				return
			case <-p.wake:
				continue
			}
		}
		sb, err := p.make(ctx)
		p.mu.Lock()
		p.making--
		if err == nil {
			p.ready = append(p.ready, sb)
			p.persistLocked()
		}
		p.mu.Unlock()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			p.e.log.Warn("making a pooled sandbox failed", "error", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(poolRetry):
			}
		}
	}
}

// make takes one sandbox through everything a task's run would, up to the run itself.
func (p *pool) make(ctx context.Context) (pooled, error) {
	req := p.shape
	req.TTL = p.opts.TTL
	req.Key = "pool-" + randomHex(8)
	id, err := p.e.p.Create(ctx, req)
	if err != nil {
		return pooled{}, err
	}
	fail := func(err error) (pooled, error) {
		dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
		defer cancel()
		_ = p.e.p.Delete(dctx, id)
		return pooled{}, err
	}
	if err := p.e.p.WaitReady(ctx, id); err != nil {
		return fail(err)
	}
	token, err := randomToken()
	if err != nil {
		return fail(err)
	}
	if err := p.e.startAgent(ctx, id, token, p.strategy, p.caps); err != nil {
		return fail(err)
	}
	ep, err := p.e.p.Expose(ctx, id, wire.AgentPort)
	if err != nil {
		return fail(err)
	}
	return pooled{ID: id, Token: token, URL: ep.URL, Header: ep.Header, Cookies: cookieMap(ep.Cookies)}, nil
}

// close stops filling and deletes every sandbox still waiting.
func (p *pool) close(ctx context.Context) {
	if p == nil {
		return
	}
	p.stop()
	<-p.done
	p.mu.Lock()
	left := p.ready
	p.ready = nil
	p.persistLocked()
	p.mu.Unlock()
	for _, sb := range left {
		if err := p.e.p.Delete(ctx, sb.ID); err != nil {
			p.e.log.Warn("deleting a pooled sandbox failed", "sandbox", sb.ID, "error", err)
		}
	}
}

// size is how many ready sandboxes the pool holds right now.
func (p *pool) size() int {
	if p == nil {
		return 0
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.ready)
}

// persistLocked writes the ready sandboxes' ids, so a node that dies does not leak them.
func (p *pool) persistLocked() {
	ids := make([]string, 0, len(p.ready))
	for _, sb := range p.ready {
		ids = append(ids, sb.ID)
	}
	raw, _ := json.Marshal(ids)
	path := filepath.Join(p.e.dir, poolFile)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err == nil {
		_ = os.Rename(tmp, path)
	}
}

// deleteLeftovers deletes the pool a previous run of this node left behind. Their agents' tokens
// died with that run, so they are of no use to anybody.
func (p *pool) deleteLeftovers(ctx context.Context) {
	raw, err := os.ReadFile(filepath.Join(p.e.dir, poolFile))
	if err != nil {
		return
	}
	var ids []string
	if json.Unmarshal(raw, &ids) != nil {
		return
	}
	for _, id := range ids {
		if err := p.e.p.Delete(ctx, id); err != nil {
			p.e.log.Warn("deleting a pooled sandbox a previous run left failed", "sandbox", id, "error", err)
		}
	}
	p.persistLocked()
}

// poolSpec is the task a pooled sandbox is made for: the pool's image, at the provider's
// default size, with nothing exposed — what an assistant placed on this provider asks for.
func poolSpec(image string) spec.TaskSpec {
	// Defaulted exactly as a submitted spec is, so the pool's size is the size a task that
	// names none arrives with.
	sp := spec.TaskSpec{Image: image}
	sp.ApplyDefaults()
	return sp
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
