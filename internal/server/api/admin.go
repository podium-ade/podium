package api

import (
	"context"
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

// NodeAdminService implements podium.v1.NodeAdminService. GetNode, DrainNode and DeleteNode are
// deliberately absent in MVP-0.
type NodeAdminService struct {
	store    *store.Store
	sessions Sessions
	logger   *slog.Logger
}

// NewNodeAdminService returns the node administration API.
func NewNodeAdminService(st *store.Store, sessions Sessions, logger *slog.Logger) *NodeAdminService {
	if logger == nil {
		logger = slog.Default()
	}
	return &NodeAdminService{store: st, sessions: sessions, logger: logger}
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
