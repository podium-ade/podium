package docker

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/strslice"
)

// Workspace is how a persistent session volume is restored and saved. The executor never
// talks to the control plane: the node fills these in.
//
// The tar is a Docker copy of /workspace. Its first entry is the workspace directory, so
// extracting it at / puts the files back on the volume. /podium/secrets is a tmpfs on the
// task container and is not on this volume, so a snapshot does not contain secret files.
type Workspace struct {
	// Restore is called after the volume exists and before anything is started. A false
	// found means the volume stays empty, which is a session that has never snapshotted
	// and has no base. A non-nil error fails the run.
	Restore func(ctx context.Context) (r io.ReadCloser, found bool, err error)
	// Save is called after a clean exit, with a tar of the volume, before the caller
	// tears the volume down. An error is logged and does not fail the run: the previous
	// snapshot stays the one on record when the upload itself fails.
	Save func(ctx context.Context, tarPath string, size int64) error
}

func (e *Executor) restoreWorkspace(ctx context.Context, req Request) error {
	if req.Workspace == nil || req.Workspace.Restore == nil {
		return nil
	}
	body, found, err := req.Workspace.Restore(ctx)
	if err != nil {
		return fmt.Errorf("restore workspace: %w", err)
	}
	if !found || body == nil {
		return nil
	}
	defer func() { _ = body.Close() }()

	err = e.withVolumeHelper(ctx, req.TaskID, req.Spec.Image, func(cid string) error {
		return e.cli.CopyToContainer(ctx, cid, "/", body, container.CopyToContainerOptions{})
	})
	if err != nil {
		return fmt.Errorf("restore workspace: %w", err)
	}
	return nil
}

func (e *Executor) saveWorkspace(ctx context.Context, req Request) error {
	if req.Workspace == nil || req.Workspace.Save == nil {
		return nil
	}
	f, err := os.CreateTemp("", "podium-ws-"+req.TaskID+"-*.tar")
	if err != nil {
		return fmt.Errorf("save workspace: %w", err)
	}
	defer func() { _ = os.Remove(f.Name()) }()

	err = e.withVolumeHelper(ctx, req.TaskID, req.Spec.Image, func(cid string) error {
		rc, _, cerr := e.cli.CopyFromContainer(ctx, cid, workspacePath)
		if cerr != nil {
			return cerr
		}
		defer func() { _ = rc.Close() }()
		_, cerr = io.Copy(f, rc)
		return cerr
	})
	if err != nil {
		_ = f.Close()
		return fmt.Errorf("save workspace: %w", err)
	}
	info, err := f.Stat()
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("save workspace: %w", err)
	}
	if info.Size() == 0 {
		return fmt.Errorf("save workspace: copy of %s was empty", volumeName(req.TaskID))
	}
	if err := req.Workspace.Save(ctx, f.Name(), info.Size()); err != nil {
		return fmt.Errorf("save workspace: %w", err)
	}
	return nil
}

// withVolumeHelper creates a container that is never started, with the task volume
// mounted at /workspace, and removes it when fn returns. Docker can copy in and out of
// a created container; starting it would run the task image's command.
func (e *Executor) withVolumeHelper(ctx context.Context, taskID, image string, fn func(cid string) error) error {
	name := "podium-wscopy-" + taskID
	_ = e.cli.ContainerRemove(ctx, name, container.RemoveOptions{Force: true})

	created, err := e.cli.ContainerCreate(ctx, &container.Config{
		Image:      image,
		Entrypoint: strslice.StrSlice{},
		Cmd:        strslice.StrSlice{"true"},
		Labels: map[string]string{
			LabelTask: taskID,
			LabelRole: RoleWorkspace,
		},
	}, &container.HostConfig{
		Mounts: []mount.Mount{{
			Type:   mount.TypeVolume,
			Source: volumeName(taskID),
			Target: workspacePath,
		}},
	}, nil, nil, name)
	if err != nil {
		return fmt.Errorf("create workspace helper: %w", err)
	}
	defer func() {
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		_ = e.cli.ContainerRemove(cctx, created.ID, container.RemoveOptions{Force: true})
	}()
	return fn(created.ID)
}
