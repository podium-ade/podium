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
		Resources:   Resources{PIDs: DefaultPIDs},
	}, got)
}

func TestParseTaskSpecRejectsUnknownField(t *testing.T) {
	_, err := ParseTaskSpec(strings.NewReader("image: alpine:3\nprivileged: true\n"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "privileged")
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
		Resources:   Resources{CPU: 1.5, MemoryMB: 512, PIDs: DefaultPIDs},
		Hardening:   Hardening{ReadOnlyRootfs: true, Capabilities: []string{"CHOWN"}},
		Sidecars: map[string]Sidecar{"db": {
			Image:     "postgres:16-alpine",
			Env:       map[string]string{"POSTGRES_PASSWORD": "pw"},
			Readiness: Readiness{TCPPort: 5432, Timeout: Duration(DefaultReadinessTimeout)},
			Resources: Resources{PIDs: DefaultPIDs},
		}},
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

func TestApplyDefaultsFillsSidecarsAndResources(t *testing.T) {
	s := &TaskSpec{
		Image: "alpine:3",
		Sidecars: map[string]Sidecar{
			"db":  {Image: "postgres:16-alpine", Readiness: Readiness{TCPPort: 5432}},
			"api": {Image: "alpine:3", Readiness: Readiness{HTTPPath: "/healthz"}},
			"raw": {Image: "alpine:3"},
		},
	}
	s.ApplyDefaults()

	assert.Equal(t, DefaultPIDs, s.Resources.PIDs)
	for name, sc := range s.Sidecars {
		assert.Equal(t, DefaultPIDs, sc.Resources.PIDs, "sidecar %s", name)
		assert.Equal(t, Duration(DefaultReadinessTimeout), sc.Readiness.Timeout, "sidecar %s", name)
	}
	assert.Equal(t, DefaultHTTPPort, s.Sidecars["api"].Readiness.HTTPPort)
	assert.Zero(t, s.Sidecars["db"].Readiness.HTTPPort, "an http port is only defaulted for an http probe")
	assert.Zero(t, s.Sidecars["raw"].Readiness.Probes())
}

func TestParseSidecarSpec(t *testing.T) {
	const doc = `
image: postgres:16-alpine
command: ["psql", "-h", "db", "-c", "select 1"]
env:
  PGPASSWORD: podium
sidecars:
  db:
    image: postgres:16-alpine
    env:
      POSTGRES_PASSWORD: podium
    readiness:
      tcp_port: 5432
      timeout: 30s
    resources:
      cpu: 1
      memory_mb: 256
resources:
  cpu: 0.5
  memory_mb: 128
  pids: 64
hardening:
  read_only_rootfs: true
  capabilities: [CHOWN, NET_BIND_SERVICE]
`
	got, err := ParseTaskSpec(strings.NewReader(doc))
	require.NoError(t, err)
	assert.Equal(t, Resources{CPU: 0.5, MemoryMB: 128, PIDs: 64}, got.Resources)
	assert.Equal(t, Hardening{ReadOnlyRootfs: true, Capabilities: []string{"CHOWN", "NET_BIND_SERVICE"}}, got.Hardening)
	require.Contains(t, got.Sidecars, "db")
	db := got.Sidecars["db"]
	assert.Equal(t, "postgres:16-alpine", db.Image)
	assert.Equal(t, Readiness{TCPPort: 5432, Timeout: Duration(30 * time.Second)}, db.Readiness)
	assert.Equal(t, Resources{CPU: 1, MemoryMB: 256, PIDs: DefaultPIDs}, db.Resources)
}

func TestValidateSidecarsResourcesAndHardening(t *testing.T) {
	valid := func() *TaskSpec {
		s := &TaskSpec{
			Image:    "alpine:3",
			Sidecars: map[string]Sidecar{"db": {Image: "postgres:16-alpine", Readiness: Readiness{TCPPort: 5432}}},
		}
		s.ApplyDefaults()
		return s
	}
	require.NoError(t, valid().Validate())

	rename := func(s *TaskSpec, to string) {
		sc := s.Sidecars["db"]
		delete(s.Sidecars, "db")
		s.Sidecars[to] = sc
	}
	mutateDB := func(s *TaskSpec, f func(*Sidecar)) {
		sc := s.Sidecars["db"]
		f(&sc)
		s.Sidecars["db"] = sc
	}

	for name, tc := range map[string]struct {
		mutate func(*TaskSpec)
		want   string
	}{
		"reserved sidecar name": {
			func(s *TaskSpec) { rename(s, "task") }, `sidecar name "task" is reserved`,
		},
		"sidecar name with an upper case letter": {
			func(s *TaskSpec) { rename(s, "DB") }, "is not a valid hostname label",
		},
		"sidecar name with an underscore": {
			func(s *TaskSpec) { rename(s, "my_db") }, "is not a valid hostname label",
		},
		"sidecar name with a leading dash": {
			func(s *TaskSpec) { rename(s, "-db") }, "is not a valid hostname label",
		},
		"sidecar name with a trailing dash": {
			func(s *TaskSpec) { rename(s, "db-") }, "is not a valid hostname label",
		},
		"empty sidecar name": {
			func(s *TaskSpec) { rename(s, "") }, "is not a valid hostname label",
		},
		"sidecar without an image": {
			func(s *TaskSpec) { mutateDB(s, func(sc *Sidecar) { sc.Image = "" }) }, "sidecars.db.image is required",
		},
		"sidecar env key that is not an identifier": {
			func(s *TaskSpec) {
				mutateDB(s, func(sc *Sidecar) { sc.Env = map[string]string{"NOT-OK": "1"} })
			},
			`sidecars.db.env key "NOT-OK" is not a valid shell identifier`,
		},
		"two readiness probes": {
			func(s *TaskSpec) {
				mutateDB(s, func(sc *Sidecar) { sc.Readiness.HTTPPath = "/healthz" })
			},
			"declares 2 probes",
		},
		"three readiness probes": {
			func(s *TaskSpec) {
				mutateDB(s, func(sc *Sidecar) {
					sc.Readiness.HTTPPath = "/healthz"
					sc.Readiness.Command = []string{"true"}
				})
			},
			"declares 3 probes",
		},
		"readiness port out of range": {
			func(s *TaskSpec) { mutateDB(s, func(sc *Sidecar) { sc.Readiness.TCPPort = 70000 }) },
			"is not a port number",
		},
		"http port without a path": {
			func(s *TaskSpec) {
				mutateDB(s, func(sc *Sidecar) { sc.Readiness = Readiness{HTTPPort: 8080} })
			},
			"http_port is set without http_path",
		},
		"http path that is not absolute": {
			func(s *TaskSpec) {
				mutateDB(s, func(sc *Sidecar) { sc.Readiness = Readiness{HTTPPath: "healthz"} })
			},
			"must start with /",
		},
		"negative readiness timeout": {
			func(s *TaskSpec) {
				mutateDB(s, func(sc *Sidecar) { sc.Readiness.Timeout = Duration(-time.Second) })
			},
			"readiness.timeout must not be negative",
		},
		"negative sidecar cpu": {
			func(s *TaskSpec) { mutateDB(s, func(sc *Sidecar) { sc.Resources.CPU = -1 }) },
			"sidecars.db.resources.cpu must not be negative",
		},
		"negative cpu":    {func(s *TaskSpec) { s.Resources.CPU = -0.5 }, "resources.cpu must not be negative"},
		"negative memory": {func(s *TaskSpec) { s.Resources.MemoryMB = -1 }, "resources.memory_mb must not be negative"},
		"negative pids":   {func(s *TaskSpec) { s.Resources.PIDs = -1 }, "resources.pids must not be negative"},
		"capability outside the allow-list": {
			func(s *TaskSpec) { s.Hardening.Capabilities = []string{"SYS_ADMIN"} },
			`"SYS_ADMIN" is not allowed`,
		},
		"capability that does not exist": {
			func(s *TaskSpec) { s.Hardening.Capabilities = []string{"MAKE_ME_ROOT"} },
			"see " + SpecDocs,
		},
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

func TestValidateAcceptsEveryAllowedCapabilitySpelling(t *testing.T) {
	s := &TaskSpec{Image: "alpine:3"}
	s.ApplyDefaults()
	for _, c := range AllowedCapabilities {
		for _, spelling := range []string{c, strings.ToLower(c), "CAP_" + c, "cap_" + strings.ToLower(c)} {
			s.Hardening.Capabilities = []string{spelling}
			require.NoError(t, s.Validate(), "capability %q", spelling)
			assert.Equal(t, c, NormalizeCapability(spelling))
		}
	}
}

func TestProtoRoundTripWithSidecars(t *testing.T) {
	want := &TaskSpec{
		Image:   "postgres:16-alpine",
		Command: []string{"psql", "-h", "db"},
		Sidecars: map[string]Sidecar{
			"db": {
				Image:     "postgres:16-alpine",
				Command:   []string{"postgres", "-c", "fsync=off"},
				Env:       map[string]string{"POSTGRES_PASSWORD": "pw"},
				Readiness: Readiness{TCPPort: 5432, Timeout: Duration(5 * time.Second)},
				Resources: Resources{CPU: 1, MemoryMB: 256, PIDs: 128},
			},
			"cache": {Image: "redis:7-alpine", Readiness: Readiness{Command: []string{"redis-cli", "ping"}}},
		},
		Resources: Resources{CPU: 0.5, MemoryMB: 64, PIDs: 50},
		Hardening: Hardening{ReadOnlyRootfs: true, Capabilities: []string{"CHOWN", "SETUID"}},
	}
	want.ApplyDefaults()
	require.NoError(t, want.Validate())
	assert.Equal(t, want, FromProto(want.ToProto()))
}
