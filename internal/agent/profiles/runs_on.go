package profiles

import (
	"fmt"
	"strings"
	"time"

	"github.com/podium-ade/podium/pkg/spec"
)

// RunsOnNodes is runs_on for the fleet's own machines.
const RunsOnNodes = "nodes"

// RunsOnHost is where the assistant runs when it is a child of the conductor itself. It is
// the assistant's alone: a playbook always runs in a task, so "host" is never a playbook's
// runs_on.
const RunsOnHost = "host"

// RunsOnKey is the conductor setting holding where a turn runs when its playbook does not
// say.
const RunsOnKey = "sessions.runs_on"

// AssistantRunsOnKey is the conductor setting holding where the ASSISTANT runs: "host", "nodes"
// or a sandbox provider's name. It is a key of its own, and not RunsOnKey, so that moving the
// playbooks to a provider never moves the assistant as a side effect: the assistant is the
// thing a person is waiting on, and where it runs decides how fast the first word arrives.
const AssistantRunsOnKey = "assistant.runs_on"

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
	// The conductor's own process is the assistant's placement and nobody else's: a playbook
	// is a container job, and "host" here would send it to a provider of that name.
	if v == RunsOnHost {
		return fmt.Errorf("runs_on %q is the assistant's alone: a playbook runs on %q or a sandbox provider", v, RunsOnNodes)
	}
	return nil
}

// ValidateAssistantRunsOn checks where the assistant runs: everything a playbook may say,
// and "host" as well.
func ValidateAssistantRunsOn(v string) error {
	if v == RunsOnHost {
		return nil
	}
	if err := ValidateRunsOn(v); err != nil {
		return fmt.Errorf("assistant runs_on %q is not %q, %q or a sandbox provider's name", v, RunsOnHost, RunsOnNodes)
	}
	return nil
}

// SandboxBlock is the task spec's sandbox block for a turn that runs on runsOn with no
// per-provider overrides, or nil for the fleet's own machines.
func SandboxBlock(runsOn string) *spec.Sandbox {
	return Playbook{}.SandboxFor(runsOn)
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
