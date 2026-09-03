package podiumv1

import (
	"bytes"
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRedactForLogClones(t *testing.T) {
	assert.Nil(t, RedactForLog(nil))

	in := &Assign{TaskId: "task_1", LeaseId: "lease_1", Spec: &TaskSpec{Image: "alpine:3"}}
	got := RedactForLog(in)
	require.NotNil(t, got)
	assert.NotSame(t, in, got)
	assert.NotSame(t, in.GetSpec(), got.GetSpec())
	assert.Equal(t, "task_1", got.GetTaskId())
	assert.Equal(t, "alpine:3", got.GetSpec().GetImage())

	got.Spec.Image = "mutated"
	assert.Equal(t, "alpine:3", in.GetSpec().GetImage())
}

func TestRedactForLogClearsResolvedSecrets(t *testing.T) {
	in := &Assign{
		TaskId: "task_1",
		Spec:   &TaskSpec{Image: "alpine:3"},
		ResolvedSecrets: []*ResolvedSecret{
			{Name: "GREETING", Target: "env", Key: "GREETING", Value: []byte("hunter2-hunter2")},
		},
	}
	got := RedactForLog(in)
	require.NotNil(t, got)
	assert.Empty(t, got.GetResolvedSecrets(), "the values must not survive the clone")
	assert.Len(t, in.GetResolvedSecrets(), 1, "the original is untouched")
	assert.Equal(t, "task_1", got.GetTaskId(), "everything an operator needs survives")
	assert.Equal(t, "alpine:3", got.GetSpec().GetImage())
}

// TestNoSecretValueSurvivesADebugLog is the acceptance item: log an Assign carrying a known
// secret at debug level, through the helper every log site in the tree is required to use,
// and the value must not be anywhere in the captured output.
func TestNoSecretValueSurvivesADebugLog(t *testing.T) {
	const value = "correct-horse-battery-staple"

	a := &Assign{
		TaskId:  "task_01abc",
		LeaseId: "lease_01abc",
		Spec: &TaskSpec{
			Image:   "alpine:3",
			Secrets: []*SecretRef{{Name: "DB_PASSWORD", Target: "env", Key: "DB_PASSWORD"}},
		},
		ResolvedSecrets: []*ResolvedSecret{
			{Name: "DB_PASSWORD", Target: "env", Key: "DB_PASSWORD", Value: []byte(value)},
		},
	}

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	logger.DebugContext(context.Background(), "pushing assignment", "assign", RedactForLog(a))

	out := buf.String()
	require.NotEmpty(t, out, "the handler must actually have logged at debug level")
	assert.NotContains(t, out, value, "the secret value reached the log")
	assert.Contains(t, out, "task_01abc", "the assignment itself is still loggable")

	// The unredacted form is what the helper is protecting against; prove the detector
	// above would have caught it.
	var leak bytes.Buffer
	slog.New(slog.NewTextHandler(&leak, &slog.HandlerOptions{Level: slog.LevelDebug})).
		DebugContext(context.Background(), "pushing assignment", "assign", a)
	assert.Contains(t, leak.String(), value, "without RedactForLog the value really does leak")
}
