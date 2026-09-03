// Package api holds the operator-facing Connect handlers: tasks and node administration.
// Identity comes from the transport middleware; nothing here authenticates anyone.
package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"connectrpc.com/connect"

	podiumv1 "github.com/alvaroibarguen/podium/internal/proto/podium/v1"
	"github.com/alvaroibarguen/podium/internal/server/store"
	"github.com/alvaroibarguen/podium/internal/transport"
	"github.com/alvaroibarguen/podium/pkg/spec"
)

// Canceller is the node registry's cancel path.
type Canceller interface {
	Cancel(ctx context.Context, nodeID, taskID, reason string) error
}

// Events is the logs service, as much of it as the API needs.
type Events interface {
	Subscribe(ctx context.Context, taskID string, fromSeq uint64) (<-chan *podiumv1.TaskEvent, error)
}

// SecretChecker answers whether the secrets a spec names exist, without reading their
// values. It is optional: a server with no master key has no secrets to check against and
// refuses the task at assignment instead.
type SecretChecker interface {
	CheckRefs(ctx context.Context, refs []spec.SecretRef) error
}

// TaskService implements podium.v1.TaskService.
type TaskService struct {
	store   *store.Store
	events  Events
	nodes   Canceller
	secrets SecretChecker
	logger  *slog.Logger
}

// NewTaskService returns the task API.
func NewTaskService(st *store.Store, events Events, canceller Canceller, checker SecretChecker, logger *slog.Logger) *TaskService {
	if logger == nil {
		logger = slog.Default()
	}
	return &TaskService{store: st, events: events, nodes: canceller, secrets: checker, logger: logger}
}

// CreateTask validates the spec and queues the task. Scheduling is the scheduler's problem.
func (s *TaskService) CreateTask(
	ctx context.Context,
	req *connect.Request[podiumv1.CreateTaskRequest],
) (*connect.Response[podiumv1.CreateTaskResponse], error) {
	ts := spec.FromProto(req.Msg.GetSpec())
	if ts == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("create task: spec is required"))
	}
	ts.ApplyDefaults()
	if err := ts.Validate(); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid task spec: %w", err))
	}
	// Admission checks that the named secrets exist. The scheduler still resolves their
	// values at assignment — the value should be in server memory for as short a time as it
	// can be — but *existence* is settled here, because the only thing that used to notice
	// was a dispatch, and on a cluster with no node connected there is no dispatch. A task
	// that could never run then sat queued forever instead of saying why.
	var admission error
	if s.secrets != nil && len(ts.Secrets) > 0 {
		admission = s.secrets.CheckRefs(ctx, ts.Secrets)
	}

	task, err := s.store.CreateTask(ctx, store.NewTask{
		Spec:        *ts,
		Priority:    req.Msg.GetPriority(),
		RequestedBy: login(ctx),
		MaxAttempts: int32(ts.MaxAttempts),
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	s.logger.InfoContext(ctx, "task created",
		"task_id", task.ID, "image", task.Spec.Image, "labels", task.Spec.Labels,
		"requested_by", task.RequestedBy, "priority", task.Priority)

	if admission != nil {
		// The task is created and then failed rather than refused outright: the row is
		// the record of what somebody tried to run and why it could not, and it is what
		// `podium run` follows to print the reason. No attempt is spent and no node is
		// ever told about it.
		task = s.failAdmission(ctx, task, admission)
	}
	return connect.NewResponse(&podiumv1.CreateTaskResponse{Task: taskToProto(task)}), nil
}

// failAdmission moves a task straight to failed because something about it can never be
// satisfied, and returns whatever the row ended up as.
func (s *TaskService) failAdmission(ctx context.Context, task store.Task, cause error) store.Task {
	reason := cause.Error()
	now := time.Now().UTC()
	s.logger.WarnContext(ctx, "task cannot be satisfied and was failed at admission",
		"task_id", task.ID, "reason", reason)
	failed, err := s.store.TransitionTask(ctx, task.ID,
		[]store.Status{store.StatusQueued}, store.StatusFailed,
		store.Patch{FailureReason: &reason, FinishedAt: &now})
	if err != nil {
		s.logger.ErrorContext(ctx, "failing an unsatisfiable task failed", "task_id", task.ID, "error", err)
		return task
	}
	return failed
}

// GetTask returns one task.
func (s *TaskService) GetTask(
	ctx context.Context,
	req *connect.Request[podiumv1.GetTaskRequest],
) (*connect.Response[podiumv1.GetTaskResponse], error) {
	task, err := s.store.GetTask(ctx, req.Msg.GetTaskId())
	if err != nil {
		return nil, storeError(err)
	}
	return connect.NewResponse(&podiumv1.GetTaskResponse{Task: taskToProto(task)}), nil
}

// ListTasks pages newest-first. next_cursor is empty on the last page; feed it back verbatim.
func (s *TaskService) ListTasks(
	ctx context.Context,
	req *connect.Request[podiumv1.ListTasksRequest],
) (*connect.Response[podiumv1.ListTasksResponse], error) {
	f := req.Msg.GetFilter()
	filter := store.Filter{
		NodeID:      f.GetNodeId(),
		RequestedBy: f.GetRequestedBy(),
		Search:      strings.TrimSpace(f.GetSearch()),
	}
	for _, st := range f.GetStatus() {
		if mapped, ok := taskStatusStore[st]; ok {
			filter.Status = append(filter.Status, mapped)
		}
	}
	page := store.Page{Limit: int(req.Msg.GetPage().GetLimit()), Cursor: req.Msg.GetPage().GetCursor()}

	tasks, next, err := s.store.ListTasks(ctx, filter, page)
	if err != nil {
		return nil, storeError(err)
	}
	out := make([]*podiumv1.Task, 0, len(tasks))
	for _, t := range tasks {
		out = append(out, taskToProto(t))
	}
	return connect.NewResponse(&podiumv1.ListTasksResponse{Tasks: out, NextCursor: next}), nil
}

// CancelTask cancels a queued task outright and asks the node to stop anything further along.
// It never waits for the container to die: with the task command as PID 1 a default-disposition
// SIGTERM is discarded, so a cancel routinely costs the node's full 30s grace period. The
// terminal status lands when the exited/finished events arrive.
func (s *TaskService) CancelTask(
	ctx context.Context,
	req *connect.Request[podiumv1.CancelTaskRequest],
) (*connect.Response[podiumv1.CancelTaskResponse], error) {
	taskID := req.Msg.GetTaskId()
	reason := req.Msg.GetReason()
	if reason == "" {
		reason = "cancelled by " + login(ctx)
	}
	task, err := s.store.GetTask(ctx, taskID)
	if err != nil {
		return nil, storeError(err)
	}

	switch {
	case task.Status.Terminal():
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			fmt.Errorf("cancel task %s: it is already %s", taskID, task.Status))
	case task.Status == store.StatusQueued:
		now := time.Now().UTC()
		cancelled, err := s.store.TransitionTask(ctx, taskID,
			[]store.Status{store.StatusQueued}, store.StatusCancelled,
			store.Patch{FinishedAt: &now, FailureReason: &reason})
		if err != nil {
			return nil, storeError(err)
		}
		s.logger.InfoContext(ctx, "queued task cancelled", "task_id", taskID, "reason", reason)
		return connect.NewResponse(&podiumv1.CancelTaskResponse{Task: taskToProto(cancelled)}), nil
	}

	// The intent is written to the row, not to a map in this process: a server restart
	// between here and the node's finished event used to lose it and land the task
	// succeeded. TransitionTask reads it back under the row lock.
	task, err = s.store.RequestCancel(ctx, taskID, reason, store.StatusCancelled)
	if err != nil {
		return nil, storeError(err)
	}
	if task.NodeID == "" {
		s.logger.WarnContext(ctx, "cancelling a task with no node", "task_id", taskID, "status", task.Status)
	} else if err := s.nodes.Cancel(ctx, task.NodeID, taskID, reason); err != nil {
		// The node is unreachable. The scheduler's sweep re-sends the cancel while the
		// node is back, and gives up on it after the cancel grace.
		s.logger.WarnContext(ctx, "cancel could not reach the node",
			"task_id", taskID, "node_id", task.NodeID, "error", err)
	}
	s.logger.InfoContext(ctx, "task cancel requested", "task_id", taskID, "status", task.Status, "reason", reason)
	return connect.NewResponse(&podiumv1.CancelTaskResponse{Task: taskToProto(task)}), nil
}

// StreamTaskEvents replays everything stored after from_seq and then follows the task live. The
// two storage tables share the node's single per-task seq space, so the merged stream is in
// strict seq order. It ends when the task is terminal and every event has been sent.
func (s *TaskService) StreamTaskEvents(
	ctx context.Context,
	req *connect.Request[podiumv1.StreamTaskEventsRequest],
	stream *connect.ServerStream[podiumv1.TaskEvent],
) error {
	events, err := s.events.Subscribe(ctx, req.Msg.GetTaskId(), req.Msg.GetFromSeq())
	if err != nil {
		return storeError(err)
	}
	for e := range events {
		if err := stream.Send(e); err != nil {
			return err
		}
	}
	return ctx.Err()
}

// login is the identity the transport middleware attached, or "unknown" when a handler was
// reached without it.
func login(ctx context.Context) string {
	if id, ok := transport.From(ctx); ok && id.Login != "" {
		return id.Login
	}
	return "unknown"
}

// storeError maps the store's sentinels onto Connect codes.
func storeError(err error) error {
	switch {
	case errors.Is(err, store.ErrNotFound):
		return connect.NewError(connect.CodeNotFound, err)
	case errors.Is(err, store.ErrInvalidTransition):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	default:
		return connect.NewError(connect.CodeInternal, err)
	}
}
