package node

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// The daemon's own slot arithmetic. A node enforces its own budget — handleAssign rejects an
// assignment when freeSlots is 0 — so this is what makes an override from the control plane
// mean anything at all.
func TestSetSlotsOverridesTheConfiguredMaxTasks(t *testing.T) {
	n := &Node{
		cfg:      Config{MaxTasks: 4},
		tasks:    map[string]*buffer{"t1": nil, "t2": nil},
		maxTasks: 4,
	}
	require.EqualValues(t, 2, n.freeSlots())

	inForce, changed := n.setSlots(8)
	require.Equal(t, 8, inForce)
	require.True(t, changed)
	require.EqualValues(t, 6, n.freeSlots(), "a raise reaches the node, or it would refuse the work")

	// Below what is already running: nothing is taken down, and the count does not go negative.
	inForce, changed = n.setSlots(1)
	require.Equal(t, 1, inForce)
	require.True(t, changed)
	require.EqualValues(t, 0, n.freeSlots())
	require.Len(t, n.tasks, 2, "lowering the budget stops new work; it does not stop running work")

	// 0 is the whole of how the daemon is handed back to its own configuration.
	inForce, changed = n.setSlots(0)
	require.Equal(t, 4, inForce)
	require.True(t, changed)
	require.EqualValues(t, 2, n.freeSlots())

	// Every stream carries a Slots, so the no-op case has to be recognisable: it is the only
	// thing that keeps a reconnect from logging a change that did not happen.
	_, changed = n.setSlots(0)
	require.False(t, changed)
}

// A drained node advertises nothing whatever it is told its budget is. A slot count is not a
// way to undo a drain.
func TestSetSlotsDoesNotUndrainTheDaemon(t *testing.T) {
	n := &Node{
		cfg:      Config{MaxTasks: 4},
		tasks:    map[string]*buffer{},
		maxTasks: 4,
		draining: true,
	}
	n.setSlots(8)
	require.EqualValues(t, 0, n.freeSlots())
}
