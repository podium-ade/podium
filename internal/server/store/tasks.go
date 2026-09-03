package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/alvaroibarguen/podium/internal/ids"
	"github.com/alvaroibarguen/podium/internal/server/store/db"
)

// Pagination bounds for ListTasks.
const (
	DefaultPageLimit = 50
	MaxPageLimit     = 500
)

// legalTransitions is the task state graph. Everything not listed here is rejected with
// ErrInvalidTransition.
//
//	queued        -> scheduled | failed | cancelled
//	scheduled     -> provisioning | failed | cancelled | lost | queued
//	provisioning  -> running      | failed | cancelled | lost | queued
//	running       -> succeeded    | failed | cancelled | lost | queued
//
// queued -> failed exists because a task can be found un-runnable before anything is
// scheduled: step 09's scheduler resolves the task's secrets before it assigns, and a
// reference to a name that does not exist fails the task without a node ever seeing it.
//
// Requeueing (-> queued) additionally requires attempts < max_attempts.
var legalTransitions = map[Status][]Status{
	StatusQueued:       {StatusScheduled, StatusFailed, StatusCancelled},
	StatusScheduled:    {StatusProvisioning, StatusFailed, StatusCancelled, StatusLost, StatusQueued},
	StatusProvisioning: {StatusRunning, StatusFailed, StatusCancelled, StatusLost, StatusQueued},
	StatusRunning:      {StatusSucceeded, StatusFailed, StatusCancelled, StatusLost, StatusQueued},
}

// CanTransition reports whether from -> to is an edge of the task state graph. It does not
// check the requeue attempt budget; TransitionTask does.
func CanTransition(from, to Status) bool {
	for _, s := range legalTransitions[from] {
		if s == to {
			return true
		}
	}
	return false
}

// CreateTask inserts a queued task.
func (s *Store) CreateTask(ctx context.Context, in NewTask) (Task, error) {
	if in.ID == "" {
		in.ID = ids.NewTask()
	}
	if in.MaxAttempts <= 0 {
		in.MaxAttempts = 1
		if in.Spec.MaxAttempts > 0 {
			in.MaxAttempts = int32(in.Spec.MaxAttempts)
		}
	}
	specJSON, err := json.Marshal(in.Spec)
	if err != nil {
		return Task{}, fmt.Errorf("marshal task spec: %w", err)
	}
	row, err := s.q.CreateTask(ctx, db.CreateTaskParams{
		ID:          in.ID,
		Spec:        specJSON,
		Status:      string(StatusQueued),
		Priority:    in.Priority,
		RequestedBy: in.RequestedBy,
		MaxAttempts: in.MaxAttempts,
	})
	if err != nil {
		return Task{}, fmt.Errorf("insert task %s: %w", in.ID, err)
	}
	return taskFromRow(row)
}

// GetTask returns one task, or ErrNotFound.
func (s *Store) GetTask(ctx context.Context, taskID string) (Task, error) {
	row, err := s.q.GetTask(ctx, taskID)
	if noRows(err) {
		return Task{}, fmt.Errorf("task %s: %w", taskID, ErrNotFound)
	}
	if err != nil {
		return Task{}, fmt.Errorf("select task %s: %w", taskID, err)
	}
	return taskFromRow(row)
}

// ListTasks returns tasks newest-first. nextCursor is empty when the page is the last one;
// otherwise pass it back as Page.Cursor.
func (s *Store) ListTasks(ctx context.Context, f Filter, p Page) ([]Task, string, error) {
	limit := p.Limit
	switch {
	case limit <= 0:
		limit = DefaultPageLimit
	case limit > MaxPageLimit:
		limit = MaxPageLimit
	}
	statuses := make([]string, 0, len(f.Status))
	for _, st := range f.Status {
		statuses = append(statuses, string(st))
	}
	rows, err := s.q.ListTasks(ctx, db.ListTasksParams{
		Statuses:    statuses,
		NodeID:      f.NodeID,
		RequestedBy: f.RequestedBy,
		Search:      f.Search,
		AfterID:     p.Cursor,
		PageLimit:   int32(limit),
	})
	if err != nil {
		return nil, "", fmt.Errorf("list tasks: %w", err)
	}
	tasks, err := tasksFromRows(rows)
	if err != nil {
		return nil, "", err
	}
	next := ""
	if len(tasks) == limit {
		next = tasks[len(tasks)-1].ID
	}
	return tasks, next, nil
}

// ClaimQueuedTasks returns up to limit queued tasks in scheduling order, taking a row lock on
// each with FOR UPDATE SKIP LOCKED so concurrent claimers get disjoint candidate sets. It does
// not transition them: the scheduler decides, and AssignTask is the exclusivity gate.
func (s *Store) ClaimQueuedTasks(ctx context.Context, limit int) ([]Task, error) {
	if limit <= 0 {
		limit = 1
	}
	var out []Task
	err := s.inTx(ctx, func(q *db.Queries) error {
		rows, err := q.ClaimQueuedTasks(ctx, int32(limit))
		if err != nil {
			return fmt.Errorf("claim queued tasks: %w", err)
		}
		out, err = tasksFromRows(rows)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// AssignTask moves a task queued -> scheduled, stamps scheduled_at and the lease, and bumps
// attempts. It returns ErrInvalidTransition when the task is no longer queued, which is what
// makes two racing schedulers safe.
func (s *Store) AssignTask(ctx context.Context, taskID, nodeID, leaseID string, leaseExpires time.Time) error {
	_, err := s.q.AssignTask(ctx, db.AssignTaskParams{
		NodeID:         &nodeID,
		LeaseID:        &leaseID,
		LeaseExpiresAt: &leaseExpires,
		ID:             taskID,
	})
	if noRows(err) {
		cur, gerr := s.GetTask(ctx, taskID)
		if gerr != nil {
			return gerr
		}
		return fmt.Errorf("assign task %s: it is %s, not %s: %w", taskID, cur.Status, StatusQueued, ErrInvalidTransition)
	}
	if err != nil {
		return fmt.Errorf("assign task %s: %w", taskID, err)
	}
	return nil
}

// TransitionTask moves a task to `to` and applies patch, all under a row lock. An empty `from`
// means "whatever the current status is, as long as the edge is legal". It returns
// ErrInvalidTransition when the row is in none of `from`, when the edge is not in the state
// graph, or when a requeue would exceed max_attempts.
func (s *Store) TransitionTask(ctx context.Context, taskID string, from []Status, to Status, patch Patch) (Task, error) {
	if !to.Valid() {
		return Task{}, fmt.Errorf("transition task %s: %q is not a task status: %w", taskID, to, ErrInvalidTransition)
	}
	usageJSON, err := marshalUsage(patch.Usage)
	if err != nil {
		return Task{}, err
	}

	var out Task
	err = s.inTx(ctx, func(q *db.Queries) error {
		row, err := q.GetTaskForUpdate(ctx, taskID)
		if noRows(err) {
			return fmt.Errorf("task %s: %w", taskID, ErrNotFound)
		}
		if err != nil {
			return fmt.Errorf("lock task %s: %w", taskID, err)
		}
		cur := Status(row.Status)
		to, from, patch = applyCancelIntent(row, to, from, patch)
		if len(from) > 0 && !containsStatus(from, cur) {
			return fmt.Errorf("transition task %s to %s: it is %s, want one of %v: %w",
				taskID, to, cur, from, ErrInvalidTransition)
		}
		if !CanTransition(cur, to) {
			return fmt.Errorf("transition task %s: %s -> %s is not a legal edge: %w",
				taskID, cur, to, ErrInvalidTransition)
		}
		if to == StatusQueued && row.Attempts >= row.MaxAttempts {
			return fmt.Errorf("requeue task %s: attempts %d of %d exhausted: %w",
				taskID, row.Attempts, row.MaxAttempts, ErrInvalidTransition)
		}
		updated, err := q.UpdateTaskTransition(ctx, db.UpdateTaskTransitionParams{
			ToStatus:       string(to),
			StartedAt:      patch.StartedAt,
			FinishedAt:     patch.FinishedAt,
			ExitCode:       patch.ExitCode,
			Usage:          usageJSON,
			FailureReason:  patch.FailureReason,
			NodeID:         patch.NodeID,
			LeaseID:        patch.LeaseID,
			LeaseExpiresAt: patch.LeaseExpiresAt,
			ID:             taskID,
		})
		if err != nil {
			return fmt.Errorf("transition task %s to %s: %w", taskID, to, err)
		}
		out, err = taskFromRow(updated)
		return err
	})
	if err != nil {
		return Task{}, err
	}
	return out, nil
}

// applyCancelIntent rewrites a terminal transition to honour a stop the operator (or the
// server's own timeout) asked for while the task was still alive.
//
// It lives here, inside the row lock, rather than in a map in the API handler, because the
// intent has to survive a server restart: a task cancelled at second 1 and finishing at
// second 40, with a restart in between, used to land succeeded. The node's own event is
// still the thing that says *when* the task ended and with what code — the intent only
// renames the outcome and supplies the reason.
//
// The `from` guard is dropped when the intent applies. The caller's guard names the state
// the *original* target came from (running, for a finished event), and a cancelled task
// may legitimately be a step behind that.
func applyCancelIntent(row db.Task, to Status, from []Status, patch Patch) (Status, []Status, Patch) {
	if row.CancelRequestedAt == nil {
		return to, from, patch
	}
	if to != StatusSucceeded && to != StatusFailed {
		return to, from, patch
	}
	want := Status(deref(row.CancelStatus))
	if !want.Valid() || !want.Terminal() {
		want = StatusCancelled
	}
	// The intent's reason wins over whatever the event implied. A task that was both
	// cancelled and OOM-killed is better explained by "cancelled by alvaro" than by "oom":
	// somebody asked for it to stop, and that is the thing that happened first.
	if row.CancelReason != nil && *row.CancelReason != "" {
		reason := *row.CancelReason
		patch.FailureReason = &reason
	}
	return want, nil, patch
}

// ClaimActiveTasks returns every task that has been handed to a node and not finished:
// the reconciler's whole working set.
func (s *Store) ClaimActiveTasks(ctx context.Context) ([]Task, error) {
	rows, err := s.q.ClaimActiveTasks(ctx)
	if err != nil {
		return nil, fmt.Errorf("list active tasks: %w", err)
	}
	return tasksFromRows(rows)
}

// ListTasksOnNode returns the node's tasks in the given statuses, oldest first.
func (s *Store) ListTasksOnNode(ctx context.Context, nodeID string, statuses []Status) ([]Task, error) {
	names := make([]string, 0, len(statuses))
	for _, st := range statuses {
		names = append(names, string(st))
	}
	rows, err := s.q.ListTasksOnNode(ctx, db.ListTasksOnNodeParams{NodeID: &nodeID, Statuses: names})
	if err != nil {
		return nil, fmt.Errorf("list tasks on node %s: %w", nodeID, err)
	}
	return tasksFromRows(rows)
}

// MarkScheduleAttempt records that the scheduler looked at a queued task and could not
// place it, and why. It is a no-op for a task that is no longer queued, so it can never
// race a concurrent assignment into writing a stale reason.
func (s *Store) MarkScheduleAttempt(ctx context.Context, taskID, reason string) error {
	if err := s.q.MarkScheduleAttempt(ctx, db.MarkScheduleAttemptParams{
		QueuedReason: reason,
		ID:           taskID,
	}); err != nil {
		return fmt.Errorf("record schedule attempt for %s: %w", taskID, err)
	}
	return nil
}

// RequestCancel records a durable intent to stop a task and the terminal status that
// intent should produce — cancelled for an operator's cancel, failed for a server-side
// timeout. It is idempotent: the first request wins, so a timeout cannot relabel a task
// the operator already cancelled. A terminal task is ErrInvalidTransition.
func (s *Store) RequestCancel(ctx context.Context, taskID, reason string, terminal Status) (Task, error) {
	if !terminal.Terminal() {
		return Task{}, fmt.Errorf("request cancel of %s: %q is not a terminal status: %w",
			taskID, terminal, ErrInvalidTransition)
	}
	row, err := s.q.RequestCancel(ctx, db.RequestCancelParams{
		CancelReason: reason,
		CancelStatus: string(terminal),
		ID:           taskID,
	})
	if noRows(err) {
		cur, gerr := s.GetTask(ctx, taskID)
		if gerr != nil {
			return Task{}, gerr
		}
		return Task{}, fmt.Errorf("request cancel of %s: it is already %s: %w",
			taskID, cur.Status, ErrInvalidTransition)
	}
	if err != nil {
		return Task{}, fmt.Errorf("request cancel of %s: %w", taskID, err)
	}
	return taskFromRow(row)
}

// ExtendLease pushes a live task's lease expiry out. It matches on the lease id as well as
// the task, so a scheduler holding a stale view cannot extend a lease that has moved on.
// It reports ErrNotFound when nothing matched, which the caller normally ignores: the task
// having moved is exactly the case the guard exists for.
func (s *Store) ExtendLease(ctx context.Context, taskID, leaseID string, expires time.Time) error {
	expires = expires.UTC()
	n, err := s.q.ExtendLease(ctx, db.ExtendLeaseParams{
		LeaseExpiresAt: &expires,
		ID:             taskID,
		LeaseID:        &leaseID,
	})
	if err != nil {
		return fmt.Errorf("extend lease of task %s: %w", taskID, err)
	}
	if n == 0 {
		return fmt.Errorf("extend lease of task %s: %w", taskID, ErrNotFound)
	}
	return nil
}

func containsStatus(list []Status, s Status) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func marshalUsage(u *Usage) ([]byte, error) {
	if u == nil {
		return nil, nil
	}
	b, err := json.Marshal(u)
	if err != nil {
		return nil, fmt.Errorf("marshal task usage: %w", err)
	}
	return b, nil
}

func taskFromRow(row db.Task) (Task, error) {
	t := Task{
		ID:             row.ID,
		Status:         Status(row.Status),
		Priority:       row.Priority,
		RequestedBy:    row.RequestedBy,
		NodeID:         deref(row.NodeID),
		LeaseID:        deref(row.LeaseID),
		LeaseExpiresAt: utcPtr(row.LeaseExpiresAt),
		Attempts:       row.Attempts,
		MaxAttempts:    row.MaxAttempts,
		CreatedAt:      row.CreatedAt.UTC(),
		ScheduledAt:    utcPtr(row.ScheduledAt),
		StartedAt:      utcPtr(row.StartedAt),
		FinishedAt:     utcPtr(row.FinishedAt),
		ExitCode:       row.ExitCode,
		FailureReason:  deref(row.FailureReason),

		LastScheduleAttemptAt: utcPtr(row.LastScheduleAttemptAt),
		QueuedReason:          deref(row.QueuedReason),
		CancelRequestedAt:     utcPtr(row.CancelRequestedAt),
		CancelReason:          deref(row.CancelReason),
		CancelStatus:          Status(deref(row.CancelStatus)),
	}
	if err := json.Unmarshal(row.Spec, &t.Spec); err != nil {
		return Task{}, fmt.Errorf("decode spec of task %s: %w", row.ID, err)
	}
	if len(row.Usage) > 0 {
		var u Usage
		if err := json.Unmarshal(row.Usage, &u); err != nil {
			return Task{}, fmt.Errorf("decode usage of task %s: %w", row.ID, err)
		}
		t.Usage = &u
	}
	return t, nil
}

func tasksFromRows(rows []db.Task) ([]Task, error) {
	out := make([]Task, 0, len(rows))
	for _, r := range rows {
		t, err := taskFromRow(r)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}
