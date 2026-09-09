package nodes

import (
	"testing"

	"github.com/stretchr/testify/require"

	podiumv1 "github.com/alvaroibarguen/podium/internal/proto/podium/v1"
	"github.com/alvaroibarguen/podium/internal/server/store"
)

// The session's slot arithmetic, which is what the scheduler reads. It has two inputs that
// disagree on purpose — the node's advertised max_tasks and the operator's override — and
// every number below is about which of them wins where.
func TestSessionSlotBudget(t *testing.T) {
	capacity := store.NodeCapacity{MaxTasks: 4, CPUCores: 8, MemoryMB: 16384}

	t.Run("no override budgets against what the node advertised", func(t *testing.T) {
		s := newSession("n1", nil, capacity, 0, []string{"t1"}, false)
		require.EqualValues(t, 3, s.snapshot().FreeSlots)
		require.EqualValues(t, 4, s.snapshot().Capacity.MaxTasks)
	})

	t.Run("an override above the advertised number gives slots the node did not offer", func(t *testing.T) {
		s := newSession("n1", nil, capacity, 8, []string{"t1"}, false)
		require.EqualValues(t, 7, s.snapshot().FreeSlots)
		require.EqualValues(t, 8, s.snapshot().Capacity.MaxTasks,
			"a snapshot reports the budget: everything that reads one asks how much work the node takes")
	})

	t.Run("an override below what is already running frees nothing rather than going negative", func(t *testing.T) {
		s := newSession("n1", nil, capacity, 1, []string{"t1", "t2", "t3"}, false)
		require.EqualValues(t, 0, s.snapshot().FreeSlots)
	})

	t.Run("a draining node advertises nothing whatever its budget", func(t *testing.T) {
		s := newSession("n1", nil, capacity, 8, nil, true)
		require.EqualValues(t, 0, s.snapshot().FreeSlots)
	})
}

// setMaxTasks is what the admin API calls, and it has to move the free count now: the node's
// next heartbeat is up to ten seconds away, and both a raise nothing acts on and a cut the
// next tick ignores are wrong.
func TestSetMaxTasksMovesTheFreeCountImmediately(t *testing.T) {
	capacity := store.NodeCapacity{MaxTasks: 4}
	s := newSession("n1", nil, capacity, 0, []string{"t1"}, false)
	require.EqualValues(t, 3, s.snapshot().FreeSlots)

	s.setMaxTasks(8)
	require.EqualValues(t, 7, s.snapshot().FreeSlots)

	s.setMaxTasks(2)
	require.EqualValues(t, 1, s.snapshot().FreeSlots)

	// 0 is the whole of how a node is handed back to its own configuration.
	s.setMaxTasks(0)
	require.EqualValues(t, 3, s.snapshot().FreeSlots)
	require.EqualValues(t, 4, s.snapshot().Capacity.MaxTasks)
}

// A drained node stays at zero free slots when its budget changes: the drain is the standing
// instruction, and a slot count is not a way to undo one.
func TestSetMaxTasksDoesNotUndrainANode(t *testing.T) {
	s := newSession("n1", nil, store.NodeCapacity{MaxTasks: 4}, 0, nil, true)
	s.setMaxTasks(8)
	require.EqualValues(t, 0, s.snapshot().FreeSlots)

	// Undraining then applies the number that was set while it was out of the pool.
	s.setDraining(false)
	require.EqualValues(t, 8, s.snapshot().FreeSlots)
}

// A heartbeat may correct a max_tasks the node under-reported, but it must never overwrite an
// override: a node capped at two while four tasks finish would otherwise have the cap raised
// back out from under it by its own load.
func TestHeartbeatDoesNotGrowAnOverride(t *testing.T) {
	beat := func(running int32) *podiumv1.Heartbeat {
		return &podiumv1.Heartbeat{
			Load:      &podiumv1.NodeLoad{RunningTasks: running},
			FreeSlots: 0,
		}
	}

	uncapped := newSession("n1", nil, store.NodeCapacity{MaxTasks: 2}, 0, nil, false)
	uncapped.observeHeartbeat(beat(5))
	require.EqualValues(t, 5, uncapped.snapshot().Capacity.MaxTasks,
		"a node running five tasks has told us its advertised two is wrong")

	capped := newSession("n1", nil, store.NodeCapacity{MaxTasks: 4}, 2, nil, false)
	capped.observeHeartbeat(beat(4))
	require.EqualValues(t, 2, capped.snapshot().Capacity.MaxTasks)
	require.EqualValues(t, 0, capped.snapshot().FreeSlots)
}

// A heartbeat computed before the node applied a lower cap must not be taken at face value:
// the scheduler would assign above the cap for an interval, and the node would reject it.
func TestHeartbeatCannotReportMoreFreeSlotsThanACap(t *testing.T) {
	stale := &podiumv1.Heartbeat{
		Load:      &podiumv1.NodeLoad{RunningTasks: 1},
		FreeSlots: 3, // what a max_tasks of 4 with one task running says
	}

	capped := newSession("n1", nil, store.NodeCapacity{MaxTasks: 4}, 2, []string{"t1"}, false)
	capped.observeHeartbeat(stale)
	require.EqualValues(t, 1, capped.snapshot().FreeSlots, "2 slots, 1 held")

	// With no cap the node's own number is authoritative, exactly as before.
	uncapped := newSession("n1", nil, store.NodeCapacity{MaxTasks: 4}, 0, []string{"t1"}, false)
	uncapped.observeHeartbeat(stale)
	require.EqualValues(t, 3, uncapped.snapshot().FreeSlots)
}

func TestOverrideOf(t *testing.T) {
	require.EqualValues(t, 0, overrideOf(store.Node{}))
	eight := int32(8)
	require.EqualValues(t, 8, overrideOf(store.Node{MaxTasksOverride: &eight}))
}
