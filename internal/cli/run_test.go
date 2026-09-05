package cli

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alvaroibarguen/podium/pkg/spec"
)

func TestBuildSpecFromFlags(t *testing.T) {
	got, err := buildSpec("", "alpine:3", "", []string{"linux/arm64"}, []string{"A=1", "B=2"}, nil,
		90*time.Second, []string{"sh", "-c", "echo hi"})
	require.NoError(t, err)
	require.Equal(t, "alpine:3", got.Image)
	require.Equal(t, []string{"sh", "-c", "echo hi"}, got.Command)
	require.Equal(t, []string{"linux/arm64"}, got.Labels)
	require.Equal(t, map[string]string{"A": "1", "B": "2"}, got.Env)
	require.Equal(t, spec.Duration(90*time.Second), got.Timeout)
	require.Equal(t, spec.DefaultWorkingDir, got.WorkingDir, "defaults are applied client side too")
	require.Equal(t, spec.DefaultMaxAttempts, got.MaxAttempts)
}

func TestBuildSpecFlagsOverrideTheSpecFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "task.yaml")
	require.NoError(t, os.WriteFile(path, []byte(
		"image: alpine:3\ncommand: [true]\nworking_dir: /srv\nenv:\n  A: from-file\n"), 0o600))

	got, err := buildSpec(path, "busybox:1", "", nil, []string{"A=from-flag"}, nil, 0, []string{"false"})
	require.NoError(t, err)
	require.Equal(t, "busybox:1", got.Image)
	require.Equal(t, []string{"false"}, got.Command)
	require.Equal(t, "/srv", got.WorkingDir, "what no flag touches survives")
	require.Equal(t, "from-flag", got.Env["A"])
}

func TestBuildSpecRejectsBadInput(t *testing.T) {
	_, err := buildSpec("", "", "", nil, nil, nil, 0, []string{"true"})
	require.ErrorContains(t, err, "image is required")

	_, err = buildSpec("", "alpine:3", "", nil, []string{"NOTKV"}, nil, 0, nil)
	require.ErrorContains(t, err, "not KEY=VALUE")
}

func TestParseTaskStatus(t *testing.T) {
	s, err := parseTaskStatus("Running")
	require.NoError(t, err)
	require.Equal(t, "running", taskStatusName(s))

	_, err = parseTaskStatus("nope")
	require.ErrorContains(t, err, "unknown status")
	require.ErrorContains(t, err, "cancelled")
}

// The shipped examples must stay valid: they are the first thing an adopter runs.
func TestShippedExamplesParse(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("..", "..", "examples", "*.yaml"))
	require.NoError(t, err)
	require.NotEmpty(t, paths)

	for _, p := range paths {
		t.Run(filepath.Base(p), func(t *testing.T) {
			s, err := buildSpec(p, "", "", nil, nil, nil, 0, nil)
			require.NoError(t, err)
			assert.NotEmpty(t, s.Image)
		})
	}
}

func TestPostgresSidecarExampleShape(t *testing.T) {
	s, err := buildSpec(filepath.Join("..", "..", "examples", "postgres-sidecar.yaml"), "", "", nil, nil, nil, 0, nil)
	require.NoError(t, err)

	require.Contains(t, s.Sidecars, "db")
	// A command probe rather than tcp_port: the image is Debian-based and has no `nc`, which
	// the tcp_port probe execs inside the sidecar. docs/task-spec.md#readiness says so.
	assert.Equal(t, []string{"pg_isready", "-U", "postgres"}, s.Sidecars["db"].Readiness.Command)
	assert.Zero(t, s.Sidecars["db"].Readiness.TCPPort)
	assert.Equal(t, "pgvector/pgvector:pg16", s.Sidecars["db"].Image)
	assert.True(t, s.Hardening.ReadOnlyRootfs)
	assert.Equal(t, 256, s.Resources.MemoryMB)
}

// TestVerdictDoesNotRepeatAnErrorTheStreamAlreadyShowed.
//
// A sidecar that never becomes ready puts a hundred lines of the sidecar's own log inside
// the error that fails the task, and `podium run` printed that whole blob twice — once as
// `error:` when the event arrived and again as `failed:` at the end — on top of the live
// sidecar log stream that had already shown every one of those lines.
func TestVerdictDoesNotRepeatAnErrorTheStreamAlreadyShowed(t *testing.T) {
	blob := "sidecar not ready: sidecar db not ready: nothing is listening on port 5432\n" +
		"[db] FATAL: could not open my data directory\n[db] and another line"

	got := verdict(blob, blob)
	require.Equal(t,
		"sidecar not ready: sidecar db not ready: nothing is listening on port 5432 (see the error above)",
		got)
	require.NotContains(t, got, "\n", "the verdict is one line")

	// A reason the stream never printed — the server's own failure_reason, say — is printed
	// whole: there is no copy above to point at.
	require.Equal(t, blob, verdict(blob, ""))

	// A one-line reason is the same either way.
	require.Equal(t, "image not found", verdict("image not found", "image not found"))
}
