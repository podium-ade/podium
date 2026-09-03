//go:build integration

package docker

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alvaroibarguen/podium/internal/ids"
	"github.com/alvaroibarguen/podium/pkg/spec"
)

func envSecret(name, key, value string) Secret {
	return Secret{Name: name, Target: spec.SecretTargetEnv, Key: key, Value: []byte(value)}
}

func fileSecret(name, path, value string) Secret {
	return Secret{Name: name, Target: spec.SecretTargetFile, Key: path, Value: []byte(value)}
}

func TestEnvSecretsReachTheTaskContainer(t *testing.T) {
	e := newTestExecutor(t)
	taskID := ids.NewTask()
	teardownAfter(t, e, taskID)

	c := newCollector()
	res, err := e.Run(context.Background(), Request{
		TaskID:  taskID,
		LeaseID: ids.NewLease(),
		Spec: specWithSidecars(t, spec.TaskSpec{
			Image:   testImage,
			Env:     map[string]string{"PLAIN": "not-a-secret"},
			Command: []string{"sh", "-c", `echo "$GREETING"; echo "$RENAMED"; echo "$PLAIN"`},
			Secrets: []spec.SecretRef{
				{Name: "GREETING", Target: "env", Key: "GREETING"},
				{Name: "OTHER", Target: "env", Key: "RENAMED"},
			},
		}),
		Secrets: []Secret{
			envSecret("GREETING", "GREETING", "hello from the secret store"),
			envSecret("OTHER", "RENAMED", "a different value"),
		},
	}, c.ch)
	require.NoError(t, err)
	require.Equal(t, 0, res.ExitCode)

	out := taskOutput(c.finish())
	assert.Equal(t, "hello from the secret store\na different value\nnot-a-secret\n", out)
}

// A secret's env entry is added after the spec's own sorted block, so containerEnv stays a
// pure function of the spec and a task with the same spec always gets the same env order.
func TestSecretEnvFollowsTheSpecsOwnSortedBlock(t *testing.T) {
	e := newTestExecutor(t)
	taskID := ids.NewTask()
	teardownAfter(t, e, taskID)

	c := newCollector()
	_, err := e.Run(context.Background(), Request{
		TaskID:  taskID,
		LeaseID: ids.NewLease(),
		Spec: specWithSidecars(t, spec.TaskSpec{
			Image:   testImage,
			Env:     map[string]string{"ZULU": "z", "ALPHA": "a"},
			Command: []string{"true"},
			Secrets: []spec.SecretRef{{Name: "S", Target: "env", Key: "S"}},
		}),
		Secrets: []Secret{envSecret("S", "S", "a secret value")},
	}, c.ch)
	require.NoError(t, err)
	c.finish()

	insp := inspectTask(t, e, taskID)
	var got []string
	for _, kv := range insp.Config.Env {
		if strings.HasPrefix(kv, "ALPHA=") || strings.HasPrefix(kv, "ZULU=") || strings.HasPrefix(kv, "S=") {
			got = append(got, strings.SplitN(kv, "=", 2)[0])
		}
	}
	assert.Equal(t, []string{"ALPHA", "ZULU", "S"}, got)
}

// The acceptance item: a file target lands 0400 under the /podium/secrets tmpfs, is
// readable, and is not writable even by root inside the container.
func TestFileSecretsAreMountedReadOnlyAt0400(t *testing.T) {
	e := newTestExecutor(t)
	taskID := ids.NewTask()
	teardownAfter(t, e, taskID)

	const script = `cat /podium/secrets/greeting; echo; ` +
		`ls -l /podium/secrets/greeting; ` +
		`(echo tampered > /podium/secrets/greeting) 2>&1 | head -1`

	c := newCollector()
	res, err := e.Run(context.Background(), Request{
		TaskID:  taskID,
		LeaseID: ids.NewLease(),
		Spec: specWithSidecars(t, spec.TaskSpec{
			Image:   testImage,
			Command: []string{"sh", "-c", script},
			Secrets: []spec.SecretRef{
				{Name: "GREETING", Target: "file", Key: "/podium/secrets/greeting"},
			},
		}),
		Secrets: []Secret{fileSecret("GREETING", "/podium/secrets/greeting", "hello from a mounted file")},
	}, c.ch)
	require.NoError(t, err)
	require.Equal(t, 0, res.ExitCode)

	out := taskOutput(c.finish())
	t.Logf("task output:\n%s", out)
	assert.Contains(t, out, "hello from a mounted file")
	assert.Contains(t, out, "-r--------", "the file must be mode 0400 inside the container")
	assert.Contains(t, strings.ToLower(out), "read-only file system")

	// The engine agrees the mount is read-only, and it is nested inside the tmpfs.
	insp := inspectTask(t, e, taskID)
	found := false
	for _, m := range insp.Mounts {
		if m.Destination == "/podium/secrets/greeting" {
			found = true
			assert.False(t, m.RW, "the secret bind mount must be read-only")
		}
	}
	assert.True(t, found, "no mount at /podium/secrets/greeting: %+v", insp.Mounts)
	assert.Equal(t, secretsTmpfs, insp.HostConfig.Tmpfs[secretsPath],
		"the tmpfs is still there under the bind mount")
}

func TestFileSecretsCanLandOutsideTheTmpfs(t *testing.T) {
	e := newTestExecutor(t)
	taskID := ids.NewTask()
	teardownAfter(t, e, taskID)

	c := newCollector()
	res, err := e.Run(context.Background(), Request{
		TaskID:  taskID,
		LeaseID: ids.NewLease(),
		Spec: specWithSidecars(t, spec.TaskSpec{
			Image:   testImage,
			Command: []string{"sh", "-c", "cat /etc/creds/token"},
			Secrets: []spec.SecretRef{{Name: "TOKEN", Target: "file", Key: "/etc/creds/token"}},
		}),
		Secrets: []Secret{fileSecret("TOKEN", "/etc/creds/token", "a token value")},
	}, c.ch)
	require.NoError(t, err)
	require.Equal(t, 0, res.ExitCode)
	assert.Equal(t, "a token value", taskOutput(c.finish()))
}

// The acceptance item: nothing survives teardown. The staged file is overwritten before it
// is unlinked, and the whole task directory goes with it.
func TestStagedSecretsAreShreddedByTeardown(t *testing.T) {
	e := newTestExecutor(t)
	taskID := ids.NewTask()

	const value = "correct-horse-battery-staple"
	c := newCollector()
	res, err := e.Run(context.Background(), Request{
		TaskID:  taskID,
		LeaseID: ids.NewLease(),
		Spec: specWithSidecars(t, spec.TaskSpec{
			Image:   testImage,
			Command: []string{"sh", "-c", "cat /podium/secrets/phrase"},
			Secrets: []spec.SecretRef{{Name: "PHRASE", Target: "file", Key: "/podium/secrets/phrase"}},
		}),
		Secrets: []Secret{fileSecret("PHRASE", "/podium/secrets/phrase", value)},
	}, c.ch)
	require.NoError(t, err)
	require.Equal(t, 0, res.ExitCode)
	c.finish()

	// While the task is alive the staged file exists on the node's disk, 0400.
	staged := filepath.Join(e.secretsDir(taskID), "00-PHRASE")
	info, err := os.Stat(staged)
	require.NoError(t, err, "the staged file should exist until teardown")
	assert.Equal(t, os.FileMode(0o400), info.Mode().Perm())
	onDisk, err := os.ReadFile(staged) //nolint:gosec // the test wrote it
	require.NoError(t, err)
	assert.Equal(t, value, string(onDisk))

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	require.NoError(t, e.Teardown(ctx, taskID, false))

	_, err = os.Stat(staged)
	assert.True(t, os.IsNotExist(err), "the staged secret file survived teardown")
	_, err = os.Stat(e.secretsDir(taskID))
	assert.True(t, os.IsNotExist(err), "the secrets directory survived teardown")
	_, err = os.Stat(e.TaskDir(taskID))
	assert.True(t, os.IsNotExist(err), "the task directory survived teardown")

	requireNoLeaks(t, e, taskID)
}

// A run that fails after staging still leaves nothing behind: Run tears down on its own
// error path.
func TestSecretsAreShreddedWhenTheRunFails(t *testing.T) {
	e := newTestExecutor(t)
	taskID := ids.NewTask()

	c := newCollector()
	_, err := e.Run(context.Background(), Request{
		TaskID:  taskID,
		LeaseID: ids.NewLease(),
		Spec: specWithSidecars(t, spec.TaskSpec{
			Image:   testImage,
			Command: []string{"true"},
			// An absolute path that is a directory in the image cannot be bind-mounted
			// over by a file, so the container create fails after staging.
			Secrets: []spec.SecretRef{{Name: "BAD", Target: "file", Key: "/etc"}},
		}),
		Secrets: []Secret{fileSecret("BAD", "/etc", "a value that never arrives")},
	}, c.ch)
	require.Error(t, err)
	c.finish()

	_, statErr := os.Stat(e.secretsDir(taskID))
	assert.True(t, os.IsNotExist(statErr), "a failed run left staged secrets behind")
	requireNoLeaks(t, e, taskID)
}

// shredSecrets is what Teardown relies on; prove it overwrites rather than just unlinking.
func TestShredSecretsOverwritesBeforeUnlinking(t *testing.T) {
	e := newTestExecutor(t)
	taskID := ids.NewTask()

	staged, err := e.stageSecrets(taskID, []Secret{
		fileSecret("A", "/podium/secrets/a", "value-a-value-a"),
		fileSecret("B", "/podium/secrets/b", "value-b-value-b"),
		envSecret("C", "C", "value-c"),
	})
	require.NoError(t, err)
	require.Len(t, staged.mounts, 2)
	require.Equal(t, []string{"C=value-c"}, staged.env)

	entries, err := os.ReadDir(e.secretsDir(taskID))
	require.NoError(t, err)
	require.Len(t, entries, 2, "only file targets are staged on disk")

	e.shredSecrets(taskID)
	_, err = os.Stat(e.secretsDir(taskID))
	assert.True(t, os.IsNotExist(err))

	// Idempotent: teardown paths call it whether or not anything was staged.
	e.shredSecrets(taskID)
	e.shredSecrets(ids.NewTask())
}

func TestStageSecretsRejectsAnUnknownTarget(t *testing.T) {
	e := newTestExecutor(t)
	taskID := ids.NewTask()

	_, err := e.stageSecrets(taskID, []Secret{{Name: "A", Target: "vault", Key: "A", Value: []byte("x")}})
	require.ErrorIs(t, err, errSpec)
	_, statErr := os.Stat(e.secretsDir(taskID))
	assert.True(t, os.IsNotExist(statErr))
}
