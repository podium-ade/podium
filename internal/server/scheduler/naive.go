package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/alvaroibarguen/podium/internal/ids"
	podiumv1 "github.com/alvaroibarguen/podium/internal/proto/podium/v1"
	"github.com/alvaroibarguen/podium/internal/server/nodes"
	"github.com/alvaroibarguen/podium/internal/server/store"
)

// Naive's fixed budget. The lease is what step 12 will expire; the deadline is the 15s a node
// has to answer an Assign with a provisioning event.
const (
	tickInterval          = time.Second
	claimLimit            = 10
	leaseTTL              = 2 * time.Minute
	provisioningDeadline  = 15 * time.Second
	assignFailureTemplate = "assigning to node %s failed: %v"
)

// Naive claims queued tasks once a second and gives each to whichever connected node has the
// most free slots and every label the task asked for. A task with no eligible node stays queued.
type Naive struct {
	store  *store.Store
	nodes  Dispatcher
	logger *slog.Logger
}

// NewNaive returns the MVP-0 scheduler.
func NewNaive(st *store.Store, disp Dispatcher, logger *slog.Logger) *Naive {
	if logger == nil {
		logger = slog.Default()
	}
	return &Naive{store: st, nodes: disp, logger: logger}
}

// Run ticks until ctx is cancelled.
func (n *Naive) Run(ctx context.Context) error {
	ticker := time.NewTicker(tickInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			n.tick(ctx)
		}
	}
}

func (n *Naive) tick(ctx context.Context) {
	candidates := n.nodes.Candidates()
	if len(candidates) == 0 {
		return
	}
	tasks, err := n.store.ClaimQueuedTasks(ctx, claimLimit)
	if err != nil {
		if ctx.Err() == nil {
			n.logger.ErrorContext(ctx, "claiming queued tasks failed", "error", err)
		}
		return
	}
	for _, task := range tasks {
		i := pick(candidates, task.Spec.Labels)
		if i < 0 {
			continue
		}
		if n.dispatch(ctx, task, candidates[i].NodeID) {
			candidates[i].FreeSlots--
		}
	}
}

// dispatch takes one task queued -> scheduled and pushes it at the node. It reports whether the
// node actually took a slot.
func (n *Naive) dispatch(ctx context.Context, task store.Task, nodeID string) bool {
	leaseID := ids.NewLease()
	now := time.Now().UTC()
	if err := n.store.AssignTask(ctx, task.ID, nodeID, leaseID, now.Add(leaseTTL)); err != nil {
		// Another claimer won the race, which is exactly what AssignTask is for.
		if !errors.Is(err, store.ErrInvalidTransition) {
			n.logger.ErrorContext(ctx, "assigning task failed", "task_id", task.ID, "error", err)
		}
		return false
	}
	assign := &podiumv1.Assign{
		TaskId:   task.ID,
		LeaseId:  leaseID,
		Spec:     task.Spec.ToProto(),
		Deadline: timestamppb.New(now.Add(provisioningDeadline)),
	}
	if err := n.nodes.Assign(ctx, nodeID, assign); err != nil {
		n.logger.WarnContext(ctx, "pushing assignment failed", "task_id", task.ID, "node_id", nodeID, "error", err)
		n.unwind(ctx, task.ID, fmt.Sprintf(assignFailureTemplate, nodeID, err))
		return false
	}
	return true
}

// unwind puts a task the node never received back on the queue, or fails it when its attempt
// budget is spent — a task that is scheduled to a node that cannot hear about it is stuck
// otherwise, and step 12's reconciliation does not exist yet.
func (n *Naive) unwind(ctx context.Context, taskID, reason string) {
	if _, err := n.store.TransitionTask(ctx, taskID, []store.Status{store.StatusScheduled}, store.StatusQueued, store.Patch{}); err == nil {
		return
	}
	if _, err := n.store.TransitionTask(ctx, taskID, []store.Status{store.StatusScheduled}, store.StatusFailed,
		store.Patch{FailureReason: &reason, FinishedAt: ptrNow()}); err != nil {
		n.logger.ErrorContext(ctx, "unwinding a failed assignment failed", "task_id", taskID, "error", err)
	}
}

func ptrNow() *time.Time {
	t := time.Now().UTC()
	return &t
}

// pick returns the index of the node with the most free slots that carries every label the task
// requires, or -1 when there is none. Ties break on node ID so the choice is deterministic.
func pick(candidates []nodes.Snapshot, required []string) int {
	best := -1
	for i, c := range candidates {
		if c.FreeSlots <= 0 || !hasAll(c.Labels, required) {
			continue
		}
		if best < 0 || c.FreeSlots > candidates[best].FreeSlots ||
			(c.FreeSlots == candidates[best].FreeSlots && c.NodeID < candidates[best].NodeID) {
			best = i
		}
	}
	return best
}

func hasAll(have, required []string) bool {
	for _, r := range required {
		if !slices.Contains(have, r) {
			return false
		}
	}
	return true
}
