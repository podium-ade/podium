package nodes

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/server/store"
)

// A session's labels are copied off the node's row when the stream opens, so relabelling a
// connected node has to reach the session too — the snapshot is what the scheduler matches on.
func TestSetLabelsRetagsALiveSession(t *testing.T) {
	r := NewRegistry()
	sess := newSession("n1", []string{"linux/amd64"}, store.NodeCapacity{MaxTasks: 4}, 0, nil, false)
	r.add(sess)

	require.True(t, r.SetLabels("n1", []string{"linux/amd64", "monorepo"}))
	snap, ok := r.SnapshotOf("n1")
	require.True(t, ok)
	require.Equal(t, []string{"linux/amd64", "monorepo"}, snap.Labels)

	// The snapshot is a copy: writing through it must not reach the session.
	snap.Labels[0] = "mutated"
	again, _ := r.SnapshotOf("n1")
	require.Equal(t, []string{"linux/amd64", "monorepo"}, again.Labels)

	require.False(t, r.SetLabels("n2", []string{"monorepo"}), "a node with no session needs nothing")
}
