//go:build integration

package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestNodeLifecycle(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)

	created, err := s.CreateNode(ctx, NewNode{
		Name:        "worker-1",
		Tags:        []string{"dev", "laptop"},
		Labels:      []string{"linux/arm64", "browser"},
		Capacity:    NodeCapacity{MaxTasks: 4, CPUCores: 10, MemoryMB: 32768},
		NodeKeyHash: HashToken("node-key-worker-1"),
		Status:      NodeOnline,
		Version:     "v0.1.0",
	})
	require.NoError(t, err)
	require.NotEmpty(t, created.ID)
	require.Equal(t, NodeOnline, created.Status)
	require.Equal(t, []string{"linux/arm64", "browser"}, created.Labels)
	require.Equal(t, []string{"dev", "laptop"}, created.Tags)
	require.Equal(t, NodeCapacity{MaxTasks: 4, CPUCores: 10, MemoryMB: 32768}, created.Capacity)
	require.Nil(t, created.LastHeartbeatAt)

	got, err := s.GetNode(ctx, created.ID)
	require.NoError(t, err)
	require.Equal(t, created, got)

	byKey, err := s.GetNodeByKeyHash(ctx, HashToken("node-key-worker-1"))
	require.NoError(t, err)
	require.Equal(t, created.ID, byKey.ID)

	_, err = s.GetNodeByKeyHash(ctx, HashToken("wrong-key"))
	require.ErrorIs(t, err, ErrNotFound)

	// A node with no labels still round-trips as an empty slice, not nil JSON.
	bare, err := s.CreateNode(ctx, NewNode{Name: "worker-2", NodeKeyHash: HashToken("node-key-worker-2")})
	require.NoError(t, err)
	require.Equal(t, NodeOffline, bare.Status)
	require.Empty(t, bare.Labels)
	require.Empty(t, bare.Tags)

	nodes, err := s.ListNodes(ctx)
	require.NoError(t, err)
	require.Len(t, nodes, 2)

	require.NoError(t, s.UpdateNodeHeartbeat(ctx, created.ID, NodeOnline,
		&NodeCapacity{MaxTasks: 2, CPUCores: 10, MemoryMB: 32768}, "v0.2.0"))
	beat, err := s.GetNode(ctx, created.ID)
	require.NoError(t, err)
	require.NotNil(t, beat.LastHeartbeatAt)
	require.WithinDuration(t, time.Now().UTC(), *beat.LastHeartbeatAt, time.Minute)
	require.Equal(t, int32(2), beat.Capacity.MaxTasks)
	require.Equal(t, "v0.2.0", beat.Version)

	// A heartbeat with nothing new keeps the last known capacity and version.
	require.NoError(t, s.UpdateNodeHeartbeat(ctx, created.ID, NodeUnreachable, nil, ""))
	quiet, err := s.GetNode(ctx, created.ID)
	require.NoError(t, err)
	require.Equal(t, NodeUnreachable, quiet.Status)
	require.Equal(t, int32(2), quiet.Capacity.MaxTasks)
	require.Equal(t, "v0.2.0", quiet.Version)

	require.NoError(t, s.SetNodeStatus(ctx, created.ID, NodeDraining))
	draining, err := s.GetNode(ctx, created.ID)
	require.NoError(t, err)
	require.Equal(t, NodeDraining, draining.Status)

	require.NoError(t, s.DeleteNode(ctx, bare.ID))
	_, err = s.GetNode(ctx, bare.ID)
	require.ErrorIs(t, err, ErrNotFound)

	require.ErrorIs(t, s.DeleteNode(ctx, bare.ID), ErrNotFound)
	require.ErrorIs(t, s.SetNodeStatus(ctx, "node_nope", NodeOnline), ErrNotFound)
	require.ErrorIs(t, s.UpdateNodeHeartbeat(ctx, "node_nope", NodeOnline, nil, ""), ErrNotFound)
	_, err = s.GetNode(ctx, "node_nope")
	require.ErrorIs(t, err, ErrNotFound)
}

// The operator's slot count is a column of its own, so a Hello re-advertising the node's own
// max_tasks cannot overwrite it. That is the whole reason it is not written into capacity.
func TestSetNodeMaxTasksSurvivesAHeartbeat(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)

	node, err := s.CreateNode(ctx, NewNode{
		Name:        "worker-1",
		Capacity:    NodeCapacity{MaxTasks: 4, CPUCores: 8, MemoryMB: 16384},
		NodeKeyHash: HashToken("node-key-slots"),
	})
	require.NoError(t, err)
	require.Nil(t, node.MaxTasksOverride)

	eight := int32(8)
	require.NoError(t, s.SetNodeMaxTasks(ctx, node.ID, &eight))
	capped, err := s.GetNode(ctx, node.ID)
	require.NoError(t, err)
	require.NotNil(t, capped.MaxTasksOverride)
	require.Equal(t, int32(8), *capped.MaxTasksOverride)

	require.NoError(t, s.UpdateNodeHeartbeat(ctx, node.ID, NodeOnline,
		&NodeCapacity{MaxTasks: 4, CPUCores: 8, MemoryMB: 16384}, "v0.2.0"))
	after, err := s.GetNode(ctx, node.ID)
	require.NoError(t, err)
	require.Equal(t, int32(4), after.Capacity.MaxTasks, "the node still advertises its own number")
	require.NotNil(t, after.MaxTasksOverride)
	require.Equal(t, int32(8), *after.MaxTasksOverride)

	require.NoError(t, s.SetNodeMaxTasks(ctx, node.ID, nil))
	cleared, err := s.GetNode(ctx, node.ID)
	require.NoError(t, err)
	require.Nil(t, cleared.MaxTasksOverride)

	require.ErrorIs(t, s.SetNodeMaxTasks(ctx, "node_nope", &eight), ErrNotFound)
}

func TestCreateNodeRequiresKeyHash(t *testing.T) {
	s := newStore(t)
	_, err := s.CreateNode(context.Background(), NewNode{Name: "keyless"})
	require.Error(t, err)
}
