package scheduler

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/alvaroibarguen/podium/internal/server/store"
)

// leaseSlack is how far a stored lease expiry may fall short of the computed one before it
// is worth rewriting.
const leaseSlack = time.Second

// sweep applies every deadline the control plane owes a live task: the fifteen seconds a
// node has to accept an assignment, the spec's own timeout, an operator's cancel, and the
// lease that says a node is still working on it.
//
// It reads the store rather than the session registry, and it hands every decision to
// TransitionTask with an explicit `from`. A task that finished a millisecond ago simply
// rejects the transition, which is the correct outcome and not an error.
func (s *Service) sweep(ctx context.Context) {
	tasks, err := s.store.ClaimActiveTasks(ctx)
	if err != nil {
		if ctx.Err() == nil {
			s.logger.ErrorContext(ctx, "lease sweep could not list active tasks", "error", err)
		}
		return
	}
	if len(tasks) == 0 {
		return
	}

	live := make(map[string]struct{})
	for _, c := range s.nodes.Candidates() {
		live[c.NodeID] = struct{}{}
	}
	now := time.Now().UTC()
	nodeCache := make(map[string]store.Node)

	for _, task := range tasks {
		_, connected := live[task.NodeID]
		switch {
		case task.CancelRequestedAt != nil:
			s.chaseCancel(ctx, task, connected, now)
		case task.Status == store.StatusScheduled:
			s.checkAcceptance(ctx, task, now)
		default:
			s.checkTimeout(ctx, task, now)
		}
		s.checkLease(ctx, task, connected, now, nodeCache)
	}
}

// chaseCancel is what happens after `podium task cancel` (or a timeout) while the task is
// still alive. The Cancel is re-sent every sweep, because the first one may have gone to a
// session that died a moment later and a node that never hears it never stops.
//
// Past the grace period, with the node gone, the control plane stops waiting and writes the
// terminal status itself. That is the only path here that does not require the node's
// agreement, and it is bounded by CancelGrace precisely so it cannot race a container that
// is simply taking its SIGTERM grace to die.
func (s *Service) chaseCancel(ctx context.Context, task store.Task, connected bool, now time.Time) {
	if connected {
		if err := s.nodes.Cancel(ctx, task.NodeID, task.ID, task.CancelReason); err != nil {
			s.logger.DebugContext(ctx, "re-sending cancel failed", "task_id", task.ID, "error", err)
		}
	}
	// A running container reports its own exit, so wait for it however long it takes. Two
	// cases never will: a node that is gone, and a task still only `scheduled`, whose
	// container was never created and so has nothing to report.
	if connected && task.Status != store.StatusScheduled {
		return
	}
	if now.Sub(*task.CancelRequestedAt) < s.timing.CancelGrace {
		return
	}
	terminal := task.CancelStatus
	if !terminal.Terminal() {
		terminal = store.StatusCancelled
	}
	reason := task.CancelReason
	if _, err := s.store.TransitionTask(ctx, task.ID, store.ActiveStatuses, terminal,
		store.Patch{FinishedAt: &now, FailureReason: &reason}); err != nil {
		if !errors.Is(err, store.ErrInvalidTransition) {
			s.logger.ErrorContext(ctx, "forcing a cancelled task terminal failed", "task_id", task.ID, "error", err)
		}
		return
	}
	s.nodes.Release(task.ID)
	s.logger.InfoContext(ctx, "cancelled task forced terminal: its node never came back",
		"task_id", task.ID, "node_id", task.NodeID, "status", terminal, "after", s.timing.CancelGrace)
}

// checkAcceptance enforces the provisioning deadline: a node that was handed a task and has
// not said `provisioning` within it has not taken the task, whatever its stream says.
//
// The Cancel is sent first and unconditionally. If the node did get the assignment and is
// merely slow, the Cancel is what stops it running a task the control plane has already
// given to somebody else; if it never got it, the Cancel is a no-op on the node.
func (s *Service) checkAcceptance(ctx context.Context, task store.Task, now time.Time) {
	if task.ScheduledAt == nil || now.Sub(*task.ScheduledAt) < s.timing.ProvisioningDeadline {
		return
	}
	if task.NodeID != "" {
		if err := s.nodes.Cancel(ctx, task.NodeID, task.ID, ReasonNotAccepted); err != nil {
			s.logger.DebugContext(ctx, "cancelling an unaccepted assignment failed",
				"task_id", task.ID, "node_id", task.NodeID, "error", err)
		}
	}
	s.logger.WarnContext(ctx, "node did not accept an assignment in time",
		"task_id", task.ID, "node_id", task.NodeID, "deadline", s.timing.ProvisioningDeadline)
	s.revoke(ctx, task.ID, ReasonNotAccepted)
}

// checkTimeout stops a task that has outrun its spec's timeout. The task is not transitioned
// here: the intent is recorded and the node is asked to stop, so the exit code, the usage
// and the moment it actually ended all still come from the container. TransitionTask turns
// the node's finished event into failed{reason: timeout} when it arrives.
func (s *Service) checkTimeout(ctx context.Context, task store.Task, now time.Time) {
	timeout := task.Spec.Timeout.Std()
	if timeout <= 0 || task.StartedAt == nil || now.Sub(*task.StartedAt) < timeout {
		return
	}
	if _, err := s.store.RequestCancel(ctx, task.ID, ReasonTimeout, store.StatusFailed); err != nil {
		if !errors.Is(err, store.ErrInvalidTransition) {
			s.logger.ErrorContext(ctx, "recording a timeout failed", "task_id", task.ID, "error", err)
		}
		return
	}
	s.logger.WarnContext(ctx, "task exceeded its timeout; cancelling",
		"task_id", task.ID, "node_id", task.NodeID, "timeout", timeout, "ran_for", now.Sub(*task.StartedAt))
	if task.NodeID != "" {
		if err := s.nodes.Cancel(ctx, task.NodeID, task.ID, ReasonTimeout); err != nil {
			s.logger.WarnContext(ctx, "cancelling a timed-out task could not reach its node",
				"task_id", task.ID, "node_id", task.NodeID, "error", err)
		}
	}
}

// checkLease keeps tasks.lease_expires_at honest and uses it as the last backstop.
//
// The heartbeat watchdog is the primary detector of a node that has gone away; the lease
// catches what it cannot — a task pointing at a node row that no longer exists, or one
// stranded by a control plane that was down while its node died. It only ever fires when
// the node is not connected, so a long-running task whose node is talking to us is never
// taken away from it.
func (s *Service) checkLease(ctx context.Context, task store.Task, connected bool, now time.Time, cache map[string]store.Node) {
	if task.LeaseID == "" || task.NodeID == "" {
		return
	}
	want := s.leaseDeadline(task, now)
	// The tolerance is not cosmetic: Postgres stores microseconds and Go carries
	// nanoseconds, so an exact comparison would find the stored value fractionally short of
	// what was just written and rewrite it on every sweep, for every running task, forever.
	if task.LeaseExpiresAt == nil || task.LeaseExpiresAt.Add(leaseSlack).Before(want) {
		if err := s.store.ExtendLease(ctx, task.ID, task.LeaseID, want); err != nil && !errors.Is(err, store.ErrNotFound) {
			s.logger.WarnContext(ctx, "extending a lease failed", "task_id", task.ID, "error", err)
		}
		return
	}
	if connected || task.LeaseExpiresAt.After(now) {
		return
	}
	node, err := s.nodeRow(ctx, task.NodeID, cache)
	if err != nil {
		s.logger.WarnContext(ctx, "expiring a lease could not read its node", "node_id", task.NodeID, "error", err)
		return
	}
	s.logger.WarnContext(ctx, "lease expired with the node disconnected",
		"task_id", task.ID, "node_id", task.NodeID, "expired_at", task.LeaseExpiresAt)
	s.nodes.LoseTask(ctx, task, node, fmt.Sprintf(
		"lease of task %s on node %s expired at %s and the node has not reconnected",
		task.ID, node.Name, task.LeaseExpiresAt.Format(time.RFC3339)))
}

// leaseDeadline is how long a task's node is allowed to hold it: the fifteen seconds to
// accept while it is only scheduled, and the spec's whole timeout plus a grace once it is
// really being worked on. The grace is what stops a lease expiring under a task that is
// still legitimately running.
func (s *Service) leaseDeadline(task store.Task, now time.Time) time.Time {
	if task.Status == store.StatusScheduled {
		base := now
		if task.ScheduledAt != nil {
			base = *task.ScheduledAt
		}
		return base.Add(s.timing.LeaseTTL)
	}
	base := now
	switch {
	case task.StartedAt != nil:
		base = *task.StartedAt
	case task.ScheduledAt != nil:
		base = *task.ScheduledAt
	}
	return base.Add(task.Spec.Timeout.Std() + s.timing.LeaseGrace)
}

func (s *Service) nodeRow(ctx context.Context, nodeID string, cache map[string]store.Node) (store.Node, error) {
	if n, ok := cache[nodeID]; ok {
		return n, nil
	}
	n, err := s.store.GetNode(ctx, nodeID)
	if err != nil {
		return store.Node{}, err
	}
	cache[nodeID] = n
	return n, nil
}
