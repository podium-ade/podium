package store

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/alvaroibarguen/podium/pkg/spec"
)

// Sentinel errors every caller is expected to match with errors.Is.
var (
	// ErrNotFound is returned when the addressed row does not exist.
	ErrNotFound = errors.New("store: not found")
	// ErrInvalidTransition is returned when a task is not in one of the requested `from`
	// states, when the edge is not in the legal state graph, or when a requeue would exceed
	// max_attempts.
	ErrInvalidTransition = errors.New("store: invalid task transition")
	// ErrTokenExpired is returned by ConsumeEnrollmentToken for a token past its expiry.
	ErrTokenExpired = errors.New("store: enrollment token expired")
	// ErrTokenUsed is returned by ConsumeEnrollmentToken for an already-consumed token.
	ErrTokenUsed = errors.New("store: enrollment token already used")
)

// Status mirrors the tasks.status column.
type Status string

// The canonical task statuses.
const (
	StatusQueued       Status = "queued"
	StatusScheduled    Status = "scheduled"
	StatusProvisioning Status = "provisioning"
	StatusRunning      Status = "running"
	StatusSucceeded    Status = "succeeded"
	StatusFailed       Status = "failed"
	StatusCancelled    Status = "cancelled"
	StatusLost         Status = "lost"
)

// AllStatuses lists every legal value of tasks.status.
var AllStatuses = []Status{
	StatusQueued, StatusScheduled, StatusProvisioning, StatusRunning,
	StatusSucceeded, StatusFailed, StatusCancelled, StatusLost,
}

// Valid reports whether s is one of the canonical statuses.
func (s Status) Valid() bool {
	for _, v := range AllStatuses {
		if s == v {
			return true
		}
	}
	return false
}

// Terminal reports whether s is an end state with no outgoing transitions.
func (s Status) Terminal() bool {
	switch s {
	case StatusSucceeded, StatusFailed, StatusCancelled, StatusLost:
		return true
	default:
		return false
	}
}

func (s Status) String() string { return string(s) }

// NodeStatus mirrors the nodes.status column.
type NodeStatus string

// The canonical node statuses.
const (
	NodeOnline      NodeStatus = "online"
	NodeUnreachable NodeStatus = "unreachable"
	NodeOffline     NodeStatus = "offline"
	NodeDraining    NodeStatus = "draining"
)

func (s NodeStatus) String() string { return string(s) }

// Usage is the resource accounting stored in tasks.usage. It mirrors podium.v1.Usage.
type Usage struct {
	CPUSeconds   float64 `json:"cpu_seconds,omitempty"`
	PeakMemoryMB int64   `json:"peak_memory_mb,omitempty"`
	WallMS       int64   `json:"wall_ms,omitempty"`
}

// NodeCapacity is what a node advertises, stored in nodes.capacity. It mirrors
// podium.v1.NodeCapacity.
type NodeCapacity struct {
	MaxTasks int32 `json:"max_tasks,omitempty"`
	CPUCores int32 `json:"cpu_cores,omitempty"`
	MemoryMB int64 `json:"memory_mb,omitempty"`
}

// Task is one row of the tasks table. Spec is the exact JSON stored in tasks.spec.
type Task struct {
	ID             string
	Spec           spec.TaskSpec
	Status         Status
	Priority       int32
	RequestedBy    string
	NodeID         string
	LeaseID        string
	LeaseExpiresAt *time.Time
	Attempts       int32
	MaxAttempts    int32
	CreatedAt      time.Time
	ScheduledAt    *time.Time
	StartedAt      *time.Time
	FinishedAt     *time.Time
	ExitCode       *int32
	Usage          *Usage
	FailureReason  string
}

// NewTask is the input to CreateTask. An empty ID is minted, a zero MaxAttempts falls back to
// the spec's and then to 1.
type NewTask struct {
	ID          string
	Spec        spec.TaskSpec
	Priority    int32
	RequestedBy string
	MaxAttempts int32
}

// Filter narrows ListTasks. Zero values mean "do not filter on this".
type Filter struct {
	Status      []Status
	NodeID      string
	RequestedBy string
}

// Page controls ListTasks pagination. Cursor is the ID returned as nextCursor by the previous
// call; results are newest-first by ID (ULIDs sort by mint time).
type Page struct {
	Limit  int
	Cursor string
}

// Patch carries the optional column updates a transition may apply. A nil field leaves the
// column untouched. Transitioning to queued always clears NodeID, LeaseID and LeaseExpiresAt
// regardless of what the patch says.
type Patch struct {
	StartedAt      *time.Time
	FinishedAt     *time.Time
	ExitCode       *int32
	Usage          *Usage
	FailureReason  *string
	NodeID         *string
	LeaseID        *string
	LeaseExpiresAt *time.Time
}

// Event is one row of the append-only task_events table.
type Event struct {
	Seq     uint64
	Kind    string
	TS      time.Time
	Payload json.RawMessage
}

// LogChunk is one row of task_log_chunks. Sidecar is empty for the task container itself.
type LogChunk struct {
	Seq     uint64
	Stream  string
	Sidecar string
	TS      time.Time
	Bytes   []byte
}

// Node is one row of the nodes table.
type Node struct {
	ID              string
	Name            string
	Tags            []string
	Labels          []string
	Capacity        NodeCapacity
	NodeKeyHash     []byte
	Status          NodeStatus
	Version         string
	LastHeartbeatAt *time.Time
	CreatedAt       time.Time
}

// NewNode is the input to CreateNode. An empty ID is minted.
type NewNode struct {
	ID          string
	Name        string
	Tags        []string
	Labels      []string
	Capacity    NodeCapacity
	NodeKeyHash []byte
	Status      NodeStatus
	Version     string
}
