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
	DefaultWorkingDir  = "/workspace"
	DefaultTimeout     = time.Hour
	DefaultMaxAttempts = 1
)

// TaskSpec is what a task runs. It mirrors podium.v1.TaskSpec on the wire.
type TaskSpec struct {
	Image       string            `yaml:"image" json:"image"`
	Command     []string          `yaml:"command,omitempty" json:"command,omitempty"`
	WorkingDir  string            `yaml:"working_dir,omitempty" json:"working_dir,omitempty"`
	Env         map[string]string `yaml:"env,omitempty" json:"env,omitempty"`
	Labels      []string          `yaml:"labels,omitempty" json:"labels,omitempty"`
	Timeout     Duration          `yaml:"timeout,omitempty" json:"timeout,omitempty"`
	MaxAttempts int               `yaml:"max_attempts,omitempty" json:"max_attempts,omitempty"`
}

var envKeyRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

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
	keys := make([]string, 0, len(s.Env))
	for k := range s.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if k == "" {
			errs = append(errs, errors.New("env key must not be empty"))
			continue
		}
		if !envKeyRE.MatchString(k) {
			errs = append(errs, fmt.Errorf("env key %q is not a valid shell identifier", k))
		}
	}
	for _, l := range s.Labels {
		if strings.TrimSpace(l) == "" {
			errs = append(errs, errors.New("label must not be empty"))
		}
	}
	return errors.Join(errs...)
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
