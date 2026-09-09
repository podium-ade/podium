package api

import (
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	podiumv1 "github.com/alvaroibarguen/podium/internal/proto/podium/v1"
	"github.com/alvaroibarguen/podium/internal/server/nodes"
	"github.com/alvaroibarguen/podium/internal/server/store"
)

var taskStatusProto = map[store.Status]podiumv1.TaskStatus{
	store.StatusQueued:       podiumv1.TaskStatus_TASK_STATUS_QUEUED,
	store.StatusScheduled:    podiumv1.TaskStatus_TASK_STATUS_SCHEDULED,
	store.StatusProvisioning: podiumv1.TaskStatus_TASK_STATUS_PROVISIONING,
	store.StatusRunning:      podiumv1.TaskStatus_TASK_STATUS_RUNNING,
	store.StatusSucceeded:    podiumv1.TaskStatus_TASK_STATUS_SUCCEEDED,
	store.StatusFailed:       podiumv1.TaskStatus_TASK_STATUS_FAILED,
	store.StatusCancelled:    podiumv1.TaskStatus_TASK_STATUS_CANCELLED,
	store.StatusLost:         podiumv1.TaskStatus_TASK_STATUS_LOST,
}

var taskStatusStore = func() map[podiumv1.TaskStatus]store.Status {
	m := make(map[podiumv1.TaskStatus]store.Status, len(taskStatusProto))
	for k, v := range taskStatusProto {
		m[v] = k
	}
	return m
}()

var nodeStatusProto = map[store.NodeStatus]podiumv1.NodeStatus{
	store.NodeOnline:      podiumv1.NodeStatus_NODE_STATUS_ONLINE,
	store.NodeUnreachable: podiumv1.NodeStatus_NODE_STATUS_UNREACHABLE,
	store.NodeOffline:     podiumv1.NodeStatus_NODE_STATUS_OFFLINE,
	store.NodeDraining:    podiumv1.NodeStatus_NODE_STATUS_DRAINING,
}

func taskToProto(t store.Task) *podiumv1.Task {
	out := &podiumv1.Task{
		Id:            t.ID,
		Spec:          t.Spec.ToProto(),
		Status:        taskStatusProto[t.Status],
		NodeId:        t.NodeID,
		Attempts:      t.Attempts,
		CreatedAt:     timestamppb.New(t.CreatedAt),
		StartedAt:     timeToProto(t.StartedAt),
		FinishedAt:    timeToProto(t.FinishedAt),
		ExitCode:      t.ExitCode,
		RequestedBy:   t.RequestedBy,
		FailureReason: t.FailureReason,
		QueuedReason:  t.QueuedReason,
		Priority:      t.Priority,

		LastScheduleAttemptAt: timeToProto(t.LastScheduleAttemptAt),
	}
	if t.Usage != nil {
		out.Usage = &podiumv1.Usage{
			CpuSeconds:   t.Usage.CPUSeconds,
			PeakMemoryMb: t.Usage.PeakMemoryMB,
			WallMs:       t.Usage.WallMS,
		}
	}
	return out
}

func nodeToProto(n store.Node, live nodes.Snapshot, connected bool) *podiumv1.Node {
	out := &podiumv1.Node{
		Id:     n.ID,
		Name:   n.Name,
		Status: nodeStatusProto[n.Status],
		Labels: n.Labels,
		Capacity: &podiumv1.NodeCapacity{
			MaxTasks: n.Capacity.MaxTasks,
			CpuCores: n.Capacity.CPUCores,
			MemoryMb: n.Capacity.MemoryMB,
		},
		Version:          n.Version,
		LastHeartbeatAt:  timeToProto(n.LastHeartbeatAt),
		CreatedAt:        timestamppb.New(n.CreatedAt),
		TsStableId:       n.TSStableID,
		Draining:         n.Draining,
		MaxTasksOverride: n.MaxTasksOverride,
	}
	if connected {
		out.RunningTasks = live.RunningTasks
		out.FreeSlots = live.FreeSlots
	}
	return out
}

func timeToProto(t *time.Time) *timestamppb.Timestamp {
	if t == nil {
		return nil
	}
	return timestamppb.New(*t)
}

// timeFromProto is timeToProto's inverse for an optional filter bound. An unset or invalid
// timestamp is nil, which every query reads as "unbounded on that side".
func timeFromProto(ts *timestamppb.Timestamp) *time.Time {
	if ts == nil || !ts.IsValid() {
		return nil
	}
	t := ts.AsTime()
	return &t
}
