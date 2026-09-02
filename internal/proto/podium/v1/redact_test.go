package podiumv1

import (
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
