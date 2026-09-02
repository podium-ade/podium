package spec

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	yaml "go.yaml.in/yaml/v3"
)

func TestParseTaskSpecAppliesDefaults(t *testing.T) {
	got, err := ParseTaskSpec(strings.NewReader("image: alpine:3\n"))
	require.NoError(t, err)
	assert.Equal(t, "alpine:3", got.Image)
	assert.Equal(t, DefaultWorkingDir, got.WorkingDir)
	assert.Equal(t, Duration(time.Hour), got.Timeout)
	assert.Equal(t, 1, got.MaxAttempts)
}

func TestParseTaskSpecFull(t *testing.T) {
	const doc = `
image: ghcr.io/acme/build:1
command: ["sh", "-c", "make"]
working_dir: /src
env:
  CI: "true"
  TOKEN_PATH: /run/x
labels: [linux/arm64, docker]
timeout: 30s
max_attempts: 3
`
	got, err := ParseTaskSpec(strings.NewReader(doc))
	require.NoError(t, err)
	assert.Equal(t, &TaskSpec{
		Image:       "ghcr.io/acme/build:1",
		Command:     []string{"sh", "-c", "make"},
		WorkingDir:  "/src",
		Env:         map[string]string{"CI": "true", "TOKEN_PATH": "/run/x"},
		Labels:      []string{"linux/arm64", "docker"},
		Timeout:     Duration(30 * time.Second),
		MaxAttempts: 3,
	}, got)
}

func TestParseTaskSpecRejectsUnknownField(t *testing.T) {
	_, err := ParseTaskSpec(strings.NewReader("image: alpine:3\nsidecars: {}\n"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sidecars")
}

func TestYAMLRoundTrip(t *testing.T) {
	want := &TaskSpec{
		Image:       "alpine:3",
		Command:     []string{"echo", "hi"},
		WorkingDir:  "/workspace",
		Env:         map[string]string{"A": "1"},
		Labels:      []string{"demo"},
		Timeout:     Duration(90 * time.Second),
		MaxAttempts: 2,
	}
	b, err := yaml.Marshal(want)
	require.NoError(t, err)
	assert.Contains(t, string(b), "timeout: 1m30s")

	got, err := ParseTaskSpec(strings.NewReader(string(b)))
	require.NoError(t, err)
	assert.Equal(t, want, got)
}

func TestApplyDefaultsDoesNotMaskBadValues(t *testing.T) {
	s := &TaskSpec{Image: "alpine:3", Timeout: Duration(-time.Second), MaxAttempts: -2}
	s.ApplyDefaults()
	assert.Equal(t, Duration(-time.Second), s.Timeout)
	assert.Equal(t, -2, s.MaxAttempts)
	require.Error(t, s.Validate())
}

func TestValidate(t *testing.T) {
	valid := func() *TaskSpec {
		s := &TaskSpec{Image: "alpine:3"}
		s.ApplyDefaults()
		return s
	}

	require.NoError(t, valid().Validate())

	for name, tc := range map[string]struct {
		mutate func(*TaskSpec)
		want   string
	}{
		"empty image":       {func(s *TaskSpec) { s.Image = "" }, "image is required"},
		"blank image":       {func(s *TaskSpec) { s.Image = "   " }, "image is required"},
		"zero timeout":      {func(s *TaskSpec) { s.Timeout = 0 }, "timeout must be positive"},
		"negative timeout":  {func(s *TaskSpec) { s.Timeout = Duration(-time.Minute) }, "timeout must be positive"},
		"zero attempts":     {func(s *TaskSpec) { s.MaxAttempts = 0 }, "max_attempts must be at least 1"},
		"negative attempts": {func(s *TaskSpec) { s.MaxAttempts = -1 }, "max_attempts must be at least 1"},
		"empty env key":     {func(s *TaskSpec) { s.Env = map[string]string{"": "v"} }, "env key must not be empty"},
		"env key with dash": {func(s *TaskSpec) { s.Env = map[string]string{"MY-VAR": "v"} }, "not a valid shell identifier"},
		"env key leading digit": {
			func(s *TaskSpec) { s.Env = map[string]string{"1VAR": "v"} }, "not a valid shell identifier",
		},
		"empty label": {func(s *TaskSpec) { s.Labels = []string{" "} }, "label must not be empty"},
	} {
		t.Run(name, func(t *testing.T) {
			s := valid()
			tc.mutate(s)
			err := s.Validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

func TestValidateReportsEveryProblem(t *testing.T) {
	s := &TaskSpec{Env: map[string]string{"ok": "1", "bad-1": "x", "bad-2": "y"}}
	err := s.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "image is required")
	assert.Contains(t, err.Error(), "timeout must be positive")
	assert.Contains(t, err.Error(), "max_attempts must be at least 1")
	assert.Contains(t, err.Error(), `env key "bad-1"`)
	assert.Contains(t, err.Error(), `env key "bad-2"`)
}

func TestDurationJSON(t *testing.T) {
	var d Duration
	require.NoError(t, d.UnmarshalJSON([]byte(`"1h30m"`)))
	assert.Equal(t, Duration(90*time.Minute), d)

	b, err := d.MarshalJSON()
	require.NoError(t, err)
	assert.JSONEq(t, `"1h30m0s"`, string(b))

	require.Error(t, d.UnmarshalJSON([]byte(`"nonsense"`)))
}
