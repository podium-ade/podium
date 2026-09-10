package nodes

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	podiumv1 "github.com/podium-ade/podium/internal/proto/podium/v1"
	"github.com/podium-ade/podium/internal/server/store"
	"github.com/podium-ade/podium/pkg/spec"
)

// reconcileTimeout bounds the store work one Hello may cost. A node whose reconciliation
// cannot finish is better off reconnecting than blocking its own stream.
const reconcileTimeout = 30 * time.Second

// CancelReasonNotOurs is what a node is told when it reports a container the control plane
// no longer holds. It is deliberately explicit: the operator reading the node's log should
// not have to guess why a container they can see was torn down.
const CancelReasonNotOurs = "reconciliation: the control plane no longer holds this task on this node"

// reconcile answers a Hello. It is the whole of the control plane's crash recovery, and it
// runs before the node is told anything else, because everything else depends on the two
// sides agreeing about which containers exist.
//
// Three things come out of it:
//
//  1. A checkpoint per task the node reported, saying whether the control plane still holds
//     it and — when it does — exactly how much of the task's output the store already has.
//     That last part is why this exists at all: the server commits an event batch and
//     *then* acks it, so a node killed in between has no record of an ack that did happen.
//     A node resuming from its own bookmark re-reads those bytes and re-emits them under
//     fresh sequence numbers, which the (task_id, seq) key cannot deduplicate, and the
//     operator sees a duplicated line. The store is the only authority on what it holds.
//  2. A Cancel for every reported task the control plane has moved on from, so the node
//     tears the container down instead of leaving an orphan running forever.
//  3. A verdict on every task the store still has on this node that the node did *not*
//     report: its container is gone, so the task is requeued or lost by the same policy a
//     dead node's tasks get.
//
// Tasks in `scheduled` are deliberately left alone here. An Assign pushed moments before
// the reconnect may still be in flight, and requeueing on that race would double-run a
// task; the scheduler's provisioning deadline resolves them a few seconds later instead.
func (s *Service) reconcile(ctx context.Context, sess *Session, node store.Node, reported []string) ([]*podiumv1.TaskCheckpoint, []string) {
	ctx, cancel := context.WithTimeout(ctx, reconcileTimeout)
	defer cancel()

	held, err := s.store.ListTasksOnNode(ctx, node.ID, store.ActiveStatuses)
	if err != nil {
		s.logger.ErrorContext(ctx, "reconciliation could not read the node's tasks",
			"node_id", node.ID, "error", err)
		// Without the store's view nothing can be decided safely. Telling the node to
		// adopt everything it reported is the conservative answer: it keeps running what
		// it is running, and the next Hello (or the watchdog) sorts it out.
		return optimisticCheckpoints(ctx, s.store, reported, s.logger), nil
	}

	byID := make(map[string]store.Task, len(held))
	for _, t := range held {
		byID[t.ID] = t
	}
	reportedSet := make(map[string]struct{}, len(reported))

	checkpoints := make([]*podiumv1.TaskCheckpoint, 0, len(reported))
	costs := make(map[string]TaskCost, len(reported))
	var orphans []string
	for _, taskID := range reported {
		reportedSet[taskID] = struct{}{}
		task, ours := byID[taskID]
		if !ours {
			checkpoints = append(checkpoints, &podiumv1.TaskCheckpoint{TaskId: taskID})
			sess.forget(taskID)
			orphans = append(orphans, taskID)
			s.logger.InfoContext(ctx, "node reported a task the control plane does not hold; asking it to tear the container down",
				"node_id", node.ID, "task_id", taskID)
			continue
		}
		costs[taskID] = CostOf(task.Spec)
		checkpoints = append(checkpoints, s.checkpoint(ctx, taskID))
	}
	sess.account(costs)

	for _, task := range held {
		if _, ok := reportedSet[task.ID]; ok {
			continue
		}
		if task.Status == store.StatusScheduled {
			continue
		}
		s.logger.WarnContext(ctx, "the node no longer has a container for a task the control plane put there",
			"node_id", node.ID, "task_id", task.ID, "status", task.Status)
		s.LoseTask(ctx, task, node, fmt.Sprintf(
			"node %s reconnected without task %s: its container is gone", node.Name, task.ID))
	}
	return checkpoints, orphans
}

// checkpoint is the control plane's honest answer for one task it still holds: how far the
// stored history goes and how much of the container's own output it has committed.
func (s *Service) checkpoint(ctx context.Context, taskID string) *podiumv1.TaskCheckpoint {
	cp := &podiumv1.TaskCheckpoint{TaskId: taskID, Adopt: true}
	high, err := s.store.MaxTaskSeq(ctx, taskID)
	if err != nil {
		s.logger.ErrorContext(ctx, "reading a task's sequence high-water mark failed",
			"task_id", taskID, "error", err)
		return cp
	}
	offsets, err := s.store.TaskStreamOffsets(ctx, taskID)
	if err != nil {
		s.logger.ErrorContext(ctx, "reading a task's committed stream offsets failed",
			"task_id", taskID, "error", err)
		return cp
	}
	cp.HighSeq = high
	cp.StdoutOffset = offsets.Stdout
	cp.StderrOffset = offsets.Stderr
	return cp
}

// optimisticCheckpoints is the degraded answer when the store cannot be read: keep
// everything, with whatever offsets can still be fetched.
func optimisticCheckpoints(ctx context.Context, st *store.Store, reported []string, logger *slog.Logger) []*podiumv1.TaskCheckpoint {
	out := make([]*podiumv1.TaskCheckpoint, 0, len(reported))
	for _, taskID := range reported {
		cp := &podiumv1.TaskCheckpoint{TaskId: taskID, Adopt: true}
		if high, err := st.MaxTaskSeq(ctx, taskID); err == nil {
			cp.HighSeq = high
		} else {
			logger.WarnContext(ctx, "degraded reconciliation: no high-water mark",
				"task_id", taskID, "error", err)
		}
		if offsets, err := st.TaskStreamOffsets(ctx, taskID); err == nil {
			cp.StdoutOffset = offsets.Stdout
			cp.StderrOffset = offsets.Stderr
		}
		out = append(out, cp)
	}
	return out
}

// LoseTask applies the node-loss policy to one task: a new attempt when the spec says the
// task may be re-run and the budget allows, otherwise `lost` with an explanation.
//
// `lost` is not `failed`, and the distinction is the point of it: the task did not do
// anything wrong, the machine it was on went away, and an operator deciding whether to
// re-run it needs to be told which of those happened. Every path through here first writes
// a synthetic event, so the task's own log says so too.
//
// It goes through TransitionTask with an explicit `from`, so a heartbeat expiring at the
// same moment a late `finished` event lands is a rejected transition — a no-op — rather
// than a task being dragged out of a terminal state.
func (s *Service) LoseTask(ctx context.Context, task store.Task, node store.Node, reason string) {
	if err := s.logs.Note(ctx, task.ID, reason); err != nil {
		s.logger.WarnContext(ctx, "recording a node-loss note failed", "task_id", task.ID, "error", err)
	}
	if task.Spec.RetryOnNodeLoss && task.Attempts < task.MaxAttempts && task.CancelRequestedAt == nil {
		if _, err := s.store.TransitionTask(ctx, task.ID, store.ActiveStatuses, store.StatusQueued, store.Patch{}); err == nil {
			s.logger.InfoContext(ctx, "task requeued after losing its node",
				"task_id", task.ID, "node_id", node.ID, "attempt", task.Attempts)
			return
		} else if !errors.Is(err, store.ErrInvalidTransition) {
			s.logger.ErrorContext(ctx, "requeueing a task after node loss failed",
				"task_id", task.ID, "error", err)
			return
		}
		// Out of attempts, or the task moved underneath us. Fall through to lost, which
		// re-checks the state under the row lock anyway.
	}
	now := time.Now().UTC()
	if _, err := s.store.TransitionTask(ctx, task.ID, store.ActiveStatuses, store.StatusLost,
		store.Patch{FailureReason: &reason, FinishedAt: &now}); err != nil {
		if !errors.Is(err, store.ErrInvalidTransition) {
			s.logger.ErrorContext(ctx, "marking a task lost failed", "task_id", task.ID, "error", err)
		}
		return
	}
	s.reg.Release(task.ID)
	s.logger.InfoContext(ctx, "task lost with its node",
		"task_id", task.ID, "node_id", node.ID, "reason", reason)
}

// CostOf is what one task takes out of a node's budget. A task's sidecars run on the same
// machine it does, so their limits count against the same capacity — a task asking for one
// core with a Postgres asking for two costs three.
func CostOf(ts spec.TaskSpec) TaskCost {
	cost := TaskCost{CPU: ts.Resources.CPU, MemoryMB: int64(ts.Resources.MemoryMB)}
	for _, sc := range ts.Sidecars {
		cost.CPU += sc.Resources.CPU
		cost.MemoryMB += int64(sc.Resources.MemoryMB)
	}
	return cost
}
