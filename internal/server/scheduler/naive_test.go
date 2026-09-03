package scheduler

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/alvaroibarguen/podium/internal/server/nodes"
	"github.com/alvaroibarguen/podium/internal/server/secrets"
	"github.com/alvaroibarguen/podium/internal/server/store"
	"github.com/alvaroibarguen/podium/pkg/spec"
)

func TestPickRequiresEverySpecLabel(t *testing.T) {
	candidates := []nodes.Snapshot{
		{NodeID: "node_a", Labels: []string{"linux/arm64"}, FreeSlots: 4},
		{NodeID: "node_b", Labels: []string{"linux/arm64", "gpu"}, FreeSlots: 1},
	}

	require.Equal(t, 1, pick(candidates, []string{"gpu"}))
	require.Equal(t, 1, pick(candidates, []string{"gpu", "linux/arm64"}))
	require.Equal(t, 0, pick(candidates, nil), "no labels: most free slots wins")
	require.Equal(t, -1, pick(candidates, []string{"fpga"}))
}

func TestPickSkipsFullAndTieBreaksOnNodeID(t *testing.T) {
	require.Equal(t, -1, pick([]nodes.Snapshot{{NodeID: "node_a", FreeSlots: 0}}, nil))

	tied := []nodes.Snapshot{
		{NodeID: "node_b", FreeSlots: 2},
		{NodeID: "node_a", FreeSlots: 2},
	}
	require.Equal(t, 1, pick(tied, nil))
}

func TestHasAll(t *testing.T) {
	require.True(t, hasAll([]string{"a", "b"}, []string{"a"}))
	require.True(t, hasAll(nil, nil))
	require.False(t, hasAll(nil, []string{"a"}))
}

func TestResolvedToProtoCopiesValues(t *testing.T) {
	value := []byte("hunter2-hunter2")
	in := []secrets.Resolved{{Name: "DB_PASSWORD", Target: "env", Key: "DB_PASSWORD", Value: value}}

	got := resolvedToProto(in)
	require.Len(t, got, 1)
	require.Equal(t, "DB_PASSWORD", got[0].GetName())
	require.Equal(t, "env", got[0].GetTarget())
	require.Equal(t, value, got[0].GetValue())

	// The wire copy must not alias the resolver's slice: dispatch zeroes that one the
	// moment the assignment has been pushed.
	secrets.Zero(value)
	require.Equal(t, []byte("hunter2-hunter2"), got[0].GetValue())

	require.Nil(t, resolvedToProto(nil))
}

// A Naive with no resolver runs ordinary tasks and refuses ones that need secrets, rather
// than assigning them with the field silently empty.
func TestResolveWithoutAResolver(t *testing.T) {
	n := NewNaive(nil, nil, nil, nil)

	got, err := n.resolve(context.Background(), store.Task{Spec: spec.TaskSpec{Image: "alpine:3"}})
	require.NoError(t, err)
	require.Nil(t, got)

	_, err = n.resolve(context.Background(), store.Task{Spec: spec.TaskSpec{
		Image:   "alpine:3",
		Secrets: []spec.SecretRef{{Name: "A", Target: "env", Key: "A"}},
	}})
	require.ErrorIs(t, err, secrets.ErrNoKey)
}
