// Package sandboxexec runs tasks in cloud sandboxes for podium-node. For each task it makes
// one sandbox through a sandbox.Provider, starts podium-sandbox-agent in it, and relays
// the agent's events and transfers. It never knows which provider it is driving.
package sandboxexec

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/podium-ade/podium/internal/node/docker"
	"github.com/podium-ade/podium/internal/sandbox"
	"github.com/podium-ade/podium/internal/sandboxagent/wire"
	"github.com/podium-ade/podium/pkg/spec"
)

const (
	agentDir     = "/tmp/podium-agent"
	agentBinPath = agentDir + "/podium-sandbox-agent"
	agentToken   = agentDir + "/token"
	recordFile   = "sandbox.json"
	// ttlMargin is how long a sandbox outlives its task's timeout, so the provider's own
	// stop is a backstop and never the thing that ends a run.
	ttlMargin = 15 * time.Minute
	// limitsEvery is how often the account's sandbox cap is read again.
	limitsEvery = time.Minute
)

// Options configures New.
type Options struct {
	Provider sandbox.Provider
	DataDir  string
	// Template is the provider snapshot a task that names none starts from.
	Template string
	// Agent returns podium-sandbox-agent for linux/amd64.
	Agent  func() ([]byte, error)
	Logger *slog.Logger
}

// Executor implements podium-node's executor on a sandbox provider.
type Executor struct {
	p      sandbox.Provider
	dir    string
	tmpl   atomic.Pointer[string]
	agent  func() ([]byte, error)
	log    *slog.Logger
	client *http.Client

	limit   atomic.Int32
	limitAt atomic.Int64

	mu    sync.Mutex
	tasks map[string]*task
}

// record is what the node keeps on disk about a task's sandbox, so a restarted daemon
// can find it again. Token is the agent's bearer secret. SENSITIVE.
type record struct {
	TaskID    string            `json:"task_id"`
	LeaseID   string            `json:"lease_id"`
	Provider  string            `json:"provider"`
	SandboxID string            `json:"sandbox_id"`
	URL       string            `json:"url,omitempty"`
	Header    http.Header       `json:"header,omitempty"`
	Cookies   map[string]string `json:"cookies,omitempty"`
	Token     string            `json:"token"`
	Holds     bool              `json:"holds,omitempty"`
	// Seq is the last agent event this node forwarded that was not a log chunk; an
	// adopting node does not repeat those.
	Seq uint64 `json:"seq,omitempty"`
}

type task struct {
	mu        sync.Mutex
	rec       record
	cancelled bool
}

// New returns an executor.
func New(o Options) (*Executor, error) {
	if o.Provider == nil {
		return nil, errors.New("sandboxexec: no provider")
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if err := os.MkdirAll(filepath.Join(o.DataDir, "tasks"), 0o700); err != nil {
		return nil, err
	}
	e := &Executor{p: o.Provider, dir: o.DataDir, agent: o.Agent, log: o.Logger,
		client: &http.Client{Timeout: 0}, tasks: map[string]*task{}}
	e.SetTemplate(o.Template)
	return e, nil
}

// SetTemplate changes the snapshot image tasks start from, for every task planned after it.
func (e *Executor) SetTemplate(t string) { e.tmpl.Store(&t) }

// TaskDir implements the node's executor.
func (e *Executor) TaskDir(taskID string) string { return filepath.Join(e.dir, "tasks", taskID) }

// ServerVersion implements the node's executor.
func (e *Executor) ServerVersion() string { return "sandbox/" + e.p.Name() }

// Close implements the node's executor.
func (e *Executor) Close() error { return nil }

// PruneImages implements the node's executor.
func (e *Executor) PruneImages(context.Context, docker.PruneRequest) ([]docker.ImageRecord, error) {
	return nil, nil
}

// GatewayPorts has nothing to say: a sandbox preview's URLs come from the provider.
func (e *Executor) GatewayPorts(context.Context, string) (map[int]int, error) { return nil, nil }

// plan is how one task runs: which strategy the agent uses, and what the sandbox is made
// from.
func (e *Executor) plan(sp spec.TaskSpec) (strategy string, req sandbox.CreateRequest, err error) {
	caps := e.p.Capabilities()
	if sp.Sandbox != nil && sp.Sandbox.Snapshot != "" {
		if !caps.Snapshot {
			return "", req, fmt.Errorf("%w: provider %s cannot start a sandbox from a snapshot", errSpec, e.p.Name())
		}
		req.Snapshot = sp.Sandbox.Snapshot
		strategy = wire.StrategyProcess
	} else if caps.NativeImage {
		req.Image = sp.Image
		strategy = wire.StrategyProcess
	} else if caps.Docker {
		req.Snapshot = *e.tmpl.Load()
		strategy = wire.StrategyDocker
	} else {
		return "", req, fmt.Errorf("%w: provider %s can neither boot an image nor run Docker", errSpec, e.p.Name())
	}
	if strategy == wire.StrategyProcess && len(sp.Sidecars) > 0 && !caps.Docker {
		return "", req, fmt.Errorf("%w: sidecars need Docker inside the sandbox, which provider %s does not have", errSpec, e.p.Name())
	}
	req.CPU, req.MemoryMB = sp.Resources.CPU, sp.Resources.MemoryMB
	ttl := sp.Timeout.Std() + ttlMargin
	if sp.Expose != nil && sp.Expose.TTL.Std() > 0 {
		ttl += sp.Expose.TTL.Std()
	}
	if caps.MaxTTL > 0 && ttl > caps.MaxTTL {
		if sp.Timeout.Std() > caps.MaxTTL {
			return "", req, fmt.Errorf("%w: timeout %s is longer than provider %s allows (%s)", errSpec, sp.Timeout, e.p.Name(), caps.MaxTTL)
		}
		ttl = caps.MaxTTL
	}
	req.TTL = ttl
	req.Ports = []int{wire.AgentPort}
	if sp.Expose != nil {
		for _, port := range sp.Expose.Ports {
			req.Ports = append(req.Ports, port.Port)
		}
	}
	return strategy, req, nil
}

var errSpec = errors.New("task spec")

// Run makes the task's sandbox, starts the agent in it and relays the run.
func (e *Executor) Run(ctx context.Context, req docker.Request, events chan<- docker.Event) (docker.Result, error) {
	em := newEmitter(events, 0)
	em.emit(docker.KindProvisioning, nil)

	t := &task{rec: record{TaskID: req.TaskID, LeaseID: req.LeaseID, Provider: e.p.Name(), Holds: req.Spec.Expose != nil}}
	e.mu.Lock()
	if _, dup := e.tasks[req.TaskID]; dup {
		e.mu.Unlock()
		err := fmt.Errorf("sandboxexec: task %s is already running", req.TaskID)
		em.emit(docker.KindError, docker.ErrorPayload{Message: err.Error(), AbortsRun: true})
		return docker.Result{}, err
	}
	e.tasks[req.TaskID] = t
	e.mu.Unlock()
	defer e.forget(req.TaskID)

	res, err := e.run(ctx, req, t, em)
	if err != nil && ctx.Err() != nil {
		// The daemon is going away, not the task: its sandbox stays for the next
		// incarnation to adopt.
		return docker.Result{}, err
	}
	if err != nil {
		var pe *sandbox.Error
		retryable := !errors.Is(err, errSpec) && (!errors.As(err, &pe) || pe.Retryable || pe.Code == "transport")
		if !errors.As(err, new(errAgentAborted)) {
			em.emit(docker.KindError, docker.ErrorPayload{Message: err.Error(), Retryable: retryable, AbortsRun: true})
		}
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
		defer cancel()
		if terr := e.Teardown(cctx, req.TaskID, false); terr != nil {
			e.log.Error("teardown after failed run", "task", req.TaskID, "error", terr)
		}
		return docker.Result{}, err
	}
	return res, nil
}

func (e *Executor) run(ctx context.Context, req docker.Request, t *task, em *emitter) (docker.Result, error) {
	strategy, creq, err := e.plan(req.Spec)
	if err != nil {
		return docker.Result{}, err
	}
	caps := e.p.Capabilities()
	creq.Key = req.LeaseID
	creq.Registry = registryFor(creq.Image, req.Registries)
	id, err := e.p.Create(ctx, creq)
	if err != nil {
		return docker.Result{}, fmt.Errorf("create sandbox: %w", err)
	}
	token, err := randomToken()
	if err != nil {
		return docker.Result{}, err
	}
	t.mu.Lock()
	t.rec.SandboxID, t.rec.Token = id, token
	t.mu.Unlock()
	if err := e.save(t); err != nil {
		return docker.Result{}, err
	}
	em.emit(docker.KindSandbox, docker.SandboxPayload{Provider: e.p.Name(), ID: id})
	if err := e.p.WaitReady(ctx, id); err != nil {
		return docker.Result{}, fmt.Errorf("sandbox %s: %w", id, err)
	}
	if err := e.startAgent(ctx, id, token, strategy, caps); err != nil {
		return docker.Result{}, err
	}
	ep, err := e.p.Expose(ctx, id, wire.AgentPort)
	if err != nil {
		return docker.Result{}, fmt.Errorf("expose agent: %w", err)
	}
	t.mu.Lock()
	t.rec.URL, t.rec.Header, t.rec.Cookies = ep.URL, ep.Header, cookieMap(ep.Cookies)
	t.mu.Unlock()
	if err := e.save(t); err != nil {
		return docker.Result{}, err
	}

	run := wire.RunRequest{
		TaskID: req.TaskID, LeaseID: req.LeaseID, Strategy: strategy, Spec: req.Spec,
		Secrets: req.Secrets, Registries: req.Registries,
		Artifacts: req.Artifacts != nil, Workspace: req.Workspace != nil,
	}
	if req.Preview != nil && req.Spec.Expose != nil {
		p, err := e.publish(ctx, id, req.Spec.Expose, caps)
		if err != nil {
			return docker.Result{}, err
		}
		run.Preview = p
	}
	c := e.agentClient(t)
	if err := c.waitHealthy(ctx); err != nil {
		return docker.Result{}, err
	}
	if err := c.postJSON(ctx, "/run", run, nil); err != nil {
		return docker.Result{}, fmt.Errorf("start run: %w", err)
	}
	t.mu.Lock()
	cancelled := t.cancelled
	t.mu.Unlock()
	if cancelled {
		_ = c.postJSON(ctx, "/cancel", nil, nil)
	}
	res, err := e.relay(ctx, t, c, req, em, nil)
	if err == nil && res.Held && req.Spec.Expose != nil {
		if xerr := e.p.ExtendTTL(ctx, id, req.Spec.Expose.TTL.Std()+ttlMargin); xerr != nil {
			e.log.Warn("extending a held sandbox's ttl failed", "task", req.TaskID, "error", xerr)
		}
	}
	return res, err
}

// startAgent installs the agent and starts it detached. The token goes in a file the agent
// reads once and deletes, never on a command line.
func (e *Executor) startAgent(ctx context.Context, id, token, strategy string, caps sandbox.Capabilities) error {
	if e.agent == nil {
		return errors.New("sandboxexec: no agent binary")
	}
	bin, err := e.agent()
	if err != nil {
		return err
	}
	if err := e.p.PutFile(ctx, id, agentBinPath, bytes.NewReader(bin), int64(len(bin))); err != nil {
		return fmt.Errorf("upload agent: %w", err)
	}
	if err := e.p.PutFile(ctx, id, agentToken, strings.NewReader(token), int64(len(token))); err != nil {
		return fmt.Errorf("upload agent token: %w", err)
	}
	if strategy == wire.StrategyDocker {
		// Docker comes up a few seconds after a sandbox reports ready.
		res, err := e.p.Exec(ctx, id, "for i in $(seq 120); do docker info >/dev/null 2>&1 && exit 0; sleep 1; done; exit 1", false)
		if err != nil || res.ExitCode != 0 {
			return fmt.Errorf("docker never came up in sandbox %s: %v %s", id, err, res.Stderr)
		}
	}
	host := "0.0.0.0"
	if caps.ExposeLoopback {
		host = "127.0.0.1"
	}
	cmd := fmt.Sprintf("chmod 0700 %s && exec %s -listen %s:%d -token-file %s -dir %s/state",
		agentBinPath, agentBinPath, host, wire.AgentPort, agentToken, agentDir)
	if caps.Tmpfs {
		cmd += " -tmpfs"
	}
	cmd += " >>" + agentDir + "/agent.log 2>&1"
	if _, err := e.p.Exec(ctx, id, cmd, true); err != nil {
		return fmt.Errorf("start agent: %w", err)
	}
	return nil
}

// publish exposes every port the task names and says where each one is.
func (e *Executor) publish(ctx context.Context, id string, x *spec.Expose, caps sandbox.Capabilities) (*docker.Preview, error) {
	p := &docker.Preview{Via: spec.ExposeViaSandbox, BindIP: "0.0.0.0", SamePorts: true}
	if caps.ExposeLoopback {
		p.BindIP = "127.0.0.1"
	}
	if !x.Publishes() {
		return p, nil
	}
	p.Published = map[string]string{}
	for name, port := range x.Ports {
		ep, err := e.p.Expose(ctx, id, port.Port)
		if err != nil {
			return nil, fmt.Errorf("expose port %s: %w", name, err)
		}
		// A preview link has to work in a browser, so the provider's credentials ride
		// in the URL the way the provider hands them out.
		p.Published[name] = withCredentials(ep)
	}
	return p, nil
}

// Cancel implements the node's executor.
func (e *Executor) Cancel(taskID string) {
	t := e.task(taskID)
	if t == nil {
		return
	}
	t.mu.Lock()
	t.cancelled = true
	ready := t.rec.URL != ""
	t.mu.Unlock()
	if ready {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := e.agentClient(t).postJSON(ctx, "/cancel", nil, nil); err != nil {
			e.log.Warn("cancel", "task", taskID, "error", err)
		}
	}
}

// Inject implements the node's executor.
func (e *Executor) Inject(taskID, text string) error {
	t := e.task(taskID)
	if t == nil {
		return fmt.Errorf("sandboxexec: task %s is not running here", taskID)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return e.agentClient(t).postJSON(ctx, "/inject", map[string]string{"text": text}, nil)
}

// Teardown deletes the task's sandbox and forgets it. keepWorkspace has nothing to keep:
// the workspace lives and dies with the sandbox.
func (e *Executor) Teardown(ctx context.Context, taskID string, _ bool) error {
	rec, err := e.load(taskID)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if rec.SandboxID != "" {
		if err := e.p.Delete(ctx, rec.SandboxID); err != nil {
			return fmt.Errorf("delete sandbox %s: %w", rec.SandboxID, err)
		}
	}
	return os.RemoveAll(e.TaskDir(taskID))
}

// Reap deletes a sandbox this node does not hold, for a task the control plane gave up on.
// One that is already gone is not an error.
func (e *Executor) Reap(ctx context.Context, id string) error {
	err := e.p.Delete(ctx, id)
	var pe *sandbox.Error
	if errors.As(err, &pe) && pe.Code == "not_found" {
		return nil
	}
	return err
}

// AccountLimit is the most sandboxes the provider account allows at once, 0 when unknown.
// It answers from the last read and reads again in the background at most once a minute,
// so a heartbeat never waits on the provider.
func (e *Executor) AccountLimit() int32 {
	now := time.Now().UnixNano()
	last := e.limitAt.Load()
	if now-last >= int64(limitsEvery) && e.limitAt.CompareAndSwap(last, now) {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if l, err := e.p.Limits(ctx); err == nil {
				e.limit.Store(int32(max(l.MaxSandboxes, 0)))
			}
		}()
	}
	return e.limit.Load()
}

// ListOwned is every task this node recorded a sandbox for.
func (e *Executor) ListOwned(context.Context) ([]docker.OwnedContainer, error) {
	entries, err := os.ReadDir(filepath.Join(e.dir, "tasks"))
	if err != nil {
		return nil, err
	}
	var out []docker.OwnedContainer
	for _, ent := range entries {
		rec, err := e.load(ent.Name())
		if err != nil || rec.SandboxID == "" {
			continue
		}
		out = append(out, docker.OwnedContainer{
			ID: rec.SandboxID, Name: rec.SandboxID, TaskID: rec.TaskID, LeaseID: rec.LeaseID,
			Role: docker.RoleTask, State: "running",
		})
	}
	return out, nil
}

// HeldExit implements the node's executor.
func (e *Executor) HeldExit(taskID string) (int, bool) {
	st, err := e.status(taskID)
	if err != nil || st.HeldExit == nil {
		return 0, false
	}
	return *st.HeldExit, true
}

// Holds implements the node's executor.
func (e *Executor) Holds(_ context.Context, taskID string) bool {
	rec, err := e.load(taskID)
	return err == nil && rec.Holds
}

// ListPreviews is every recorded task that publishes ports. Their URLs are the
// provider's, so there is no address or slot to give back.
func (e *Executor) ListPreviews(ctx context.Context) ([]docker.HeldPreview, error) {
	owned, err := e.ListOwned(ctx)
	if err != nil {
		return nil, err
	}
	var out []docker.HeldPreview
	for _, o := range owned {
		if rec, err := e.load(o.TaskID); err == nil && rec.Holds {
			out = append(out, docker.HeldPreview{TaskID: o.TaskID, Via: spec.ExposeViaSandbox})
		}
	}
	return out, nil
}

func (e *Executor) status(taskID string) (wire.Status, error) {
	rec, err := e.load(taskID)
	if err != nil {
		return wire.Status{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var st wire.Status
	err = e.agentClient(&task{rec: rec}).getJSON(ctx, "/status", &st)
	return st, err
}

func (e *Executor) task(taskID string) *task {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.tasks[taskID]
}

func (e *Executor) forget(taskID string) {
	e.mu.Lock()
	delete(e.tasks, taskID)
	e.mu.Unlock()
}

func (e *Executor) save(t *task) error {
	t.mu.Lock()
	raw, err := json.Marshal(t.rec)
	id := t.rec.TaskID
	t.mu.Unlock()
	if err != nil {
		return err
	}
	dir := e.TaskDir(id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp := filepath.Join(dir, recordFile+".tmp")
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, recordFile))
}

func (e *Executor) load(taskID string) (record, error) {
	var rec record
	raw, err := os.ReadFile(filepath.Join(e.TaskDir(taskID), recordFile))
	if err != nil {
		return rec, err
	}
	return rec, json.Unmarshal(raw, &rec)
}

func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func cookieMap(cs []*http.Cookie) map[string]string {
	if len(cs) == 0 {
		return nil
	}
	m := make(map[string]string, len(cs))
	for _, c := range cs {
		m[c.Name] = c.Value
	}
	return m
}

// registryFor is the login for the registry image is pulled from, among the ones the task
// was assigned with, or nil.
func registryFor(image string, creds []docker.RegistryCredential) *sandbox.RegistryLogin {
	if image == "" {
		return nil
	}
	host, err := spec.RegistryHost(image)
	if err != nil {
		return nil
	}
	for _, c := range creds {
		if c.Host == host {
			return &sandbox.RegistryLogin{Username: c.Username, Password: string(c.Password)}
		}
	}
	return nil
}
