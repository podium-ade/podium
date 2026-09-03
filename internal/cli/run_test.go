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
	assert.Equal(t, 5432, s.Sidecars["db"].Readiness.TCPPort)
	assert.Equal(t, "postgres:16-alpine", s.Sidecars["db"].Image)
	assert.True(t, s.Hardening.ReadOnlyRootfs)
	assert.Equal(t, 256, s.Resources.MemoryMB)
}
