package conductor

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/podium-ade/podium/internal/agent/store"
	podiumv1 "github.com/podium-ade/podium/internal/proto/podium/v1"
)

// runtimeExitMaxTurns is the agent runtime's exit code for "I ran out of turns" (step 16).
const runtimeExitMaxTurns = 3

// reasonTimeout is the failure_reason podium-server writes for a task that outran its
// spec's timeout. It is a literal because internal/server may not be imported here.
const reasonTimeout = "timeout"

// missingSecretRE pulls the secret names out of the control plane's
// `secrets: missing secret "a", "b"` failure reason. A name is not sensitive — it is the
// name of a value — so it is the one part of a failure_reason a human ever sees.
var missingSecretRE = regexp.MustCompile(`missing secret\s+(.*)$`)

// outcome is how a turn ended: the status to record and, when something went wrong, the
// plain-words line to post. The raw failure_reason is never in Post — it goes to the log.
type outcome struct {
	Status string
	Post   string
}

// classify maps a terminal task onto a turn status and a human sentence. It reads only the
// task's own fields; nothing a task *said* influences it.
func classify(task *podiumv1.Task, timeout time.Duration) outcome {
	id := task.GetId()
	switch task.GetStatus() {
	case podiumv1.TaskStatus_TASK_STATUS_SUCCEEDED:
		return outcome{Status: store.TurnSucceeded}
	case podiumv1.TaskStatus_TASK_STATUS_LOST:
		return outcome{
			Status: store.TurnLost,
			Post:   fmt.Sprintf("The machine running this went away. Task `%s`. I did not retry.", id),
		}
	case podiumv1.TaskStatus_TASK_STATUS_CANCELLED:
		return outcome{
			Status: store.TurnCancelled,
			Post:   fmt.Sprintf("This was cancelled. Task `%s`.", id),
		}
	}

	reason := task.GetFailureReason()
	if task.GetExitCode() == runtimeExitMaxTurns {
		return outcome{
			Status: store.TurnFailed,
			Post:   fmt.Sprintf("I ran out of turns before finishing. Task `%s`.", id),
		}
	}
	if reason == reasonTimeout {
		return outcome{
			Status: store.TurnTimeout,
			Post:   fmt.Sprintf("I hit the %s limit for this playbook. Task `%s`.", timeout, id),
		}
	}
	if names := missingSecrets(reason); names != "" {
		return outcome{
			Status: store.TurnFailed,
			Post: fmt.Sprintf("This bot is missing a credential (`%s`). An operator needs to set it. Task `%s`.",
				names, id),
		}
	}
	return outcome{
		Status: store.TurnFailed,
		Post:   fmt.Sprintf("Something went wrong on my side. Task `%s`.", id),
	}
}

// missingSecrets returns the names a "missing secret" failure named, or "" when the reason
// is about something else.
func missingSecrets(reason string) string {
	m := missingSecretRE.FindStringSubmatch(reason)
	if m == nil {
		return ""
	}
	names := strings.FieldsFunc(m[1], func(r rune) bool { return r == ',' })
	for i, n := range names {
		names[i] = strings.Trim(strings.TrimSpace(n), `"`)
	}
	return strings.Join(names, ", ")
}
