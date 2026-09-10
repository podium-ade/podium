// Package scheduler decides which node runs which task, and keeps the promises that
// decision makes: a lease per assignment, a deadline for accepting one, the spec's own
// timeout, and the node-health policy that turns a machine going away into either a new
// attempt or an honest `lost`.
package scheduler

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/podium-ade/podium/internal/ids"
	podiumv1 "github.com/podium-ade/podium/internal/proto/podium/v1"
	"github.com/podium-ade/podium/internal/server/nodes"
	"github.com/podium-ade/podium/internal/server/secrets"
	"github.com/podium-ade/podium/internal/server/store"
	"github.com/podium-ade/podium/pkg/spec"
)

// The reasons a task is told it is still queued, and the reasons it is failed. They are
// constants because the CLI, the UI and the tests all read them, and because an operator
// staring at "queued" deserves the same sentence every time.
const (
	ReasonNoNodes        = "no node is connected to the control plane"
	ReasonNoLabels       = "no online node carries every label this task requires"
	ReasonAllDraining    = "every node that could run this task is draining"
	ReasonNoRoom         = "no online node has enough free CPU or memory for this task"
	ReasonFull           = "every node that could run this task is full"
	ReasonNotAccepted    = "node did not accept assignment"
	ReasonTimeout        = "timeout"
	ReasonNoLease        = "the task is on no node and holds no lease"
	assignFailedTemplate = "assigning to node %s failed: %v"
)

// Dispatcher is the node registry, as much of it as a scheduler needs.
type Dispatcher interface {
	// Candidates is one snapshot per connected node.
	Candidates() []nodes.Snapshot
	// Assign pushes an assignment onto a node's stream and charges its cost.
	Assign(ctx context.Context, nodeID string, a *podiumv1.Assign, cost nodes.TaskCost) error
	// Cancel asks a node to stop a task. It does not wait.
	Cancel(ctx context.Context, nodeID, taskID, reason string) error
	// LoseTask applies the node-loss policy: requeue if the spec allows it, else lost.
	LoseTask(ctx context.Context, task store.Task, node store.Node, reason string)
	// Release gives back the slot a node was holding for a task that never reached a
	// terminal status, which is the only path the event side does not already cover.
	Release(taskID string)
}

// Resolver turns a task's secret references into the plaintext an Assign carries. It is
// called immediately before the assignment and never earlier: a value should exist in
// server memory for as short a time as possible.
type Resolver interface {
	Resolve(ctx context.Context, taskID string, refs []spec.SecretRef) ([]secrets.Resolved, error)
	// ResolveRegistries returns the login for each registry the images are pulled from
	// that the store holds one for, and nothing for the rest.
	ResolveRegistries(ctx context.Context, taskID string, images []string) ([]secrets.RegistryCredential, error)
}

// Service is the scheduler: it places queued work on nodes and keeps the promises that
// placement makes. Two loops, deliberately separate:
//
//   - the dispatch tick claims queued tasks and assigns them, woken either by its own
//     ticker or by the database saying a task has just become queued;
//   - the watchdog sweep looks at every task a node already owes an answer for and applies
//     the deadlines — the 15s to accept an assignment, the spec's timeout, the operator's
//     cancel, the lease.
//
// Neither ever transitions a task from what a session says in memory. Every state change
// goes through TransitionTask with an explicit `from`, so a heartbeat expiring at the same
// instant a late event arrives is a rejected transition — a no-op — and not corruption.
type Service struct {
	store   *store.Store
	nodes   Dispatcher
	secrets Resolver
	timing  Timing
	logger  *slog.Logger
}

// New returns the scheduler. It does not start anything; call Run.
func New(st *store.Store, disp Dispatcher, resolver Resolver, timing Timing, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{store: st, nodes: disp, secrets: resolver, timing: timing, logger: logger}
}

// Run ticks until ctx is cancelled.
func (s *Service) Run(ctx context.Context) error {
	queued, err := s.store.SubscribeQueuedTasks(ctx)
	if err != nil {
		// A scheduler that cannot listen still works; it just answers on its own tick
		// rather than within milliseconds of a submission.
		s.logger.WarnContext(ctx, "scheduler could not subscribe to the queue channel; falling back to polling",
			"interval", s.timing.Tick, "error", err)
		queued = nil
	}

	tick := time.NewTicker(s.timing.Tick)
	defer tick.Stop()
	watchdog := time.NewTicker(s.timing.Watchdog)
	defer watchdog.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case _, ok := <-queued:
			if !ok {
				queued = nil
				continue
			}
			s.dispatch(ctx)
		case <-tick.C:
			s.dispatch(ctx)
		case <-watchdog.C:
			s.sweep(ctx)
		}
	}
}

// dispatch places as much of the queue as it can on the nodes that are connected right now.
func (s *Service) dispatch(ctx context.Context) {
	tasks, err := s.store.ClaimQueuedTasks(ctx, s.timing.ClaimLimit)
	if err != nil {
		if ctx.Err() == nil {
			s.logger.ErrorContext(ctx, "claiming queued tasks failed", "error", err)
		}
		return
	}
	if len(tasks) == 0 {
		return
	}
	// One snapshot for the whole batch, decremented locally as tasks are placed. Reading
	// the registry per task would let a node with one free slot take the whole batch,
	// because its heartbeat is up to ten seconds away.
	candidates := s.nodes.Candidates()
	for _, task := range tasks {
		if task.CancelRequestedAt != nil {
			// A task can be requeued after somebody asked for it to stop — a revoked
			// assignment, say. Sending it to a node instead of finishing it would run
			// work that was already called off.
			s.finishCancelledInQueue(ctx, task)
			continue
		}
		i := pick(candidates, task.Spec, nodes.CostOf(task.Spec))
		if i < 0 {
			s.leaveQueued(ctx, task, placementReason(candidates, task.Spec, nodes.CostOf(task.Spec)))
			continue
		}
		if s.assign(ctx, task, candidates[i].NodeID) {
			charge(&candidates[i], nodes.CostOf(task.Spec))
		}
	}
}

// finishCancelledInQueue ends a queued task whose stop was requested while it was somewhere
// else. There is no container and no node, so there is nothing to wait for.
func (s *Service) finishCancelledInQueue(ctx context.Context, task store.Task) {
	terminal := task.CancelStatus
	if !terminal.Terminal() {
		terminal = store.StatusCancelled
	}
	now := time.Now().UTC()
	reason := task.CancelReason
	if _, err := s.store.TransitionTask(ctx, task.ID, []store.Status{store.StatusQueued}, terminal,
		store.Patch{FinishedAt: &now, FailureReason: &reason}); err != nil {
		if !errors.Is(err, store.ErrInvalidTransition) {
			s.logger.ErrorContext(ctx, "finishing a cancelled queued task failed", "task_id", task.ID, "error", err)
		}
		return
	}
	s.logger.InfoContext(ctx, "queued task ended without running: its stop was already requested",
		"task_id", task.ID, "status", terminal, "reason", reason)
}

// leaveQueued records why a task could not be placed. It is the difference between a task
// an operator can debug and one that just sits there.
func (s *Service) leaveQueued(ctx context.Context, task store.Task, reason string) {
	if task.QueuedReason == reason && task.LastScheduleAttemptAt != nil {
		// Nothing has changed; do not write a row every tick for a task nobody can run.
		return
	}
	if err := s.store.MarkScheduleAttempt(ctx, task.ID, reason); err != nil && ctx.Err() == nil {
		s.logger.WarnContext(ctx, "recording a schedule attempt failed", "task_id", task.ID, "error", err)
	}
}

// assign takes one task queued -> scheduled and pushes it at the node. It reports whether
// the node actually took a slot.
//
// Secrets are resolved here, one dispatch at a time, and never earlier: the value is in
// server memory only for the length of this call. Whether the *names* exist is settled at
// admission instead, so a task naming a secret that does not exist fails at `podium run`
// even when no node has ever connected.
func (s *Service) assign(ctx context.Context, task store.Task, nodeID string) bool {
	resolved, err := s.resolve(ctx, task)
	if err != nil {
		s.failUnresolvable(ctx, task, err)
		return false
	}
	defer func() {
		for _, r := range resolved {
			secrets.Zero(r.Value)
		}
	}()
	registries, err := s.resolveRegistries(ctx, task)
	if err != nil {
		s.failUnresolvable(ctx, task, err)
		return false
	}
	defer func() {
		for _, r := range registries {
			secrets.Zero(r.Password)
		}
	}()

	leaseID := ids.NewLease()
	now := time.Now().UTC()
	if err := s.store.AssignTask(ctx, task.ID, nodeID, leaseID, now.Add(s.timing.LeaseTTL)); err != nil {
		// Another claimer won the race, which is exactly what AssignTask is for.
		if !errors.Is(err, store.ErrInvalidTransition) {
			s.logger.ErrorContext(ctx, "assigning task failed", "task_id", task.ID, "error", err)
		}
		return false
	}
	assign := &podiumv1.Assign{
		TaskId:              task.ID,
		LeaseId:             leaseID,
		Spec:                task.Spec.ToProto(),
		Deadline:            timestamppb.New(now.Add(s.timing.ProvisioningDeadline)),
		ResolvedSecrets:     resolvedToProto(resolved),
		RegistryCredentials: registriesToProto(registries),
	}
	if err := s.nodes.Assign(ctx, nodeID, assign, nodes.CostOf(task.Spec)); err != nil {
		s.logger.WarnContext(ctx, "pushing assignment failed", "task_id", task.ID, "node_id", nodeID, "error", err)
		s.revoke(ctx, task.ID, fmt.Sprintf(assignFailedTemplate, nodeID, err))
		return false
	}
	return true
}

// resolve is Resolve with a nil-resolver guard, so a Service built without one simply runs
// tasks that need no secrets.
func (s *Service) resolve(ctx context.Context, task store.Task) ([]secrets.Resolved, error) {
	if len(task.Spec.Secrets) == 0 {
		return nil, nil
	}
	if s.secrets == nil {
		return nil, fmt.Errorf("%w: this server has no secret store", secrets.ErrNoKey)
	}
	return s.secrets.Resolve(ctx, task.ID, task.Spec.Secrets)
}

// resolveRegistries is ResolveRegistries with the same nil-resolver guard. A store error is
// fatal to the task for the same reason a secret's is: a task that runs without the login it
// was meant to have does not fail any better on the node.
func (s *Service) resolveRegistries(ctx context.Context, task store.Task) ([]secrets.RegistryCredential, error) {
	if s.secrets == nil {
		return nil, nil
	}
	return s.secrets.ResolveRegistries(ctx, task.ID, task.Spec.Images())
}

// failUnresolvable fails a task whose secrets could not be resolved. It happens while the
// task is still queued, so no node has seen it and no attempt has been spent.
func (s *Service) failUnresolvable(ctx context.Context, task store.Task, cause error) {
	reason := cause.Error()
	if errors.Is(cause, secrets.ErrMissing) {
		s.logger.WarnContext(ctx, "task references a secret that does not exist", "task_id", task.ID, "error", cause)
	} else {
		s.logger.ErrorContext(ctx, "resolving task secrets failed", "task_id", task.ID, "error", cause)
	}
	now := time.Now().UTC()
	if _, err := s.store.TransitionTask(ctx, task.ID, []store.Status{store.StatusQueued}, store.StatusFailed,
		store.Patch{FailureReason: &reason, FinishedAt: &now}); err != nil {
		s.logger.ErrorContext(ctx, "failing an unresolvable task failed", "task_id", task.ID, "error", err)
	}
}

// resolvedToProto copies the values onto the wire. This is the one place a plaintext
// secret leaves the server, and the Assign it lands in must only ever be logged through
// podiumv1.RedactForLog.
func resolvedToProto(in []secrets.Resolved) []*podiumv1.ResolvedSecret {
	if len(in) == 0 {
		return nil
	}
	out := make([]*podiumv1.ResolvedSecret, 0, len(in))
	for _, r := range in {
		out = append(out, &podiumv1.ResolvedSecret{
			Name:   r.Name,
			Target: r.Target,
			Key:    r.Key,
			Value:  bytes.Clone(r.Value),
		})
	}
	return out
}

// registriesToProto copies the registry logins onto the wire, under the same rule as
// resolvedToProto: the Assign is only ever logged through podiumv1.RedactForLog.
func registriesToProto(in []secrets.RegistryCredential) []*podiumv1.RegistryCredential {
	if len(in) == 0 {
		return nil
	}
	out := make([]*podiumv1.RegistryCredential, 0, len(in))
	for _, r := range in {
		out = append(out, &podiumv1.RegistryCredential{
			Host:     r.Host,
			Username: r.Username,
			Password: bytes.Clone(r.Password),
		})
	}
	return out
}

// revoke takes back an assignment the node never accepted. The task is put back exactly as
// it was claimed, so nothing about the attempt survives except the count.
func (s *Service) revoke(ctx context.Context, taskID, reason string) {
	s.handBack(ctx, taskID, []store.Status{store.StatusScheduled}, reason)
}

// Retry is what the log ingest calls when a node reports an error that ended the run but
// could succeed elsewhere — an image the registry was too busy to serve, an engine that
// stuttered. The node is finished with the task either way, so the only question is
// whether anybody else should try, and that is the attempt budget's to answer.
func (s *Service) Retry(ctx context.Context, taskID, reason string) {
	s.handBack(ctx, taskID, store.ActiveStatuses, reason)
}

// handBack is the whole attempt budget, in one place: a task whose attempt ended without a
// container goes back on the queue when max_attempts allows another, and fails carrying
// reason when it does not.
//
// It is total over the statuses it is given — every one of them has both a queued and a
// failed edge — which is the property the whole fix rests on. A task that reaches here
// leaves the state it was in; it is never left parked in one nobody is working on.
func (s *Service) handBack(ctx context.Context, taskID string, from []store.Status, reason string) {
	if _, err := s.store.TransitionTask(ctx, taskID, from, store.StatusQueued, store.Patch{}); err == nil {
		s.nodes.Release(taskID)
		s.logger.InfoContext(ctx, "task taken back from its node; another attempt queued",
			"task_id", taskID, "reason", reason)
		return
	}
	now := time.Now().UTC()
	if _, err := s.store.TransitionTask(ctx, taskID, from, store.StatusFailed,
		store.Patch{FailureReason: &reason, FinishedAt: &now}); err != nil {
		if !errors.Is(err, store.ErrInvalidTransition) {
			s.logger.ErrorContext(ctx, "failing a task whose attempt ended failed",
				"task_id", taskID, "error", err)
		}
		return
	}
	s.nodes.Release(taskID)
	s.logger.InfoContext(ctx, "task failed; no attempts left", "task_id", taskID, "reason", reason)
}

// pick returns the index of the best node for a task, or -1 when none fits.
//
// "Best" is the one with the most free slots, which spreads a burst across a fleet instead
// of packing it onto one machine; ties go to whichever was assigned to longest ago, so two
// identical nodes alternate rather than one always winning. Bin packing is deliberately not
// attempted: it needs a picture of the future that a task runner does not have.
func pick(candidates []nodes.Snapshot, ts spec.TaskSpec, cost nodes.TaskCost) int {
	best := -1
	for i := range candidates {
		if !eligible(candidates[i], ts, cost) {
			continue
		}
		if best < 0 || better(candidates[i], candidates[best]) {
			best = i
		}
	}
	return best
}

func better(a, b nodes.Snapshot) bool {
	if a.FreeSlots != b.FreeSlots {
		return a.FreeSlots > b.FreeSlots
	}
	if !a.LastAssignedAt.Equal(b.LastAssignedAt) {
		return a.LastAssignedAt.Before(b.LastAssignedAt)
	}
	return a.NodeID < b.NodeID
}

// eligible is the whole matching rule. A capacity a node never reported is zero, and zero
// means "unmeasured": constraining against it would refuse every task on a node whose
// gopsutil call failed, which is a worse failure than over-committing one.
func eligible(c nodes.Snapshot, ts spec.TaskSpec, cost nodes.TaskCost) bool {
	return !c.Draining &&
		c.FreeSlots > 0 &&
		hasAll(c.Labels, ts.Labels) &&
		fitsCPU(c, cost) &&
		fitsMemory(c, cost)
}

func fitsCPU(c nodes.Snapshot, cost nodes.TaskCost) bool {
	return cost.CPU <= 0 || c.Capacity.CPUCores <= 0 || c.FreeCPU >= cost.CPU
}

func fitsMemory(c nodes.Snapshot, cost nodes.TaskCost) bool {
	return cost.MemoryMB <= 0 || c.Capacity.MemoryMB <= 0 || c.FreeMemoryMB >= cost.MemoryMB
}

// charge decrements a candidate snapshot in place, so the rest of this batch sees the slot
// go. The session's own accounting is done by Dispatcher.Assign; this is the local copy.
func charge(c *nodes.Snapshot, cost nodes.TaskCost) {
	if c.FreeSlots > 0 {
		c.FreeSlots--
	}
	c.FreeCPU -= cost.CPU
	c.FreeMemoryMB -= cost.MemoryMB
	c.LastAssignedAt = time.Now().UTC()
}

// placementReason says, in one sentence an operator can act on, why nothing took the task.
// It narrows the candidate set one constraint at a time and reports the constraint that
// emptied it, because "unschedulable" on its own is unactionable.
func placementReason(candidates []nodes.Snapshot, ts spec.TaskSpec, cost nodes.TaskCost) string {
	if len(candidates) == 0 {
		return ReasonNoNodes
	}
	labelled := filter(candidates, func(c nodes.Snapshot) bool { return hasAll(c.Labels, ts.Labels) })
	if len(labelled) == 0 {
		return ReasonNoLabels + " (" + strings.Join(ts.Labels, ", ") + ")"
	}
	awake := filter(labelled, func(c nodes.Snapshot) bool { return !c.Draining })
	if len(awake) == 0 {
		return ReasonAllDraining
	}
	roomy := filter(awake, func(c nodes.Snapshot) bool { return fitsCPU(c, cost) && fitsMemory(c, cost) })
	if len(roomy) == 0 {
		return fmt.Sprintf("%s (wants %g CPU, %d MB)", ReasonNoRoom, cost.CPU, cost.MemoryMB)
	}
	return ReasonFull
}

func filter(in []nodes.Snapshot, keep func(nodes.Snapshot) bool) []nodes.Snapshot {
	out := make([]nodes.Snapshot, 0, len(in))
	for _, c := range in {
		if keep(c) {
			out = append(out, c)
		}
	}
	return out
}

func hasAll(have, required []string) bool {
	for _, r := range required {
		if !slices.Contains(have, r) {
			return false
		}
	}
	return true
}
