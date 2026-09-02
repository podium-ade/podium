// Package nodes owns node identity and the node stream: enrollment, the in-memory session
// registry, heartbeat bookkeeping and the event batches nodes push back.
package nodes

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"connectrpc.com/connect"

	"github.com/alvaroibarguen/podium/internal/ids"
	podiumv1 "github.com/alvaroibarguen/podium/internal/proto/podium/v1"
	"github.com/alvaroibarguen/podium/internal/server/store"
	"github.com/alvaroibarguen/podium/internal/transport"
)

// Protocol timings. helloDeadline is how long a stream may stay anonymous; flushInterval and
// flushBytes mirror the node's own batching budget from the design (100ms / 64KB).
const (
	helloDeadline = 5 * time.Second
	flushInterval = 100 * time.Millisecond
	flushBytes    = 64 << 10
	nodeKeyBytes  = 32
)

// Ingestor is the logs service, as much of it as the stream needs.
type Ingestor interface {
	Ingest(ctx context.Context, taskID string, batch []*podiumv1.TaskEvent) (uint64, error)
}

// Service implements podium.v1.NodeService.
type Service struct {
	store  *store.Store
	logs   Ingestor
	reg    *Registry
	logger *slog.Logger
}

// NewService returns the node-facing service and its (empty) session registry.
func NewService(st *store.Store, ing Ingestor, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{store: st, logs: ing, reg: NewRegistry(), logger: logger}
}

// Registry is the live session registry, which the scheduler and the admin API read.
func (s *Service) Registry() *Registry { return s.reg }

// Enroll exchanges a single-use enrollment token for a node identity. The node key is 32 random
// bytes returned exactly once; only its SHA-256 reaches Postgres.
func (s *Service) Enroll(
	ctx context.Context,
	req *connect.Request[podiumv1.EnrollRequest],
) (*connect.Response[podiumv1.EnrollResponse], error) {
	id, ok := transport.From(ctx)
	if !ok || (id.Kind != transport.KindNode && id.Kind != transport.KindDevToken) {
		return nil, connect.NewError(connect.CodePermissionDenied,
			errors.New("enroll: caller is not a node"))
	}
	in := req.Msg
	if in.GetToken() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("enroll: token is required"))
	}

	nodeID := ids.NewNode()
	tokenLabels, err := s.store.ConsumeEnrollmentToken(ctx, in.GetToken(), nodeID)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrNotFound):
			return nil, connect.NewError(connect.CodePermissionDenied, errors.New("enroll: unknown token"))
		case errors.Is(err, store.ErrTokenUsed):
			return nil, connect.NewError(connect.CodePermissionDenied, errors.New("enroll: token already used"))
		case errors.Is(err, store.ErrTokenExpired):
			return nil, connect.NewError(connect.CodePermissionDenied, errors.New("enroll: token expired"))
		}
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("enroll: %w", err))
	}

	raw := make([]byte, nodeKeyBytes)
	if _, err := rand.Read(raw); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("enroll: generate node key: %w", err))
	}
	nodeKey := base64.RawURLEncoding.EncodeToString(raw)

	name := in.GetHostname()
	if name == "" {
		name = nodeID
	}
	node, err := s.store.CreateNode(ctx, store.NewNode{
		ID:          nodeID,
		Name:        name,
		Tags:        id.NodeTags,
		Labels:      unionLabels(tokenLabels, in.GetLabels()),
		Capacity:    store.NodeCapacity{CPUCores: in.GetCpuCores(), MemoryMB: in.GetMemoryMb()},
		NodeKeyHash: store.HashToken(nodeKey),
		Status:      store.NodeOffline,
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("enroll: %w", err))
	}
	s.logger.InfoContext(ctx, "node enrolled",
		"node_id", node.ID, "name", node.Name, "labels", node.Labels,
		"arch", in.GetArch(), "os", in.GetOs(), "docker_version", in.GetDockerVersion())

	return connect.NewResponse(&podiumv1.EnrollResponse{NodeId: node.ID, NodeKey: nodeKey}), nil
}

// unionLabels merges the token's labels with the ones the node asked for, sorted and deduped.
func unionLabels(a, b []string) []string {
	set := make(map[string]struct{}, len(a)+len(b))
	for _, l := range append(append([]string(nil), a...), b...) {
		if l != "" {
			set[l] = struct{}{}
		}
	}
	out := make([]string, 0, len(set))
	for l := range set {
		out = append(out, l)
	}
	slices.Sort(out)
	return out
}

// Assign hands a task to a connected node. Every log statement that touches an Assign goes
// through podiumv1.RedactForLog; nothing else may log one.
func (s *Service) Assign(ctx context.Context, nodeID string, a *podiumv1.Assign) error {
	sess, ok := s.reg.Get(nodeID)
	if !ok {
		return fmt.Errorf("assign task %s to node %s: %w", a.GetTaskId(), nodeID, ErrNoSession)
	}
	if err := sess.Send(ctx, &podiumv1.ServerMessage{Msg: &podiumv1.ServerMessage_Assign{Assign: a}}); err != nil {
		return fmt.Errorf("assign task %s to node %s: %w", a.GetTaskId(), nodeID, err)
	}
	sess.reserve(a.GetTaskId())
	s.logger.InfoContext(ctx, "task assigned", "node_id", nodeID, "assign", podiumv1.RedactForLog(a))
	return nil
}

// Cancel asks a node to stop a task. It does not wait: the container only dies once the node
// has run its SIGTERM grace period, and the terminal status lands with the exited/finished
// events.
func (s *Service) Cancel(ctx context.Context, nodeID, taskID, reason string) error {
	sess, ok := s.reg.Get(nodeID)
	if !ok {
		return fmt.Errorf("cancel task %s on node %s: %w", taskID, nodeID, ErrNoSession)
	}
	msg := &podiumv1.ServerMessage{Msg: &podiumv1.ServerMessage_Cancel{
		Cancel: &podiumv1.Cancel{TaskId: taskID, Reason: reason},
	}}
	if err := sess.Send(ctx, msg); err != nil {
		return fmt.Errorf("cancel task %s on node %s: %w", taskID, nodeID, err)
	}
	s.logger.InfoContext(ctx, "task cancel sent", "node_id", nodeID, "task_id", taskID, "reason", reason)
	return nil
}

// Candidates is what the scheduler matches against.
func (s *Service) Candidates() []Snapshot { return s.reg.Snapshot() }
