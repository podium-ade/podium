package docker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
)

// OwnedContainer is a container this node created for a task, as reported by
// [Executor.ListOwned].
type OwnedContainer struct {
	ID        string
	Name      string
	TaskID    string
	LeaseID   string
	Role      string
	Image     string
	State     string
	Status    string
	CreatedAt time.Time
}

// Cancel asks the task's container to stop: SIGTERM, then SIGKILL after a 30s
// grace period. It is idempotent, returns immediately, and does nothing for a
// task this executor is not running. The run reports the resulting exit
// normally; deciding that the task was "cancelled" is the caller's job.
func (e *Executor) Cancel(taskID string) {
	rs := e.lookup(taskID)
	if rs == nil {
		return
	}

	rs.mu.Lock()
	rs.cancelled = true
	cid := rs.containerID
	rs.mu.Unlock()

	if cid == "" {
		// The container does not exist yet; Run applies the flag once it starts.
		return
	}
	e.startKill(rs, cid)
}

// startKill runs the SIGTERM → grace → SIGKILL sequence once per run.
func (e *Executor) startKill(rs *runState, cid string) {
	rs.once.Do(func() {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), cancelGrace+time.Minute)
			defer cancel()

			if err := e.cli.ContainerKill(ctx, cid, "SIGTERM"); err != nil && !cerrdefs.IsNotFound(err) {
				e.log.Warn("send SIGTERM", "container", cid, "error", err)
			}
			select {
			case <-rs.done:
				return
			case <-time.After(cancelGrace):
			}
			if err := e.cli.ContainerKill(ctx, cid, "SIGKILL"); err != nil && !cerrdefs.IsNotFound(err) {
				e.log.Warn("send SIGKILL", "container", cid, "error", err)
			}
		}()
	})
}

// ListOwned returns every container on this engine labelled podium.task,
// running or not. Step 12's reconciliation uses it to find orphans after a
// daemon restart.
func (e *Executor) ListOwned(ctx context.Context) ([]OwnedContainer, error) {
	summaries, err := e.cli.ContainerList(ctx, container.ListOptions{
		All:     true,
		Filters: filters.NewArgs(filters.Arg("label", LabelTask)),
	})
	if err != nil {
		return nil, fmt.Errorf("list podium containers: %w", err)
	}

	owned := make([]OwnedContainer, 0, len(summaries))
	for _, s := range summaries {
		name := ""
		if len(s.Names) > 0 {
			name = s.Names[0][1:] // the API prefixes names with "/"
		}
		owned = append(owned, OwnedContainer{
			ID:        s.ID,
			Name:      name,
			TaskID:    s.Labels[LabelTask],
			LeaseID:   s.Labels[LabelLease],
			Role:      s.Labels[LabelRole],
			Image:     s.Image,
			State:     string(s.State),
			Status:    s.Status,
			CreatedAt: time.Unix(s.Created, 0).UTC(),
		})
	}
	return owned, nil
}

// Teardown removes everything a task owns: its containers (forcibly), its
// network, its workspace volume unless keepWorkspace, and its state directory.
// It is idempotent — resources that are already gone are not an error.
func (e *Executor) Teardown(ctx context.Context, taskID string, keepWorkspace bool) error {
	if taskID == "" {
		return errors.New("docker executor: teardown: taskID is required")
	}
	var errs []error

	summaries, err := e.cli.ContainerList(ctx, container.ListOptions{
		All:     true,
		Filters: filters.NewArgs(filters.Arg("label", LabelTask+"="+taskID)),
	})
	if err != nil {
		errs = append(errs, fmt.Errorf("list containers of task %s: %w", taskID, err))
	}
	for _, s := range summaries {
		if err := e.cli.ContainerRemove(ctx, s.ID, container.RemoveOptions{Force: true}); err != nil && !cerrdefs.IsNotFound(err) {
			errs = append(errs, fmt.Errorf("remove container %s: %w", s.ID, err))
		}
	}

	if err := e.cli.NetworkRemove(ctx, networkName(taskID)); err != nil && !cerrdefs.IsNotFound(err) {
		errs = append(errs, fmt.Errorf("remove network %s: %w", networkName(taskID), err))
	}

	if !keepWorkspace {
		if err := e.cli.VolumeRemove(ctx, volumeName(taskID), true); err != nil && !cerrdefs.IsNotFound(err) {
			errs = append(errs, fmt.Errorf("remove volume %s: %w", volumeName(taskID), err))
		}
	}

	if err := os.RemoveAll(e.taskDir(taskID)); err != nil {
		errs = append(errs, fmt.Errorf("remove task dir for %s: %w", taskID, err))
	}

	return errors.Join(errs...)
}
