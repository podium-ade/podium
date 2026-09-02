package scheduler

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/alvaroibarguen/podium/internal/server/nodes"
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
