package profiles

import (
	"fmt"
	"strings"
	"time"

	"github.com/podium-ade/podium/pkg/spec"
)

// RunsOnNodes is runs_on for the fleet's own machines.
const RunsOnNodes = "nodes"

// RunsOnKey is the conductor setting holding where a turn runs when its playbook does not
// say.
const RunsOnKey = "sessions.runs_on"

// RunsOn is that setting.
type RunsOn struct {
	RunsOn    string    `json:"runs_on"`
	UpdatedBy string    `json:"updated_by"`
	UpdatedAt time.Time `json:"updated_at"`
}

// PlaybookSandbox overrides a sandbox provider's settings for a playbook's turns, when they
// run on that provider.
type PlaybookSandbox struct {
	Boat  *spec.BoatSandbox  `yaml:"boat,omitempty" json:"boat,omitempty"`
	Modal *spec.ModalSandbox `yaml:"modal,omitempty" json:"modal,omitempty"`
}

// ValidateRunsOn checks a runs_on value: empty, "nodes", or a provider's name. Whether that
// provider is configured is the control plane's to say; a turn sent to one that is not
// waits in the queue with a reason.
func ValidateRunsOn(v string) error {
	if v != strings.TrimSpace(v) || strings.ContainsAny(v, "=, ") {
		return fmt.Errorf("runs_on %q is not %q or a sandbox provider's name", v, RunsOnNodes)
	}
	return nil
}

// SandboxFor is the task spec's sandbox block for a turn of s that runs on runsOn, or nil for
// one that runs on the fleet's own machines.
func (s Playbook) SandboxFor(runsOn string) *spec.Sandbox {
	if runsOn == "" || runsOn == RunsOnNodes {
		return nil
	}
	out := &spec.Sandbox{Provider: runsOn}
	if s.Sandbox != nil {
		// Copies: a turn's spec must not share anything with the loaded playbook.
		switch b, m := s.Sandbox.Boat, s.Sandbox.Modal; {
		case runsOn == "boat" && b != nil:
			out.Boat = &spec.BoatSandbox{Type: b.Type, Template: b.Template, Org: b.Org}
		case runsOn == "modal" && m != nil:
			out.Modal = &spec.ModalSandbox{Runtime: m.Runtime, Environment: m.Environment}
		}
	}
	return out
}
