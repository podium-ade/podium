package node

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"sync/atomic"

	"github.com/alvaroibarguen/podium/internal/node/docker"
	"github.com/alvaroibarguen/podium/internal/proto/podium/v1/podiumv1connect"
	"github.com/alvaroibarguen/podium/internal/transport/dev"
)

// adoptSeqFloor is where an adopted task restarts its sequence space when the bookmark
// the previous incarnation wrote is unreadable. Reusing a sequence number is worse than
// leaving a gap: the server's insert is "on conflict do nothing", so a colliding
// finished event would be silently dropped and the task would stay running forever.
const adoptSeqFloor = 1 << 20

// Node is the daemon: one Docker executor, one identity, one stream at a time, and one
// replay buffer per task in flight.
type Node struct {
	cfg    Config
	logger *slog.Logger
	exec   *docker.Executor
	id     Identity
	facts  HostFacts
	client podiumv1connect.NodeServiceClient

	// runCtx outlives the daemon's own context on purpose: a SIGTERM must not cancel a
	// container run, because cancelling it makes the executor tear the container down.
	// Killing the process instead leaves the container alive for the next incarnation to
	// adopt.
	runCtx context.Context

	mu       sync.Mutex
	tasks    map[string]*buffer
	draining bool

	wake      chan struct{}
	drainOnce sync.Once
	drainDone chan struct{}
	connected atomic.Bool

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
		DataDir:    cfg.DataDir,
		DockerHost: cfg.DockerHost,
		Logger:     logger,
	})
	if err != nil {
		return nil, err
	}

	facts := hostFacts(ctx, exec.ServerVersion())
	client := podiumv1connect.NewNodeServiceClient(dev.NewStreamClient(cfg.DevToken), cfg.Server)

	id, ok, err := LoadIdentity(cfg.DataDir)
	if err != nil {
		_ = exec.Close()
		return nil, err
	}
	if !ok {
		id, err = enroll(ctx, client, cfg, facts, logger)
		if err != nil {
			_ = exec.Close()
			return nil, err
		}
	}

	return &Node{
		cfg:       cfg,
		logger:    logger.With("node_id", id.NodeID),
		exec:      exec,
		id:        id,
		facts:     facts,
		client:    client,
		runCtx:    context.Background(),
		tasks:     make(map[string]*buffer),
		wake:      make(chan struct{}, 1),
		drainDone: make(chan struct{}),
	}, nil
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

// Close releases the Docker client. Running containers are deliberately left alone.
func (n *Node) Close() error { return n.exec.Close() }

// signal nudges the stream loop to flush whatever the task goroutines have buffered.
func (n *Node) signal() {
	select {
	case n.wake <- struct{}{}:
	default:
	}
}

// adoptOwned takes over every container this engine still has labelled podium.task. On a
// normal start there are none; after a crash or a SIGTERM they are tasks whose containers
// outlived the daemon, and each one gets a goroutine that streams its tail and reports it
// finished, so the server is never left with a task stuck in running.
func (n *Node) adoptOwned(ctx context.Context) {
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
		base := uint64(adoptSeqFloor)
		leaseID := c.LeaseID
		var ackedStdout, ackedStderr int64
		if st, err := loadTaskState(dir); err == nil {
			if st.HighSeq > 0 {
				base = st.HighSeq
			}
			if st.LeaseID != "" {
				leaseID = st.LeaseID
			}
			ackedStdout, ackedStderr = st.AckedStdout, st.AckedStderr
		} else {
			n.logger.WarnContext(ctx, "no sequence bookmark for adopted task; restarting the sequence space above the floor",
				"task_id", c.TaskID, "floor", adoptSeqFloor, "error", err)
		}

		buf := newBuffer(c.TaskID, leaseID, dir, base)
		buf.ackedStdout, buf.ackedStderr = ackedStdout, ackedStderr
		if !n.addTask(c.TaskID, buf) {
			continue
		}
		n.logger.InfoContext(ctx, "adopting container left behind by a previous run",
			"task_id", c.TaskID, "container", c.Name, "state", c.State,
			"from_seq", base, "acked_stdout_bytes", ackedStdout, "acked_stderr_bytes", ackedStderr)
		go n.adoptTask(buf)
	}
}

// addTask registers a task's buffer. It reports false when the task is already tracked,
// which is how a duplicate Assign after a reconnect is ignored.
func (n *Node) addTask(taskID string, buf *buffer) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	if _, ok := n.tasks[taskID]; ok {
		return false
	}
	n.tasks[taskID] = buf
	return true
}

func (n *Node) removeTask(taskID string) {
	n.mu.Lock()
	delete(n.tasks, taskID)
	empty := len(n.tasks) == 0
	draining := n.draining
	n.mu.Unlock()
	if empty && draining {
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
// server over-assigns.
func (n *Node) freeSlots() int32 {
	n.mu.Lock()
	defer n.mu.Unlock()
	free := n.cfg.MaxTasks - len(n.tasks)
	if free < 0 || n.draining {
		return 0
	}
	return int32(free)
}

func (n *Node) runningCount() int32 {
	n.mu.Lock()
	defer n.mu.Unlock()
	return int32(len(n.tasks))
}

func (n *Node) setDraining() {
	n.mu.Lock()
	n.draining = true
	empty := len(n.tasks) == 0
	n.mu.Unlock()
	if empty {
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
