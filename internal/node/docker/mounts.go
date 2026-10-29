package docker

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"

	"github.com/docker/docker/api/types/mount"

	"github.com/podium-ade/podium/pkg/spec"
)

// mountsSubdir is where a task's file mounts are written inside its state directory, which
// Teardown already removes.
const mountsSubdir = "files"

// The modes a mount is written with. World-readable for the reason secretFileMode is: a bind
// mount keeps the node's uid, and the task runs as whatever user its image chose. Nothing
// is writable or executable, and the mount itself is read-only on top of that.
const (
	mountDirMode  fs.FileMode = 0o755
	mountFileMode fs.FileMode = 0o444
)

// stageMounts writes every FileMount of the spec to the task's state directory and returns
// the read-only bind mounts that put them at their targets. The spec has been validated,
// so every target is under spec.FilesMount and every path is an ordinary relative one.
func (e *Executor) stageMounts(taskID string, mounts []spec.FileMount) ([]mount.Mount, error) {
	out := make([]mount.Mount, 0, len(mounts))
	for i, m := range mounts {
		dir := filepath.Join(e.taskDir(taskID), mountsSubdir, strconv.Itoa(i))
		if err := writeMount(dir, m); err != nil {
			return nil, fmt.Errorf("%w: mount %s: %w", errSpec, m.Target, err)
		}
		out = append(out, mount.Mount{
			Type:     mount.TypeBind,
			Source:   dir,
			Target:   m.Target,
			ReadOnly: true,
		})
	}
	return out, nil
}

// writeMount writes one FileMount's files under dir, which must not exist yet.
//
// The modes are set with chmod rather than left to open(2) and mkdir(2), whose permission
// arguments the process umask subtracts from: a node started with umask 077 would otherwise
// write a mount its own task cannot read.
func writeMount(dir string, m spec.FileMount) error {
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		return err
	}
	if err := os.Mkdir(dir, mountDirMode); err != nil {
		return err
	}
	if err := os.Chmod(dir, mountDirMode); err != nil {
		return err
	}
	for rel, content := range m.Files {
		if err := spec.CheckMountPath(rel); err != nil {
			return err
		}
		path := filepath.Join(dir, filepath.FromSlash(rel))
		if err := mkdirsUnder(dir, filepath.Dir(path)); err != nil {
			return err
		}
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mountFileMode)
		if err != nil {
			return err
		}
		_, werr := f.WriteString(content)
		if werr == nil {
			werr = f.Chmod(mountFileMode)
		}
		if cerr := f.Close(); werr == nil {
			werr = cerr
		}
		if werr != nil {
			return werr
		}
	}
	return nil
}

// mkdirsUnder makes every directory from root (which exists) down to dir with mountDirMode.
func mkdirsUnder(root, dir string) error {
	if dir == root {
		return nil
	}
	if err := mkdirsUnder(root, filepath.Dir(dir)); err != nil {
		return err
	}
	if err := os.Mkdir(dir, mountDirMode); err != nil {
		if os.IsExist(err) {
			return nil
		}
		return err
	}
	return os.Chmod(dir, mountDirMode)
}
