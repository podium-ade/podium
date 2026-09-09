package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	podiumv1 "github.com/alvaroibarguen/podium/internal/proto/podium/v1"
	"github.com/alvaroibarguen/podium/internal/server/nodes"
	"github.com/alvaroibarguen/podium/internal/server/store"
)

// Sessions is the live-session half of the node registry.
type Sessions interface {
	SnapshotOf(nodeID string) (nodes.Snapshot, bool)
}

// Drainer pushes an operator's standing instructions at a connected node: what it may not
// take, and how much of it.
type Drainer interface {
	SetDrain(ctx context.Context, nodeID string, draining bool) error
	SetSlots(ctx context.Context, nodeID string, maxTasks int32) error
}

// MaxNodeSlots is the largest slot count SetNodeSlots accepts. It is a guard against a typo,
// not a considered limit on what a machine can do: the scheduler assigns straight up to this
// number, so a stray zero on the end would pile hundreds of containers onto one box before
// anybody noticed. An operator who really wants more can raise max_tasks in the node's own
// configuration, where the number sits next to the machine it describes.
const MaxNodeSlots = 256

// NodeAdminService implements podium.v1.NodeAdminService.
type NodeAdminService struct {
	store    *store.Store
	sessions Sessions
	drains   Drainer
	logger   *slog.Logger
}

// NewNodeAdminService returns the node administration API.
func NewNodeAdminService(st *store.Store, sessions Sessions, drains Drainer, logger *slog.Logger) *NodeAdminService {
	if logger == nil {
		logger = slog.Default()
	}
	return &NodeAdminService{store: st, sessions: sessions, drains: drains, logger: logger}
}

// CreateEnrollmentToken mints a single-use token. The plaintext is returned once and never
// stored; only its SHA-256 reaches Postgres, so it is never recoverable and never logged.
func (s *NodeAdminService) CreateEnrollmentToken(
	ctx context.Context,
	req *connect.Request[podiumv1.CreateEnrollmentTokenRequest],
) (*connect.Response[podiumv1.CreateEnrollmentTokenResponse], error) {
	ttl := req.Msg.GetTtl().AsDuration()
	if ttl <= 0 {
		ttl = store.DefaultEnrollmentTokenTTL
	}
	createdBy := login(ctx)
	expiresAt := time.Now().UTC().Add(ttl)

	token, id, err := s.store.CreateEnrollmentToken(ctx, req.Msg.GetLabels(), ttl, createdBy)
	if err != nil {
		return nil, storeError(err)
	}
	s.logger.InfoContext(ctx, "enrollment token created",
		"token_id", id, "labels", req.Msg.GetLabels(), "created_by", createdBy, "expires_at", expiresAt)

	return connect.NewResponse(&podiumv1.CreateEnrollmentTokenResponse{
		Token:     token,
		ExpiresAt: timestamppb.New(expiresAt),
	}), nil
}

// ListNodes returns every enrolled node, oldest first, with live slot counts for the ones that
// currently hold a stream on this server.
func (s *NodeAdminService) ListNodes(
	ctx context.Context,
	_ *connect.Request[podiumv1.ListNodesRequest],
) (*connect.Response[podiumv1.ListNodesResponse], error) {
	rows, err := s.store.ListNodes(ctx)
	if err != nil {
		return nil, storeError(err)
	}
	out := make([]*podiumv1.Node, 0, len(rows))
	for _, n := range rows {
		live, connected := s.sessions.SnapshotOf(n.ID)
		out = append(out, nodeToProto(n, live, connected))
	}
	return connect.NewResponse(&podiumv1.ListNodesResponse{Nodes: out}), nil
}

// RekeyNode unbinds a node from the Tailscale device it enrolled from. It is what an operator
// runs when a worker is rebuilt or replaced: the node keeps its ID, its labels and its history,
// and the next Hello binds it to whatever device it arrives from. Until then the node key alone
// is enough to connect, which is exactly the exposure the binding removes — so rekey a node when
// you are about to move it, not as a matter of routine.
func (s *NodeAdminService) RekeyNode(
	ctx context.Context,
	req *connect.Request[podiumv1.RekeyNodeRequest],
) (*connect.Response[podiumv1.RekeyNodeResponse], error) {
	nodeID := req.Msg.GetNodeId()
	if nodeID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("rekey: node_id is required"))
	}
	before, err := s.store.GetNode(ctx, nodeID)
	if err != nil {
		return nil, storeError(err)
	}
	if err := s.store.SetNodeTSStableID(ctx, nodeID, ""); err != nil {
		return nil, storeError(err)
	}
	s.logger.InfoContext(ctx, "node unbound from its tailscale device",
		"node_id", nodeID, "was_bound_to", before.TSStableID, "by", login(ctx))

	after, err := s.store.GetNode(ctx, nodeID)
	if err != nil {
		return nil, storeError(err)
	}
	live, connected := s.sessions.SnapshotOf(nodeID)
	return connect.NewResponse(&podiumv1.RekeyNodeResponse{Node: nodeToProto(after, live, connected)}), nil
}

// DrainNode stops a node being given new work. Whatever it is running finishes, which is
// the whole point: a drain is how a machine is taken out of service without killing the
// jobs that are already on it.
//
// The flag is a column, so it survives both daemons restarting and applies to a node that
// is offline right now — draining a machine before you power it back on is a reasonable
// thing to want. The live session is told as well, so the scheduler stops considering the
// node immediately rather than at its next heartbeat.
func (s *NodeAdminService) DrainNode(
	ctx context.Context,
	req *connect.Request[podiumv1.DrainNodeRequest],
) (*connect.Response[podiumv1.DrainNodeResponse], error) {
	node, err := s.setDraining(ctx, req.Msg.GetNodeId(), true)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&podiumv1.DrainNodeResponse{Node: node}), nil
}

// UndrainNode puts a drained node back in the pool.
func (s *NodeAdminService) UndrainNode(
	ctx context.Context,
	req *connect.Request[podiumv1.UndrainNodeRequest],
) (*connect.Response[podiumv1.UndrainNodeResponse], error) {
	node, err := s.setDraining(ctx, req.Msg.GetNodeId(), false)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&podiumv1.UndrainNodeResponse{Node: node}), nil
}

// SetNodeSlots changes how many tasks a node runs at once. Zero clears the instruction, and
// the node goes back to the max_tasks in its own configuration file.
//
// It is stored against the node rather than sent and forgotten, for the same reason a drain
// is: an operator who caps a machine at two tasks means it for the machine, and a value that
// evaporated on the next reconnect would be worse than no feature at all. A node that is
// offline right now is a legitimate thing to configure — every stream is sent the count just
// after its HelloAck, so the node picks it up on its next connection.
func (s *NodeAdminService) SetNodeSlots(
	ctx context.Context,
	req *connect.Request[podiumv1.SetNodeSlotsRequest],
) (*connect.Response[podiumv1.SetNodeSlotsResponse], error) {
	nodeID := req.Msg.GetNodeId()
	maxTasks := req.Msg.GetMaxTasks()
	switch {
	case nodeID == "":
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("set slots: node_id is required"))
	case maxTasks < 0:
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf(
			"set slots: max_tasks is %d; it cannot be negative, and 0 hands the node back to its own max_tasks", maxTasks))
	case maxTasks > MaxNodeSlots:
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf(
			"set slots: max_tasks is %d, and %d is the most this API accepts; raise max_tasks in the node's own configuration if the machine really takes more",
			maxTasks, MaxNodeSlots))
	}
	if _, err := s.store.GetNode(ctx, nodeID); err != nil {
		return nil, storeError(err)
	}
	var override *int32
	if maxTasks > 0 {
		override = &maxTasks
	}
	if err := s.store.SetNodeMaxTasks(ctx, nodeID, override); err != nil {
		return nil, storeError(err)
	}
	if s.drains != nil {
		if err := s.drains.SetSlots(ctx, nodeID, maxTasks); err != nil {
			// Every stream is sent the count as it opens, so failing to reach the node
			// now changes nothing about the outcome.
			s.logger.InfoContext(ctx, "slot count not delivered; the node will read it on reconnect",
				"node_id", nodeID, "max_tasks", maxTasks, "error", err)
		}
	}
	s.logger.InfoContext(ctx, "node slot count changed",
		"node_id", nodeID, "max_tasks", maxTasks, "by", login(ctx))

	after, err := s.store.GetNode(ctx, nodeID)
	if err != nil {
		return nil, storeError(err)
	}
	live, connected := s.sessions.SnapshotOf(nodeID)
	return connect.NewResponse(&podiumv1.SetNodeSlotsResponse{Node: nodeToProto(after, live, connected)}), nil
}

func (s *NodeAdminService) setDraining(ctx context.Context, nodeID string, draining bool) (*podiumv1.Node, error) {
	if nodeID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("drain: node_id is required"))
	}
	if _, err := s.store.GetNode(ctx, nodeID); err != nil {
		return nil, storeError(err)
	}
	if err := s.store.SetNodeDraining(ctx, nodeID, draining); err != nil {
		return nil, storeError(err)
	}
	// Only a connected node's status is touched here. For a disconnected one the health
	// watchdog owns the column, and writing "offline" over an "unreachable" that is only
	// ten seconds old would be a worse answer than the one already there.
	if _, connected := s.sessions.SnapshotOf(nodeID); connected {
		status := store.NodeOnline
		if draining {
			status = store.NodeDraining
		}
		if err := s.store.SetNodeStatus(ctx, nodeID, status); err != nil {
			s.logger.WarnContext(ctx, "updating node status after a drain failed", "node_id", nodeID, "error", err)
		}
	}
	if s.drains != nil {
		if err := s.drains.SetDrain(ctx, nodeID, draining); err != nil {
			// An offline node reads nodes.draining when it reconnects, so failing to
			// reach it now changes nothing about the outcome.
			s.logger.InfoContext(ctx, "drain instruction not delivered; the node will read it on reconnect",
				"node_id", nodeID, "draining", draining, "error", err)
		}
	}
	s.logger.InfoContext(ctx, "node drain state changed", "node_id", nodeID, "draining", draining, "by", login(ctx))

	after, err := s.store.GetNode(ctx, nodeID)
	if err != nil {
		return nil, storeError(err)
	}
	live, connected := s.sessions.SnapshotOf(nodeID)
	return nodeToProto(after, live, connected), nil
}

// DeleteNode forgets a node. It refuses one that still holds a stream unless it has been
// drained, and refuses one that still has tasks on it whatever its state: tasks.node_id is
// a foreign key, and a task whose node row vanished is a task nobody can explain.
func (s *NodeAdminService) DeleteNode(
	ctx context.Context,
	req *connect.Request[podiumv1.DeleteNodeRequest],
) (*connect.Response[podiumv1.DeleteNodeResponse], error) {
	nodeID := req.Msg.GetNodeId()
	if nodeID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("delete node: node_id is required"))
	}
	node, err := s.store.GetNode(ctx, nodeID)
	if err != nil {
		return nil, storeError(err)
	}
	_, connected := s.sessions.SnapshotOf(nodeID)
	if connected && !node.Draining && !req.Msg.GetForce() {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf(
			"delete node %s: it is %s and not draining; run `podium node drain %s` first, or pass --force",
			nodeID, node.Status, nodeID))
	}
	active, err := s.store.ListTasksOnNode(ctx, nodeID, store.ActiveStatuses)
	if err != nil {
		return nil, storeError(err)
	}
	if len(active) > 0 {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf(
			"delete node %s: %d task(s) are still running on it", nodeID, len(active)))
	}
	if err := s.store.DeleteNode(ctx, nodeID); err != nil {
		return nil, storeError(err)
	}
	s.logger.InfoContext(ctx, "node deleted", "node_id", nodeID, "name", node.Name, "by", login(ctx))
	return connect.NewResponse(&podiumv1.DeleteNodeResponse{}), nil
}
