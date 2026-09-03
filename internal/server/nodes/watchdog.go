package nodes

import (
	"context"
	"fmt"
	"time"

	"github.com/alvaroibarguen/podium/internal/server/store"
)

// Watchdog is how node health is decided. The numbers come from the scheduler's Timing so
// there is one policy in the tree; they are passed in rather than imported because the
// scheduler already depends on this package and the cycle would be worse than the setter.
type Watchdog struct {
	// Interval is how often node health is swept.
	Interval time.Duration
	// UnreachableAfter is how long without a heartbeat before a node is unreachable. Its
	// tasks keep running: a node that cannot talk has not necessarily stopped working, and
	// the design's whole point is that a brief network loss costs nothing.
	UnreachableAfter time.Duration
	// OfflineAfter is how long without a heartbeat before a node is offline and its leases
	// expire.
	OfflineAfter time.Duration
}

// SetWatchdog configures the health sweep. It is a setter because the timings belong to the
// scheduler's Timing struct, which imports this package.
func (s *Service) SetWatchdog(w Watchdog) { s.watchdog = w }

// RunWatchdog sweeps node health until ctx is cancelled: a node that has stopped
// heartbeating becomes unreachable, then offline, and an offline node's tasks are requeued
// or lost.
//
// It is deliberately driven by the stored heartbeat rather than by the session registry.
// A session ending already marks the node unreachable, but a node whose TCP connection is
// wedged open holds a session it is not using, and only the heartbeat clock notices that.
func (s *Service) RunWatchdog(ctx context.Context) error {
	interval := s.watchdog.Interval
	if interval <= 0 {
		interval = 5 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			s.sweepNodes(ctx)
		}
	}
}

// sweepNodes moves every node to the status its heartbeat clock implies.
func (s *Service) sweepNodes(ctx context.Context) {
	rows, err := s.store.ListNodes(ctx)
	if err != nil {
		if ctx.Err() == nil {
			s.logger.ErrorContext(ctx, "node health sweep could not list nodes", "error", err)
		}
		return
	}
	now := time.Now().UTC()
	for _, node := range rows {
		want := s.healthOf(node, now)
		if want == node.Status {
			continue
		}
		if err := s.store.SetNodeStatus(ctx, node.ID, want); err != nil {
			s.logger.WarnContext(ctx, "updating node status failed", "node_id", node.ID, "error", err)
			continue
		}
		s.logger.InfoContext(ctx, "node status changed",
			"node_id", node.ID, "name", node.Name, "from", node.Status, "to", want,
			"last_heartbeat_at", node.LastHeartbeatAt)
		if want == store.NodeOffline {
			s.expireLeases(ctx, node)
		}
	}
}

// healthOf is the whole status rule, in one pure-ish place.
//
// A node holding a live session is online (or draining), full stop: the stream is a
// stronger signal than the heartbeat clock, and a node that is talking to us right now is
// not unreachable. Everything else is decided by how long it has been quiet.
func (s *Service) healthOf(node store.Node, now time.Time) store.NodeStatus {
	if _, live := s.reg.Get(node.ID); live {
		if node.Draining {
			return store.NodeDraining
		}
		return store.NodeOnline
	}
	since := node.CreatedAt
	if node.LastHeartbeatAt != nil {
		since = *node.LastHeartbeatAt
	}
	quiet := now.Sub(since)
	switch {
	case quiet >= s.watchdog.OfflineAfter:
		return store.NodeOffline
	case quiet >= s.watchdog.UnreachableAfter:
		return store.NodeUnreachable
	default:
		// Not quiet long enough to demote, and not connected either: this is the gap
		// between the stream ending and the clock running out. endSession already wrote
		// unreachable; leave whatever is there.
		return node.Status
	}
}

// expireLeases resolves every task an offline node was holding. Their containers may well
// still be running — the node cannot tell us — but the control plane has stopped waiting,
// and the containers are torn down when (if) the node reconnects and reconciles.
func (s *Service) expireLeases(ctx context.Context, node store.Node) {
	tasks, err := s.store.ListTasksOnNode(ctx, node.ID, store.ActiveStatuses)
	if err != nil {
		s.logger.ErrorContext(ctx, "listing an offline node's tasks failed", "node_id", node.ID, "error", err)
		return
	}
	for _, task := range tasks {
		s.LoseTask(ctx, task, node, fmt.Sprintf("node %s went offline", node.Name))
	}
}
