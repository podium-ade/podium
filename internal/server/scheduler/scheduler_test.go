package scheduler

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alvaroibarguen/podium/internal/server/nodes"
	"github.com/alvaroibarguen/podium/internal/server/store"
	"github.com/alvaroibarguen/podium/pkg/spec"
)

func node(id string, slots int32, labels ...string) nodes.Snapshot {
	return nodes.Snapshot{
		NodeID:    id,
		Labels:    labels,
		FreeSlots: slots,
	}
}

func withCapacity(s nodes.Snapshot, cores int32, memMB int64, freeCPU float64, freeMem int64) nodes.Snapshot {
	s.Capacity = store.NodeCapacity{MaxTasks: 4, CPUCores: cores, MemoryMB: memMB}
	s.FreeCPU = freeCPU
	s.FreeMemoryMB = freeMem
	return s
}

func TestPickPrefersTheNodeWithTheMostFreeSlots(t *testing.T) {
	cands := []nodes.Snapshot{node("node_a", 1), node("node_b", 3), node("node_c", 2)}
	i := pick(cands, spec.TaskSpec{}, nodes.TaskCost{})
	require.GreaterOrEqual(t, i, 0)
	assert.Equal(t, "node_b", cands[i].NodeID)
}

func TestPickBreaksTiesOnLeastRecentlyAssigned(t *testing.T) {
	now := time.Now().UTC()
	a := node("node_a", 2)
	a.LastAssignedAt = now
	b := node("node_b", 2)
	b.LastAssignedAt = now.Add(-time.Minute)

	// node_a sorts first by ID, so a naive tie break would pick it; the older assignment
	// has to win, or one node takes every task in a fleet of identical machines.
	i := pick([]nodes.Snapshot{a, b}, spec.TaskSpec{}, nodes.TaskCost{})
	require.GreaterOrEqual(t, i, 0)
	assert.Equal(t, "node_b", []nodes.Snapshot{a, b}[i].NodeID)
}

func TestPickRequiresEveryLabel(t *testing.T) {
	plain := node("node_a", 4)
	browser := node("node_b", 1, "browser", "linux/arm64")
	cands := []nodes.Snapshot{plain, browser}

	i := pick(cands, spec.TaskSpec{Labels: []string{"browser"}}, nodes.TaskCost{})
	require.GreaterOrEqual(t, i, 0)
	assert.Equal(t, "node_b", cands[i].NodeID, "the unlabelled node has more slots and must still lose")

	// A label nobody carries is unschedulable, however idle the fleet is.
	assert.Equal(t, -1, pick(cands, spec.TaskSpec{Labels: []string{"gpu"}}, nodes.TaskCost{}))
}

func TestPickSkipsDrainingNodes(t *testing.T) {
	draining := node("node_a", 4)
	draining.Draining = true
	cands := []nodes.Snapshot{draining, node("node_b", 1)}

	i := pick(cands, spec.TaskSpec{}, nodes.TaskCost{})
	require.GreaterOrEqual(t, i, 0)
	assert.Equal(t, "node_b", cands[i].NodeID)

	assert.Equal(t, -1, pick([]nodes.Snapshot{draining}, spec.TaskSpec{}, nodes.TaskCost{}))
}

func TestPickRefusesATaskBiggerThanTheNode(t *testing.T) {
	small := withCapacity(node("node_a", 4), 4, 8192, 4, 8192)
	cost := nodes.TaskCost{CPU: 8}
	assert.Equal(t, -1, pick([]nodes.Snapshot{small}, spec.TaskSpec{}, cost),
		"a task asking for 8 cores must not land on a 4-core node")

	big := withCapacity(node("node_b", 1), 16, 65536, 16, 65536)
	i := pick([]nodes.Snapshot{small, big}, spec.TaskSpec{}, cost)
	require.GreaterOrEqual(t, i, 0)
	assert.Equal(t, "node_b", []nodes.Snapshot{small, big}[i].NodeID)
}

func TestPickTreatsUnmeasuredCapacityAsNoConstraint(t *testing.T) {
	// A node whose capacity probe failed reports zero cores. Refusing every task on it
	// would be a worse failure than over-committing it once.
	unknown := node("node_a", 4)
	i := pick([]nodes.Snapshot{unknown}, spec.TaskSpec{}, nodes.TaskCost{CPU: 64, MemoryMB: 1 << 20})
	assert.Equal(t, 0, i)
}

func TestCostIncludesSidecars(t *testing.T) {
	ts := spec.TaskSpec{
		Resources: spec.Resources{CPU: 1, MemoryMB: 512},
		Sidecars: map[string]spec.Sidecar{
			"db":    {Image: "pgvector/pgvector:pg16", Resources: spec.Resources{CPU: 2, MemoryMB: 1024}},
			"cache": {Image: "redis:7-alpine", Resources: spec.Resources{CPU: 0.5, MemoryMB: 256}},
		},
	}
	cost := nodes.CostOf(ts)
	assert.InDelta(t, 3.5, cost.CPU, 0.0001)
	assert.EqualValues(t, 1792, cost.MemoryMB)
}

func TestChargeStopsOneTickOverfillingANode(t *testing.T) {
	c := withCapacity(node("node_a", 2), 8, 8192, 8, 8192)
	cost := nodes.TaskCost{CPU: 3, MemoryMB: 4096}

	charge(&c, cost)
	assert.EqualValues(t, 1, c.FreeSlots)
	assert.InDelta(t, 5, c.FreeCPU, 0.0001)
	assert.EqualValues(t, 4096, c.FreeMemoryMB)

	charge(&c, cost)
	assert.EqualValues(t, 0, c.FreeSlots)
	assert.Equal(t, -1, pick([]nodes.Snapshot{c}, spec.TaskSpec{}, cost))
}

func TestPlacementReasonNamesTheConstraintThatEmptiedTheSet(t *testing.T) {
	full := node("node_a", 0)
	draining := node("node_b", 4)
	draining.Draining = true
	labelled := node("node_c", 4, "browser")
	tight := withCapacity(node("node_d", 4), 4, 1024, 4, 1024)

	assert.Equal(t, ReasonNoNodes, placementReason(nil, spec.TaskSpec{}, nodes.TaskCost{}))
	assert.Contains(t,
		placementReason([]nodes.Snapshot{full}, spec.TaskSpec{Labels: []string{"gpu"}}, nodes.TaskCost{}),
		ReasonNoLabels)
	assert.Contains(t,
		placementReason([]nodes.Snapshot{full}, spec.TaskSpec{Labels: []string{"gpu"}}, nodes.TaskCost{}),
		"gpu")
	assert.Equal(t, ReasonAllDraining,
		placementReason([]nodes.Snapshot{draining}, spec.TaskSpec{}, nodes.TaskCost{}))
	assert.Contains(t,
		placementReason([]nodes.Snapshot{tight}, spec.TaskSpec{}, nodes.TaskCost{CPU: 8}),
		ReasonNoRoom)
	assert.Equal(t, ReasonFull,
		placementReason([]nodes.Snapshot{full, labelled}, spec.TaskSpec{}, nodes.TaskCost{}))
}

func TestFastTimersShrinkEveryIntervalTenfold(t *testing.T) {
	slow := DefaultTiming()
	fast := slow.Fast()

	assert.Equal(t, 50*time.Millisecond, fast.Tick)
	assert.Equal(t, 500*time.Millisecond, fast.Watchdog)
	assert.Equal(t, 12*time.Second, fast.OfflineAfter)
	assert.Equal(t, 3*time.Second, fast.UnreachableAfter)
	assert.Equal(t, 1500*time.Millisecond, fast.ProvisioningDeadline)
	assert.Equal(t, 6*time.Second, fast.CancelGrace)
	assert.Equal(t, slow.ClaimLimit, fast.ClaimLimit, "a batch size is not a timer")
}

func TestFastTimersFlagParsing(t *testing.T) {
	for _, on := range []string{"1", "true", "TRUE", "t"} {
		assert.True(t, fastTimers(on), on)
	}
	for _, off := range []string{"", "0", "false", "no", "yes", "banana"} {
		assert.False(t, fastTimers(off), off)
	}
}

func TestLeaseDeadlineCoversTheWholeTimeoutPlusAGrace(t *testing.T) {
	s := &Service{timing: DefaultTiming()}
	now := time.Now().UTC()
	started := now.Add(-time.Minute)

	scheduled := store.Task{Status: store.StatusScheduled, ScheduledAt: &now}
	assert.Equal(t, now.Add(2*time.Minute), s.leaseDeadline(scheduled, now))

	running := store.Task{
		Status:    store.StatusRunning,
		StartedAt: &started,
		Spec:      spec.TaskSpec{Timeout: spec.Duration(30 * time.Minute)},
	}
	assert.Equal(t, started.Add(35*time.Minute), s.leaseDeadline(running, now),
		"a lease must outlast the task's own timeout, or it expires under a healthy run")
}
