package node

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/alvaroibarguen/podium/internal/node/docker"
	podiumv1 "github.com/alvaroibarguen/podium/internal/proto/podium/v1"
	"github.com/alvaroibarguen/podium/internal/proto/podium/v1/podiumv1connect"
	"github.com/alvaroibarguen/podium/internal/transport/local"
	"github.com/alvaroibarguen/podium/internal/transport/tailnet"
)

// adoptSeqFloor is where an adopted task restarts its sequence space when the control
// plane never answered and the bookmark the previous incarnation wrote is unreadable.
// Reusing a sequence number is worse than leaving a gap: the server's insert is "on
// conflict do nothing", so a colliding finished event would be silently dropped and the
// task would stay running forever.
const adoptSeqFloor = 1 << 20

// adoptTeardownTimeout bounds the cleanup of a container the control plane has disowned.
const adoptTeardownTimeout = 60 * time.Second

// Node is the daemon: one Docker executor, one identity, one stream at a time, and one
// replay buffer per task in flight.
type Node struct {
	cfg    Config
	logger *slog.Logger
	exec   *docker.Executor
	id     Identity
	facts  HostFacts
	client podiumv1connect.NodeServiceClient

	// tsnet is the node's own Tailscale device under transport: tailnet, nil otherwise.
	tsnet *tailnet.Client

	// runCtx outlives the daemon's own context on purpose: a SIGTERM must not cancel a
	// container run, because cancelling it makes the executor tear the container down.
	// Killing the process instead leaves the container alive for the next incarnation to
	// adopt.
	runCtx context.Context

	mu    sync.Mutex
	tasks map[string]*buffer
	// taskImages is every image reference a task on this node needs, so the image cache
	// prune can never pick one out from under a running container.
	taskImages map[string][]string
	// pending holds the containers this daemon found on the engine at startup and has
	// not decided about yet. They are registered in tasks (so Hello reports them and
	// they hold slots) but nothing is read from them until the control plane has said
	// what it already has — see applyCheckpoints.
	pending  map[string]*pendingAdoption
	draining bool
	// maxTasks is the concurrency budget in force: cfg.MaxTasks until the control plane
	// says otherwise. The override is not persisted here — the server re-sends it after
	// every HelloAck — so a node that restarts alone comes back on its own configuration.
	maxTasks int

	wake      chan struct{}
	drainOnce sync.Once
	drainDone chan struct{}
	connected atomic.Bool
	// diskAboveWatermark latches the last disk sample against the image cache high
	// watermark. While it is set the node advertises no free slots: a machine that cannot
	// fit another image cannot reliably start another task, and refusing the work is more
	// honest than accepting it and failing at the pull.
	diskAboveWatermark atomic.Bool

	metrics *metricsServer
}

// New validates the configuration, connects to Docker, resolves the node's identity —
// enrolling on the first run — and prepares the stream client. It does not connect the
// stream; Run does that.
func New(ctx context.Context, cfg Config, logger *slog.Logger) (*Node, error) {
	if logger == nil {
		logger = slog.Default()
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("node config: %w", err)
	}

	exec, err := docker.New(ctx, docker.Options{
		DataDir:                 cfg.DataDir,
		DockerHost:              cfg.DockerHost,
		AllowPrivilegedSidecars: cfg.AllowPrivilegedSidecars,
		Logger:                  logger,
	})
	if err != nil {
		return nil, err
	}

	facts := hostFacts(ctx, exec.ServerVersion())
	httpClient, ts, err := dialer(ctx, cfg, logger)
	if err != nil {
		_ = exec.Close()
		return nil, err
	}
	client := podiumv1connect.NewNodeServiceClient(httpClient, cfg.Server)

	closeAll := func() {
		_ = exec.Close()
		if ts != nil {
			_ = ts.Close()
		}
	}

	id, ok, err := LoadIdentity(cfg.DataDir)
	if err != nil {
		closeAll()
		return nil, err
	}
	if !ok {
		id, err = enroll(ctx, client, cfg, facts, logger)
		if err != nil {
			closeAll()
			return nil, err
		}
	} else if cfg.EnrollToken != "" {
		// The stored identity wins, and saying nothing about it is expensive: an
		// identity.json left behind by a DIFFERENT control plane makes the node reconnect
		// forever with "unauthenticated: stream: unknown node key" and never print
		// `node enrolled`, while the operator watches the enrollment token they supplied
		// have no effect at all.
		logger.InfoContext(ctx, "ignoring the supplied enrollment token: this data dir already "+
			"holds an identity. Delete the file and start again to enrol afresh.",
			"node_id", id.NodeID, "identity", filepath.Join(cfg.DataDir, identityFile))
	}

	return &Node{
		cfg:        cfg,
		logger:     logger.With("node_id", id.NodeID),
		exec:       exec,
		id:         id,
		facts:      facts,
		client:     client,
		tsnet:      ts,
		runCtx:     context.Background(),
		tasks:      make(map[string]*buffer),
		taskImages: make(map[string][]string),
		pending:    make(map[string]*pendingAdoption),
		maxTasks:   cfg.MaxTasks,
		wake:       make(chan struct{}, 1),
		drainDone:  make(chan struct{}),
	}, nil
}

// dialer builds the HTTP client every RPC goes through, and the tailnet device behind it when
// there is one. A node never listens: under transport: tailnet it joins the tailnet purely to
// dial out, which is what lets a worker sit behind NAT with no open ports.
func dialer(ctx context.Context, cfg Config, logger *slog.Logger) (*http.Client, *tailnet.Client, error) {
	switch cfg.Transport {
	case TransportTailnet:
		ts, err := tailnet.NewClient(tailnet.ClientOptions{
			Hostname: cfg.TSHostname,
			StateDir: cfg.TSStateDir(),
			AuthKey:  cfg.TSAuthKey,
			Logger:   logger,
		})
		if err != nil {
			return nil, nil, err
		}
		if err := ts.Up(ctx); err != nil {
			_ = ts.Close()
			return nil, nil, err
		}
		return ts.HTTPClient(), ts, nil
	case TransportHost:
		return tailnet.NewHostClient(), nil, nil
	default:
		return local.NewStreamClient(cfg.LocalToken), nil, nil
	}
}

// NodeID is the enrolled identity's ID.
func (n *Node) NodeID() string { return n.id.NodeID }

// MetricsAddr is the bound address of the health and metrics server, valid once Run has
// started it.
func (n *Node) MetricsAddr() string {
	if n.metrics == nil {
		return n.cfg.MetricsListen
	}
	return n.metrics.addr()
}

// Close releases the Docker client and leaves the tailnet. Running containers are deliberately
// left alone: the next incarnation adopts them.
func (n *Node) Close() error {
	err := n.exec.Close()
	if n.tsnet != nil {
		if tsErr := n.tsnet.Close(); tsErr != nil && err == nil {
			err = tsErr
		}
	}
	return err
}

// signal nudges the stream loop to flush whatever the task goroutines have buffered.
func (n *Node) signal() {
	select {
	case n.wake <- struct{}{}:
	default:
	}
}

// pendingAdoption is a container found on the engine at startup, waiting for the control
// plane to say what it already holds for it.
type pendingAdoption struct {
	buf *buffer
	// container and state are what the engine reported, for the log line.
	container string
	state     string
	// bookmarked is whether the previous incarnation's on-disk state file could be read.
	// Without it, and without a checkpoint from the server, the sequence space has to
	// restart above adoptSeqFloor: a gap is recoverable, a collision is not.
	bookmarked bool
}

// discoverOwned finds every container this engine still has labelled podium.task and
// registers it, without reading a byte of it.
//
// On a normal start there are none. After a crash or a SIGTERM they are tasks whose
// containers outlived the daemon. Nothing is adopted here on purpose: an adoption has to
// know how much of the container's output the control plane already has, and this daemon
// cannot know that — the server commits a batch and then acks it, so a daemon killed in
// between has no record of an ack that did happen. The answer comes back in the HelloAck,
// and applyCheckpoints starts the adoptions then.
func (n *Node) discoverOwned(ctx context.Context) {
	owned, err := n.exec.ListOwned(ctx)
	if err != nil {
		n.logger.WarnContext(ctx, "listing podium containers failed; nothing adopted", "error", err)
		return
	}
	for _, c := range owned {
		if c.TaskID == "" || c.Role != docker.RoleTask {
			continue
		}
		dir := n.exec.TaskDir(c.TaskID)
		leaseID := c.LeaseID
		var base uint64
		var ackedStdout, ackedStderr int64
		bookmarked := false
		if st, err := loadTaskState(dir); err == nil {
			bookmarked = true
			base = st.HighSeq
			if st.LeaseID != "" {
				leaseID = st.LeaseID
			}
			ackedStdout, ackedStderr = st.AckedStdout, st.AckedStderr
		} else {
			n.logger.WarnContext(ctx, "no sequence bookmark for a container left behind",
				"task_id", c.TaskID, "error", err)
		}

		buf := newBuffer(c.TaskID, leaseID, dir, base)
		buf.ackedStdout, buf.ackedStderr = ackedStdout, ackedStderr
		if !n.addTask(c.TaskID, buf, c.Image) {
			continue
		}
		n.mu.Lock()
		n.pending[c.TaskID] = &pendingAdoption{
			buf: buf, container: c.Name, state: c.State, bookmarked: bookmarked,
		}
		n.mu.Unlock()
		n.logger.InfoContext(ctx, "found a container left behind by a previous run; waiting for reconciliation",
			"task_id", c.TaskID, "container", c.Name, "state", c.State)
	}
}

// applyCheckpoints is the node half of reconciliation. Every container this daemon found at
// startup is either adopted from the offsets the control plane actually holds, or torn down
// because the control plane has moved on from it.
func (n *Node) applyCheckpoints(ctx context.Context, checkpoints []*podiumv1.TaskCheckpoint) {
	for _, cp := range checkpoints {
		p := n.takePending(cp.GetTaskId())
		if p == nil {
			// A task this daemon is already running. Nothing to resume; if the control
			// plane has disowned it the Cancel that follows stops it.
			continue
		}
		if !cp.GetAdopt() {
			go n.dropAdoption(ctx, cp.GetTaskId(), p, "the control plane no longer holds it")
			continue
		}
		p.buf.resume(cp.GetHighSeq(), cp.GetStdoutOffset(), cp.GetStderrOffset())
		n.logger.InfoContext(ctx, "adopting container left behind by a previous run",
			"task_id", cp.GetTaskId(), "container", p.container, "state", p.state,
			"from_seq", cp.GetHighSeq(),
			"stdout_offset", cp.GetStdoutOffset(), "stderr_offset", cp.GetStderrOffset())
		go n.adoptTask(p.buf)
	}
}

// adoptStranded is the fallback for containers the control plane said nothing about, which
// should never happen against a server that speaks HelloAck. They are adopted from the
// on-disk bookmark, which is what this daemon used to do for everything — and which is
// exactly why a duplicated log line was possible.
func (n *Node) adoptStranded(ctx context.Context) {
	n.mu.Lock()
	stranded := n.pending
	n.pending = make(map[string]*pendingAdoption)
	n.mu.Unlock()

	for taskID, p := range stranded {
		if !p.bookmarked {
			p.buf.resume(adoptSeqFloor, 0, 0)
		}
		stdout, stderr := p.buf.ackedBytes()
		n.logger.WarnContext(ctx, "the control plane sent no checkpoint for a container; adopting from the local bookmark",
			"task_id", taskID, "from_seq", p.buf.high(),
			"stdout_offset", stdout, "stderr_offset", stderr)
		go n.adoptTask(p.buf)
	}
}

// takePending removes and returns a task's pending adoption, or nil when it has none.
func (n *Node) takePending(taskID string) *pendingAdoption {
	n.mu.Lock()
	defer n.mu.Unlock()
	p := n.pending[taskID]
	delete(n.pending, taskID)
	return p
}

func (n *Node) hasPending() bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	return len(n.pending) > 0
}

// dropAdoption tears down a container this node found but the control plane does not want.
// It is the orphan path: without it a container whose task was requeued elsewhere keeps
// running forever, holding its network, its volume and whatever it is talking to.
//
// It always runs on its own goroutine. A teardown stops a container, waits out its grace
// period, removes it, its network and its volume — twenty seconds is unremarkable — and the
// caller is the one goroutine that sends heartbeats, acks and events for every other task
// on this node.
func (n *Node) dropAdoption(ctx context.Context, taskID string, p *pendingAdoption, why string) {
	n.logger.InfoContext(ctx, "tearing down an orphaned container",
		"task_id", taskID, "container", p.container, "state", p.state, "reason", why)
	tctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), adoptTeardownTimeout)
	defer cancel()
	if err := n.exec.Teardown(tctx, taskID, false); err != nil {
		n.logger.Error("tearing down an orphaned container failed", "task_id", taskID, "error", err)
	}
	n.removeTask(taskID)
	n.signal()
}

// dropPendingContainer is dropAdoption addressed by task id, for a Cancel that arrives for
// a container this node has not started reading yet. It reports whether there was one, and
// does the tearing down in the background.
func (n *Node) dropPendingContainer(ctx context.Context, taskID, reason string) bool {
	p := n.takePending(taskID)
	if p == nil {
		return false
	}
	go n.dropAdoption(ctx, taskID, p, reason)
	return true
}

// addTask registers a task's buffer and the images it needs. It reports false when the
// task is already tracked, which is how a duplicate Assign after a reconnect is ignored.
func (n *Node) addTask(taskID string, buf *buffer, images ...string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	if _, ok := n.tasks[taskID]; ok {
		return false
	}
	n.tasks[taskID] = buf
	n.taskImages[taskID] = images
	return true
}

func (n *Node) removeTask(taskID string) {
	n.mu.Lock()
	delete(n.tasks, taskID)
	delete(n.taskImages, taskID)
	delete(n.pending, taskID)
	empty := len(n.tasks) == 0
	draining := n.draining
	n.mu.Unlock()
	if empty && draining && n.cfg.ExitOnDrain {
		n.drainOnce.Do(func() { close(n.drainDone) })
	}
}

// buffers is a snapshot of the live buffers, taken so the stream loop can send without
// holding the lock.
func (n *Node) buffers() []*buffer {
	n.mu.Lock()
	defer n.mu.Unlock()
	out := make([]*buffer, 0, len(n.tasks))
	for _, b := range n.tasks {
		out = append(out, b)
	}
	return out
}

func (n *Node) runningTaskIDs() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	out := make([]string, 0, len(n.tasks))
	for id := range n.tasks {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// freeSlots is what the scheduler budgets against. It never goes negative, even if the
// server over-assigns, and it is zero while the node is draining or its disk is past the
// image cache watermark — a machine that cannot fit another image cannot reliably start
// another task, and refusing the work is more honest than failing at the pull.
func (n *Node) freeSlots() int32 {
	if n.diskFull() {
		return 0
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	free := n.maxTasks - len(n.tasks)
	if free < 0 || n.draining {
		return 0
	}
	return int32(free)
}

// setSlots applies the control plane's slot count, or restores this node's own max_tasks when
// it is 0. Lowering it below what is already running takes nothing down: the running tasks
// finish, and freeSlots stays at 0 until enough of them have.
func (n *Node) setSlots(maxTasks int32) (inForce int, changed bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	was := n.maxTasks
	if maxTasks > 0 {
		n.maxTasks = int(maxTasks)
	} else {
		n.maxTasks = n.cfg.MaxTasks
	}
	return n.maxTasks, n.maxTasks != was
}

func (n *Node) runningCount() int32 {
	n.mu.Lock()
	defer n.mu.Unlock()
	return int32(len(n.tasks))
}

// setDraining records the control plane's instruction. A drained node stops accepting
// assignments and advertises no free slots; only one started with --exit-on-drain also
// exits when the last task finishes, because an operator draining a machine for
// maintenance wants it to stay connected and undrainable, not to vanish.
func (n *Node) setDraining(v bool) {
	n.mu.Lock()
	n.draining = v
	empty := len(n.tasks) == 0
	n.mu.Unlock()
	if v && empty && n.cfg.ExitOnDrain {
		n.drainOnce.Do(func() { close(n.drainDone) })
	}
}

func (n *Node) isDraining() bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.draining
}

// errDrained ends Run cleanly once a drained node has nothing left to run.
var errDrained = errors.New("node drained")
