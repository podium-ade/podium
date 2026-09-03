package docker

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/docker/docker/api/types/mount"

	"github.com/alvaroibarguen/podium/pkg/spec"
)

// secretFileMode is the mode of a staged secret file, on the node's disk and inside the
// container alike: readable by its owner and nobody else, and never executable.
const secretFileMode fs.FileMode = 0o400

// secretsSubdir is where a task's file-target secrets are staged inside its state
// directory, which Teardown already removes.
const secretsSubdir = "secrets"

// Secret is one resolved secret on its way into a task container. Value is plaintext; it
// exists in node memory for the length of one container start and is zeroed afterwards.
type Secret struct {
	Name   string
	Target string
	Key    string
	Value  []byte
}

// staged is what stageSecrets produced: environment entries for the container config, bind
// mounts for its host config, and the host paths to shred when the task is torn down.
type staged struct {
	env    []string
	mounts []mount.Mount
}

// stageSecrets turns resolved secrets into the two things a container needs.
//
// An env target becomes a KEY=value entry. A file target is written to the task's state
// directory with mode 0400 and bind-mounted read-only at the absolute path the ref asked
// for — which is normally under /podium/secrets, the noexec/nosuid tmpfs every task
// container already gets. A bind mount nested inside that tmpfs is mounted on top of it
// and keeps its 0400 mode, verified on Docker Desktop and asserted by
// TestFileSecretsAreMountedReadOnly.
//
// Writing into the tmpfs with CopyToContainer instead — the alternative the design
// considered, which would never touch node disk — does not work: a tmpfs is materialised
// when the container starts, so a copy before start fails outright ("Could not find the
// file /podium/secrets in container"), and a copy after start races the task's own first
// instruction. The bind mount wins.
//
// On any error everything already written is shredded, so a half-staged task leaves no
// plaintext behind.
func (e *Executor) stageSecrets(taskID string, secrets []Secret) (staged, error) {
	var out staged
	if len(secrets) == 0 {
		return out, nil
	}

	dir := e.secretsDir(taskID)
	needFiles := false
	for _, s := range secrets {
		if s.Target == spec.SecretTargetFile {
			needFiles = true
			break
		}
	}
	if needFiles {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return staged{}, fmt.Errorf("%w: create secrets dir for %s: %w", errSpec, taskID, err)
		}
	}

	for i, s := range secrets {
		switch s.Target {
		case spec.SecretTargetEnv:
			out.env = append(out.env, s.Key+"="+string(s.Value))
		case spec.SecretTargetFile:
			host := filepath.Join(dir, fmt.Sprintf("%02d-%s", i, s.Name))
			if err := writeSecretFile(host, s.Value); err != nil {
				e.shredSecrets(taskID)
				return staged{}, err
			}
			out.mounts = append(out.mounts, mount.Mount{
				Type:     mount.TypeBind,
				Source:   host,
				Target:   s.Key,
				ReadOnly: true,
			})
		default:
			e.shredSecrets(taskID)
			return staged{}, fmt.Errorf("%w: secret %s has target %q, want %s or %s",
				errSpec, s.Name, s.Target, spec.SecretTargetEnv, spec.SecretTargetFile)
		}
	}
	return out, nil
}

// writeSecretFile creates one 0400 file. O_EXCL because a name collision would mean two
// secrets sharing a file, and silently serving the wrong credential is worse than failing.
func writeSecretFile(path string, value []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, secretFileMode)
	if err != nil {
		return fmt.Errorf("stage secret file: %w", err)
	}
	if _, err := f.Write(value); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return fmt.Errorf("stage secret file: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return fmt.Errorf("stage secret file: %w", err)
	}
	return nil
}

// secretsDir is where one task's file-target secrets are staged.
func (e *Executor) secretsDir(taskID string) string {
	return filepath.Join(e.taskDir(taskID), secretsSubdir)
}

// shredSecrets overwrites every staged secret file with zeroes, then removes the
// directory. os.RemoveAll alone would unlink the files and leave their contents on the
// filesystem until the blocks were reused; overwriting first is not a guarantee on a
// copy-on-write or log-structured filesystem, but it is the most a userspace process can
// do and it costs nothing.
//
// It is idempotent and never returns an error: it runs on teardown paths that must
// continue regardless, and it reports what it could not do to the log.
func (e *Executor) shredSecrets(taskID string) {
	dir := e.secretsDir(taskID)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			e.log.Warn("reading the secrets dir to shred it failed", "task", taskID, "error", err)
		}
		return
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		if err := overwrite(path); err != nil {
			e.log.Warn("overwriting a secret file failed", "task", taskID, "error", err)
		}
		if err := os.Remove(path); err != nil {
			e.log.Warn("removing a secret file failed", "task", taskID, "error", err)
		}
	}
	if err := os.Remove(dir); err != nil && !errors.Is(err, fs.ErrNotExist) {
		e.log.Warn("removing the secrets dir failed", "task", taskID, "error", err)
	}
}

// overwrite fills a file with zeroes and flushes them to the device.
func overwrite(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	// The staged file is 0400, so it has to be made writable before it can be scrubbed.
	if err := os.Chmod(path, 0o600); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	if _, err := f.Write(make([]byte, info.Size())); err != nil {
		return err
	}
	return f.Sync()
}
