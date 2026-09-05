package docker

import (
	"context"
	"errors"
	"fmt"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/container"
)

// StepReattached is the marker an adopted run puts in a task's history at the point where
// one daemon incarnation handed the container to the next.
const StepReattached = "node/reattached"

// AdoptRequest re-attaches to a task container that outlived the daemon.
type AdoptRequest struct {
	TaskID  string
	LeaseID string
	// FromSeq is the highest sequence number the previous incarnation of the
	// daemon assigned to this task. Adopt numbers its events from FromSeq+1 so
	// they never collide with rows the server already stored.
	FromSeq uint64
	// SkipStdout and SkipStderr are how many bytes of each stream the server has
	// already acknowledged. Docker replays a container's whole output on every
	// attach, so those bytes are read and discarded rather than sent twice.
	SkipStdout int64
	SkipStderr int64
}

// Adopt re-attaches to the container of a task this engine is already running,
// which is how a restarted daemon finishes a task it started before the
// restart. It emits the tail of the run: the output the caller says has not
// been delivered yet, then exited and finished.
//
// Like Run, Adopt does not close events and leaves Teardown to the caller.
func (e *Executor) Adopt(ctx context.Context, req AdoptRequest, events chan<- Event) (Result, error) {
	if req.TaskID == "" {
		return Result{}, errors.New("docker executor: adopt: TaskID is required")
	}
	em := newEmitterAt(ctx, events, req.FromSeq)

	rs, err := e.register(req.TaskID)
	if err != nil {
		em.emit(KindError, ErrorPayload{Message: err.Error(), Retryable: false, AbortsRun: true})
		return Result{}, err
	}
	defer e.unregister(req.TaskID, rs)

	res, err := e.adopt(ctx, req, em, rs)
	if err != nil {
		em.emit(KindError, ErrorPayload{Message: err.Error(), Retryable: true, AbortsRun: true})
		return Result{}, err
	}
	return res, nil
}

func (e *Executor) adopt(ctx context.Context, req AdoptRequest, em *emitter, rs *runState) (Result, error) {
	name := containerName(req.TaskID)
	insp, err := e.cli.ContainerInspect(ctx, name)
	if err != nil {
		if cerrdefs.IsNotFound(err) {
			return Result{}, fmt.Errorf("adopt task %s: no container %s", req.TaskID, name)
		}
		return Result{}, fmt.Errorf("adopt task %s: inspect %s: %w", req.TaskID, name, err)
	}
	cid := insp.ID

	rs.mu.Lock()
	rs.containerID = cid
	pending := rs.cancelled
	rs.mu.Unlock()
	if pending {
		e.startKill(rs, cid)
	}

	running := insp.State != nil && insp.State.Running

	// Say so in the task's own history. Everything the previous incarnation of the daemon
	// had in memory is gone with it: the runner's event socket, the log redactor, and any
	// sidecar log stream it had attached. The container keeps working and its stdout and
	// stderr resume exactly where the control plane says they stopped, but a reader of
	// this log deserves to know there is a seam here rather than wonder why the sidecar
	// went quiet. See docs/runner-events.md.
	em.emit(KindStep, StepPayload{Name: StepReattached, Status: "started"})

	// Attach unconditionally: a container that has already exited still holds the
	// output the previous incarnation never got acknowledged, and Docker ends the
	// follow by itself once it has replayed what it has.
	var waitCh <-chan container.WaitResponse
	var waitErrCh <-chan error
	if running {
		waitCh, waitErrCh = e.cli.ContainerWait(ctx, cid, container.WaitConditionNotRunning)
	}
	logsDone := e.streamLogsFrom(ctx, cid, em, req.SkipStdout, req.SkipStderr)

	if running {
		select {
		case werr := <-waitErrCh:
			if werr == nil {
				werr = errors.New("wait ended without a result")
			}
			return Result{}, fmt.Errorf("adopt task %s: wait: %w", req.TaskID, werr)
		case <-waitCh:
		}
	}
	select {
	case <-logsDone:
	case <-time.After(logDrainTimeout):
		e.log.Warn("adopted log stream did not close", "task", req.TaskID)
	}

	if running {
		insp, err = e.cli.ContainerInspect(ctx, cid)
		if err != nil {
			return Result{}, fmt.Errorf("adopt task %s: inspect after exit: %w", req.TaskID, err)
		}
	}

	exitCode, oom, wallMS := 0, false, int64(0)
	if insp.State != nil {
		exitCode = insp.State.ExitCode
		oom = insp.State.OOMKilled
		wallMS = wallMillis(insp.State.StartedAt, insp.State.FinishedAt)
	}
	usage := Usage{WallMS: wallMS}

	em.emit(KindExited, ExitedPayload{ExitCode: exitCode, OOMKilled: oom})
	em.emit(KindFinished, FinishedPayload{ExitCode: exitCode, Usage: usage})
	return Result{ExitCode: exitCode, OOMKilled: oom, Usage: usage}, nil
}

// wallMillis is the container's run time from the two RFC3339 stamps the
// inspect reports. Either being unparseable simply means no wall time.
func wallMillis(startedAt, finishedAt string) int64 {
	start, err := time.Parse(time.RFC3339Nano, startedAt)
	if err != nil {
		return 0
	}
	end, err := time.Parse(time.RFC3339Nano, finishedAt)
	if err != nil {
		return 0
	}
	if ms := end.Sub(start).Milliseconds(); ms > 0 {
		return ms
	}
	return 0
}
