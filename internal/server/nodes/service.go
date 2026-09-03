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
	"strings"
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

// Ingestor is the logs service, as much of it as the node stream needs.
type Ingestor interface {
	Ingest(ctx context.Context, taskID string, batch []*podiumv1.TaskEvent) (uint64, error)
	// Note records something the control plane observed about a task rather than
	// something the node reported — losing the node being the case that matters.
	Note(ctx context.Context, taskID, message string) error
}

// Service implements podium.v1.NodeService.
type Service struct {
	store  *store.Store
	logs   Ingestor
	reg    *Registry
	logger *slog.Logger
	enroll *rateLimiter

	// watchdog is the health-sweep policy, set by the server from the scheduler's Timing.
	watchdog Watchdog

	// allowUntagged mirrors PODIUM_TS_ALLOW_UNTAGGED_NODES. Off, a tailnet caller must carry
	// the node tag to enroll; on, any tailnet device with a valid token may, which is the
	// documented escape hatch for a tailnet that has no ACL tags yet.
	allowUntagged bool
}

// NewService returns the node-facing service and its (empty) session registry.
func NewService(st *store.Store, ing Ingestor, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{
		store:    st,
		logs:     ing,
		reg:      NewRegistry(),
		logger:   logger,
		enroll:   newRateLimiter(enrollBurst, enrollWindow, nil),
		watchdog: Watchdog{Interval: 5 * time.Second, UnreachableAfter: 30 * time.Second, OfflineAfter: 120 * time.Second},
	}
}

// SetAllowUntaggedNodes lets an untagged tailnet device enroll. It is a setter because the
// server reads the flag from its own configuration, which nodes must not import.
func (s *Service) SetAllowUntaggedNodes(v bool) { s.allowUntagged = v }

// Registry is the live session registry, which the scheduler and the admin API read.
func (s *Service) Registry() *Registry { return s.reg }

// Enroll exchanges a single-use enrollment token for a node identity. The node key is 32 random
// bytes returned exactly once; only its SHA-256 reaches Postgres.
func (s *Service) Enroll(
	ctx context.Context,
	req *connect.Request[podiumv1.EnrollRequest],
) (*connect.Response[podiumv1.EnrollResponse], error) {
	id, ok := transport.From(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("enroll: no identity"))
	}
	if !s.mayEnroll(id) {
		s.logger.WarnContext(ctx, "enrollment refused: caller is not a node",
			"kind", id.Kind, "login", id.Login, "remote_addr", id.RemoteAddr, "tags", id.NodeTags)
		return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf(
			"enroll: this device is not tagged as a Podium node; give it a Tailscale auth key "+
				"tagged %s (or set PODIUM_TS_ALLOW_UNTAGGED_NODES=true on the server, which is "+
				"weaker)", tailnetNodeTagHint(id)))
	}
	// The budget is per remote IP, before the token is looked at: an unlimited Enroll is an
	// unlimited guessing run at a 32-byte token, and refusing early costs no database work.
	if !s.enroll.allow(rateKey(id.RemoteAddr)) {
		s.logger.WarnContext(ctx, "enrollment rate limited", "remote_addr", id.RemoteAddr)
		return nil, connect.NewError(connect.CodeResourceExhausted, fmt.Errorf(
			"enroll: more than %d attempts in %s from this address; wait and try again",
			enrollBurst, enrollWindow))
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
		TSStableID:  id.NodeStableID,
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("enroll: %w", err))
	}
	s.logger.InfoContext(ctx, "node enrolled",
		"node_id", node.ID, "name", node.Name, "labels", node.Labels,
		"arch", in.GetArch(), "os", in.GetOs(), "docker_version", in.GetDockerVersion(),
		"ts_stable_id", node.TSStableID, "tags", id.NodeTags)

	return connect.NewResponse(&podiumv1.EnrollResponse{NodeId: node.ID, NodeKey: nodeKey}), nil
}

// mayEnroll decides whether an identity is allowed to become a node.
//
//   - KindNode: the device carries the required ACL tag. This is the tailnet path.
//   - KindDevToken: the dev transport has no device tags at all, and its token is already the
//     only thing between a caller and the whole API.
//   - KindUser: only under PODIUM_TS_ALLOW_UNTAGGED_NODES, the escape hatch for a tailnet with
//     no tags yet.
func (s *Service) mayEnroll(id transport.Identity) bool {
	switch id.Kind {
	case transport.KindNode, transport.KindDevToken:
		return true
	case transport.KindUser:
		return s.allowUntagged
	default:
		return false
	}
}

// tailnetNodeTagHint names the tag the caller is missing. The transport does not tell the
// handler which tag it required, so the canonical one is the honest thing to print.
func tailnetNodeTagHint(id transport.Identity) string {
	if len(id.NodeTags) > 0 {
		return "tag:podium-node (this device carries " + strings.Join(id.NodeTags, ",") + ")"
	}
	return "tag:podium-node"
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

// Assign hands a task to a connected node and charges the task's cost against the node's
// budget, so the scheduler's next tick sees the capacity go rather than waiting for a
// heartbeat up to ten seconds away. Every log statement that touches an Assign goes through
// podiumv1.RedactForLog; nothing else may log one.
func (s *Service) Assign(ctx context.Context, nodeID string, a *podiumv1.Assign, cost TaskCost) error {
	sess, ok := s.reg.Get(nodeID)
	if !ok {
		return fmt.Errorf("assign task %s to node %s: %w", a.GetTaskId(), nodeID, ErrNoSession)
	}
	if err := sess.Send(ctx, &podiumv1.ServerMessage{Msg: &podiumv1.ServerMessage_Assign{Assign: a}}); err != nil {
		return fmt.Errorf("assign task %s to node %s: %w", a.GetTaskId(), nodeID, err)
	}
	sess.reserve(a.GetTaskId(), cost)
	s.logger.InfoContext(ctx, "task assigned", "node_id", nodeID, "assign", podiumv1.RedactForLog(a))
	return nil
}

// SetDrain tells a connected node to stop accepting work, or to start again. The stored
// nodes.draining column is the durable half and is the caller's job; this is what makes the
// live session act on it immediately.
//
// A node with no live session is not an error: draining an offline node is a legitimate
// thing to do before it comes back, and the column is what it reads when it does.
func (s *Service) SetDrain(ctx context.Context, nodeID string, draining bool) error {
	sess, ok := s.reg.Get(nodeID)
	if !ok {
		return fmt.Errorf("drain node %s: %w", nodeID, ErrNoSession)
	}
	sess.setDraining(draining)
	msg := &podiumv1.ServerMessage{Msg: &podiumv1.ServerMessage_Drain{
		Drain: &podiumv1.Drain{Undo: !draining},
	}}
	if err := sess.Send(ctx, msg); err != nil {
		return fmt.Errorf("drain node %s: %w", nodeID, err)
	}
	s.logger.InfoContext(ctx, "node drain state sent", "node_id", nodeID, "draining", draining)
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

// Release gives back the slot and the resources a node was holding for a task, on whichever
// session claims it. The event path already does this for a task that reaches a terminal
// status; this is for the ones that never get there — an assignment revoked before the node
// accepted it, or a cancelled task the server wrote off without its node. Without it the
// session keeps a phantom entry until the node reconnects, and reports both a running task
// that is not running and less free CPU and memory than it has.
func (s *Service) Release(taskID string) { s.reg.Release(taskID) }
