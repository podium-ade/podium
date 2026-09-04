package conductor

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alvaroibarguen/podium/internal/agent/store"
	podiumv1 "github.com/alvaroibarguen/podium/internal/proto/podium/v1"
)

func task(status podiumv1.TaskStatus, reason string, exit *int32) *podiumv1.Task {
	return &podiumv1.Task{Id: "task_01", Status: status, FailureReason: reason, ExitCode: exit}
}

func exitCode(v int32) *int32 { return &v }

// The whole status table from the step file, plus the property that matters more than any
// single row: the raw failure_reason is never in the text a human sees.
func TestClassify(t *testing.T) {
	tests := []struct {
		name       string
		task       *podiumv1.Task
		wantStatus string
		wantPost   []string
	}{
		{
			name:       "succeeded says nothing beyond the final",
			task:       task(podiumv1.TaskStatus_TASK_STATUS_SUCCEEDED, "", exitCode(0)),
			wantStatus: store.TurnSucceeded,
		},
		{
			name:       "exit 3 is the runtime running out of turns",
			task:       task(podiumv1.TaskStatus_TASK_STATUS_FAILED, "", exitCode(3)),
			wantStatus: store.TurnFailed,
			wantPost:   []string{"ran out of turns", "task_01"},
		},
		{
			name:       "a timeout is its own turn status",
			task:       task(podiumv1.TaskStatus_TASK_STATUS_FAILED, "timeout", nil),
			wantStatus: store.TurnTimeout,
			wantPost:   []string{"15m0s limit", "task_01"},
		},
		{
			name: "a missing secret names the credential and nothing else",
			task: task(podiumv1.TaskStatus_TASK_STATUS_FAILED,
				`secrets: missing secret "podium.agent.github_token"`, nil),
			wantStatus: store.TurnFailed,
			wantPost:   []string{"missing a credential", "podium.agent.github_token", "operator"},
		},
		{
			name: "two missing secrets are both named",
			task: task(podiumv1.TaskStatus_TASK_STATUS_FAILED,
				"secrets: missing secret podium.agent.github_token, podium.agent.warehouse_url", nil),
			wantStatus: store.TurnFailed,
			wantPost:   []string{"podium.agent.github_token, podium.agent.warehouse_url"},
		},
		{
			name: "anything else is a generic apology",
			task: task(podiumv1.TaskStatus_TASK_STATUS_FAILED,
				"oom: the container was killed at 0x7ffd in /secret/path", exitCode(137)),
			wantStatus: store.TurnFailed,
			wantPost:   []string{"Something went wrong on my side", "task_01"},
		},
		{
			name:       "a lost node says it did not retry",
			task:       task(podiumv1.TaskStatus_TASK_STATUS_LOST, "node went offline", nil),
			wantStatus: store.TurnLost,
			wantPost:   []string{"went away", "did not retry", "task_01"},
		},
		{
			name:       "a cancelled turn says so",
			task:       task(podiumv1.TaskStatus_TASK_STATUS_CANCELLED, "cancelled by alvaro", nil),
			wantStatus: store.TurnCancelled,
			wantPost:   []string{"cancelled", "task_01"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := classify(tc.task, 15*time.Minute)
			assert.Equal(t, tc.wantStatus, got.Status)
			if len(tc.wantPost) == 0 {
				assert.Empty(t, got.Post)
				return
			}
			for _, want := range tc.wantPost {
				assert.Contains(t, got.Post, want)
			}
			if reason := tc.task.GetFailureReason(); reason != "" && tc.wantStatus != store.TurnCancelled {
				assert.NotContains(t, got.Post, reason,
					"the raw failure_reason must never reach a human; it goes to the log")
			}
		})
	}
}

// Every status classify can produce has to be one the turns.status check constraint allows.
func TestEveryClassifiedStatusIsAllowedByTheSchema(t *testing.T) {
	allowed := map[string]bool{
		store.TurnRunning: true, store.TurnSucceeded: true, store.TurnFailed: true,
		store.TurnLost: true, store.TurnCancelled: true, store.TurnTimeout: true,
	}
	for _, status := range []podiumv1.TaskStatus{
		podiumv1.TaskStatus_TASK_STATUS_SUCCEEDED,
		podiumv1.TaskStatus_TASK_STATUS_FAILED,
		podiumv1.TaskStatus_TASK_STATUS_LOST,
		podiumv1.TaskStatus_TASK_STATUS_CANCELLED,
		podiumv1.TaskStatus_TASK_STATUS_UNSPECIFIED,
	} {
		got := classify(task(status, "", nil), time.Minute)
		require.True(t, allowed[got.Status], "%s classified as %q, which the schema refuses",
			status, got.Status)
	}
}

func TestMissingSecretsOnlyFiresForAMissingSecret(t *testing.T) {
	assert.Empty(t, missingSecrets("timeout"))
	assert.Empty(t, missingSecrets(""))
	assert.Empty(t, missingSecrets("the secret was fine"))
	assert.Equal(t, "a", missingSecrets(`secrets: missing secret "a"`))
}
