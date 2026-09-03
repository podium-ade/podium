// Package spec defines the public task specification: the YAML/JSON document an operator
// writes and the validated Go type every Podium component codes against.
package spec

import (
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"time"

	yaml "go.yaml.in/yaml/v3"
)

// Defaults applied by ApplyDefaults.
const (
	DefaultWorkingDir       = "/workspace"
	DefaultTimeout          = time.Hour
	DefaultMaxAttempts      = 1
	DefaultPIDs             = 4096
	DefaultReadinessTimeout = time.Minute
	DefaultHTTPPort         = 80
)

// ReservedSidecarName is the network alias the task container itself answers to, so no
// sidecar may take it.
const ReservedSidecarName = "task"

// SpecDocs is where a validation error points a reader who wants the whole picture.
const SpecDocs = "docs/task-spec.md"

// AllowedCapabilities is the complete set of Linux capabilities a task may add back after
// the executor drops them all. Anything outside it is rejected at validation: a task that
// needs more is a node-level operator decision, not a spec field.
var AllowedCapabilities = []string{
	"CHOWN", "DAC_OVERRIDE", "FOWNER", "SETUID", "SETGID", "NET_BIND_SERVICE", "KILL",
}

// TaskSpec is what a task runs. It mirrors podium.v1.TaskSpec on the wire.
type TaskSpec struct {
	Image       string             `yaml:"image" json:"image"`
	Command     []string           `yaml:"command,omitempty" json:"command,omitempty"`
	WorkingDir  string             `yaml:"working_dir,omitempty" json:"working_dir,omitempty"`
	Env         map[string]string  `yaml:"env,omitempty" json:"env,omitempty"`
	Sidecars    map[string]Sidecar `yaml:"sidecars,omitempty" json:"sidecars,omitempty"`
	Resources   Resources          `yaml:"resources,omitempty" json:"resources,omitempty"`
	Hardening   Hardening          `yaml:"hardening,omitempty" json:"hardening,omitempty"`
	Labels      []string           `yaml:"labels,omitempty" json:"labels,omitempty"`
	Timeout     Duration           `yaml:"timeout,omitempty" json:"timeout,omitempty"`
	MaxAttempts int                `yaml:"max_attempts,omitempty" json:"max_attempts,omitempty"`
}

// Sidecar is a sibling container started before the task and reachable from it by the
// name it is keyed under, which becomes its DNS alias on the task network.
type Sidecar struct {
	Image     string            `yaml:"image" json:"image"`
	Command   []string          `yaml:"command,omitempty" json:"command,omitempty"`
	Env       map[string]string `yaml:"env,omitempty" json:"env,omitempty"`
	Readiness Readiness         `yaml:"readiness,omitempty" json:"readiness,omitempty"`
	Resources Resources         `yaml:"resources,omitempty" json:"resources,omitempty"`
}

// Readiness is how the node decides a sidecar is usable. At most one probe may be set; a
// sidecar with none is ready as soon as its container is running.
type Readiness struct {
	TCPPort  int      `yaml:"tcp_port,omitempty" json:"tcp_port,omitempty"`
	HTTPPath string   `yaml:"http_path,omitempty" json:"http_path,omitempty"`
	HTTPPort int      `yaml:"http_port,omitempty" json:"http_port,omitempty"`
	Command  []string `yaml:"command,omitempty" json:"command,omitempty"`
	Timeout  Duration `yaml:"timeout,omitempty" json:"timeout,omitempty"`
}

// Resources caps one container. The node applies them blindly; refusing a task that asks
// for more than a node has is the scheduler's job.
type Resources struct {
	CPU      float64 `yaml:"cpu,omitempty" json:"cpu,omitempty"`
	MemoryMB int     `yaml:"memory_mb,omitempty" json:"memory_mb,omitempty"`
	PIDs     int     `yaml:"pids,omitempty" json:"pids,omitempty"`
}

// Hardening relaxes or tightens the task container's sandbox. The defaults — every
// capability dropped, no new privileges, the engine's seccomp profile — are not
// negotiable; these two fields are the only dials.
type Hardening struct {
	ReadOnlyRootfs bool     `yaml:"read_only_rootfs,omitempty" json:"read_only_rootfs,omitempty"`
	Capabilities   []string `yaml:"capabilities,omitempty" json:"capabilities,omitempty"`
}

// Probes reports how many readiness probes are configured. Validation allows 0 or 1.
func (r Readiness) Probes() int {
	n := 0
	if r.TCPPort != 0 {
		n++
	}
	if r.HTTPPath != "" {
		n++
	}
	if len(r.Command) > 0 {
		n++
	}
	return n
}

var (
	envKeyRE      = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	sidecarNameRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
)

// ApplyDefaults fills unset fields. It never overwrites a value the caller set, so an
// invalid one (a negative timeout, say) survives for Validate to reject.
func (s *TaskSpec) ApplyDefaults() {
	if s.WorkingDir == "" {
		s.WorkingDir = DefaultWorkingDir
	}
	if s.Timeout == 0 {
		s.Timeout = Duration(DefaultTimeout)
	}
	if s.MaxAttempts == 0 {
		s.MaxAttempts = DefaultMaxAttempts
	}
	s.Resources.applyDefaults()
	for name, sc := range s.Sidecars {
		sc.Resources.applyDefaults()
		if sc.Readiness.Timeout == 0 {
			sc.Readiness.Timeout = Duration(DefaultReadinessTimeout)
		}
		if sc.Readiness.HTTPPath != "" && sc.Readiness.HTTPPort == 0 {
			sc.Readiness.HTTPPort = DefaultHTTPPort
		}
		s.Sidecars[name] = sc
	}
}

func (r *Resources) applyDefaults() {
	if r.PIDs == 0 {
		r.PIDs = DefaultPIDs
	}
}

// Validate reports every problem with the spec at once.
func (s *TaskSpec) Validate() error {
	var errs []error
	if strings.TrimSpace(s.Image) == "" {
		errs = append(errs, errors.New("image is required"))
	}
	if s.Timeout <= 0 {
		errs = append(errs, fmt.Errorf("timeout must be positive, got %s", s.Timeout))
	}
	if s.MaxAttempts < 1 {
		errs = append(errs, fmt.Errorf("max_attempts must be at least 1, got %d", s.MaxAttempts))
	}
	errs = append(errs, validateEnv("env", s.Env)...)
	for _, l := range s.Labels {
		if strings.TrimSpace(l) == "" {
			errs = append(errs, errors.New("label must not be empty"))
		}
	}
	errs = append(errs, s.Resources.validate("resources")...)
	errs = append(errs, s.Hardening.validate()...)
	errs = append(errs, s.validateSidecars()...)
	return errors.Join(errs...)
}

func (s *TaskSpec) validateSidecars() []error {
	var errs []error
	for _, name := range sortedKeys(s.Sidecars) {
		sc := s.Sidecars[name]
		field := "sidecars." + name
		switch {
		case name == ReservedSidecarName:
			errs = append(errs, fmt.Errorf(
				"sidecar name %q is reserved for the task container itself", ReservedSidecarName))
		case !sidecarNameRE.MatchString(name):
			errs = append(errs, fmt.Errorf(
				"sidecar name %q is not a valid hostname label (%s); it becomes the sidecar's DNS name on the task network",
				name, sidecarNameRE.String()))
		}
		if strings.TrimSpace(sc.Image) == "" {
			errs = append(errs, fmt.Errorf("%s.image is required", field))
		}
		errs = append(errs, validateEnv(field+".env", sc.Env)...)
		errs = append(errs, sc.Resources.validate(field+".resources")...)
		errs = append(errs, sc.Readiness.validate(field+".readiness")...)
	}
	return errs
}

func (r Resources) validate(field string) []error {
	var errs []error
	if r.CPU < 0 {
		errs = append(errs, fmt.Errorf("%s.cpu must not be negative, got %g", field, r.CPU))
	}
	if r.MemoryMB < 0 {
		errs = append(errs, fmt.Errorf("%s.memory_mb must not be negative, got %d", field, r.MemoryMB))
	}
	if r.PIDs < 0 {
		errs = append(errs, fmt.Errorf("%s.pids must not be negative, got %d", field, r.PIDs))
	}
	return errs
}

func (r Readiness) validate(field string) []error {
	var errs []error
	if n := r.Probes(); n > 1 {
		errs = append(errs, fmt.Errorf(
			"%s declares %d probes; set exactly one of tcp_port, http_path or command, or none at all", field, n))
	}
	if r.TCPPort < 0 || r.TCPPort > 65535 {
		errs = append(errs, fmt.Errorf("%s.tcp_port %d is not a port number", field, r.TCPPort))
	}
	if r.HTTPPort < 0 || r.HTTPPort > 65535 {
		errs = append(errs, fmt.Errorf("%s.http_port %d is not a port number", field, r.HTTPPort))
	}
	if r.HTTPPort != 0 && r.HTTPPath == "" {
		errs = append(errs, fmt.Errorf("%s.http_port is set without http_path", field))
	}
	if r.HTTPPath != "" && !strings.HasPrefix(r.HTTPPath, "/") {
		errs = append(errs, fmt.Errorf("%s.http_path %q must start with /", field, r.HTTPPath))
	}
	if r.Timeout < 0 {
		errs = append(errs, fmt.Errorf("%s.timeout must not be negative, got %s", field, r.Timeout))
	}
	return errs
}

func (h Hardening) validate() []error {
	var errs []error
	for _, c := range h.Capabilities {
		if !capabilityAllowed(c) {
			errs = append(errs, fmt.Errorf(
				"hardening.capabilities: %q is not allowed; podium permits only %s (see %s)",
				c, strings.Join(AllowedCapabilities, ", "), SpecDocs))
		}
	}
	return errs
}

// NormalizeCapability is the spelling the executor passes to Docker: uppercase, with the
// optional CAP_ prefix stripped, so both "chown" and "CAP_CHOWN" name the same capability.
func NormalizeCapability(c string) string {
	return strings.TrimPrefix(strings.ToUpper(strings.TrimSpace(c)), "CAP_")
}

func capabilityAllowed(c string) bool {
	n := NormalizeCapability(c)
	for _, allowed := range AllowedCapabilities {
		if n == allowed {
			return true
		}
	}
	return false
}

func validateEnv(field string, env map[string]string) []error {
	var errs []error
	for _, k := range sortedKeys(env) {
		if k == "" {
			errs = append(errs, fmt.Errorf("%s key must not be empty", field))
			continue
		}
		if !envKeyRE.MatchString(k) {
			errs = append(errs, fmt.Errorf("%s key %q is not a valid shell identifier", field, k))
		}
	}
	return errs
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// ParseTaskSpec decodes a YAML task spec, applies defaults and validates it.
func ParseTaskSpec(r io.Reader) (*TaskSpec, error) {
	dec := yaml.NewDecoder(r)
	dec.KnownFields(true)
	var s TaskSpec
	if err := dec.Decode(&s); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, errors.New("parse task spec: empty document")
		}
		return nil, fmt.Errorf("parse task spec: %w", err)
	}
	s.ApplyDefaults()
	if err := s.Validate(); err != nil {
		return nil, fmt.Errorf("invalid task spec: %w", err)
	}
	return &s, nil
}
