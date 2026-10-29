package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	podiumv1 "github.com/podium-ade/podium/internal/proto/podium/v1"
	"github.com/podium-ade/podium/internal/server/snapshots"
	"github.com/podium-ade/podium/internal/server/store"
)

// SnapshotService implements podium.v1.SnapshotService.
type SnapshotService struct {
	snapshots *snapshots.Service
	logger    *slog.Logger
}

// NewSnapshotService returns the operator-facing snapshot handlers.
func NewSnapshotService(svc *snapshots.Service, logger *slog.Logger) *SnapshotService {
	if logger == nil {
		logger = slog.Default()
	}
	return &SnapshotService{snapshots: svc, logger: logger}
}

// ListSnapshots returns every session snapshot and the retention policy. A server with no
// object store answers with enabled false rather than an error, so the page can say why.
func (s *SnapshotService) ListSnapshots(
	ctx context.Context,
	_ *connect.Request[podiumv1.ListSnapshotsRequest],
) (*connect.Response[podiumv1.ListSnapshotsResponse], error) {
	rows, err := s.snapshots.List(ctx)
	if err != nil {
		return nil, storeError(fmt.Errorf("list snapshots: %w", err))
	}
	policy, err := s.snapshots.Retention(ctx)
	if err != nil {
		return nil, storeError(fmt.Errorf("list snapshots: %w", err))
	}
	out := make([]*podiumv1.Snapshot, 0, len(rows))
	for _, r := range rows {
		out = append(out, &podiumv1.Snapshot{
			SessionId: r.SessionID,
			SizeBytes: r.SizeBytes,
			Sha256:    r.SHA256,
			NodeId:    r.NodeID,
			TaskId:    r.TaskID,
			UpdatedAt: timestamppb.New(r.UpdatedAt),
			InUse:     r.InUse,
		})
	}
	return connect.NewResponse(&podiumv1.ListSnapshotsResponse{
		Snapshots: out,
		Retention: retentionToProto(policy),
		Enabled:   s.snapshots.Enabled(),
	}), nil
}

// DeleteSnapshot removes one session's snapshot, row first and object second.
func (s *SnapshotService) DeleteSnapshot(
	ctx context.Context,
	req *connect.Request[podiumv1.DeleteSnapshotRequest],
) (*connect.Response[podiumv1.DeleteSnapshotResponse], error) {
	session := req.Msg.GetSessionId()
	if session == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("delete snapshot: session_id is required"))
	}
	if err := s.snapshots.Delete(ctx, login(ctx), session); err != nil {
		return nil, snapshotError(fmt.Errorf("delete snapshot: %w", err))
	}
	return connect.NewResponse(&podiumv1.DeleteSnapshotResponse{}), nil
}

// SetSnapshotRetention replaces the policy and sweeps once with it.
func (s *SnapshotService) SetSnapshotRetention(
	ctx context.Context,
	req *connect.Request[podiumv1.SetSnapshotRetentionRequest],
) (*connect.Response[podiumv1.SetSnapshotRetentionResponse], error) {
	age := req.Msg.GetMaxAgeSeconds()
	total := req.Msg.GetMaxTotalBytes()
	if age < 0 || total < 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("set snapshot retention: limits cannot be negative"))
	}
	if age > int64(math.MaxInt64/time.Second) {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("set snapshot retention: max_age_seconds is too large"))
	}
	policy, swept, err := s.snapshots.SetRetention(ctx, login(ctx), store.SnapshotRetention{
		MaxAge:        time.Duration(age) * time.Second,
		MaxTotalBytes: total,
	})
	if err != nil {
		return nil, snapshotError(fmt.Errorf("set snapshot retention: %w", err))
	}
	return connect.NewResponse(&podiumv1.SetSnapshotRetentionResponse{
		Retention:  retentionToProto(policy),
		Removed:    int32(swept.Removed),
		FreedBytes: swept.FreedBytes,
	}), nil
}

func retentionToProto(p store.SnapshotRetention) *podiumv1.SnapshotRetention {
	out := &podiumv1.SnapshotRetention{
		MaxAgeSeconds: int64(p.MaxAge / time.Second),
		MaxTotalBytes: p.MaxTotalBytes,
		UpdatedBy:     p.UpdatedBy,
	}
	if !p.UpdatedAt.IsZero() {
		out.UpdatedAt = timestamppb.New(p.UpdatedAt)
	}
	return out
}

func snapshotError(err error) error {
	switch {
	case errors.Is(err, snapshots.ErrDisabled):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	case errors.Is(err, snapshots.ErrReplaced):
		return connect.NewError(connect.CodeAborted, err)
	default:
		return storeError(err)
	}
}
