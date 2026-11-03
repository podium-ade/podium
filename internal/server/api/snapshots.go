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
	"github.com/podium-ade/podium/internal/server/sandboxsnaps"
	"github.com/podium-ade/podium/internal/server/snapshots"
	"github.com/podium-ade/podium/internal/server/store"
)

// SnapshotService implements podium.v1.SnapshotService.
type SnapshotService struct {
	snapshots *snapshots.Service
	sandbox   *sandboxsnaps.Service
	logger    *slog.Logger
}

// NewSnapshotService returns the operator-facing snapshot handlers. sandbox may be nil, and
// then the sandbox snapshot RPCs list nothing and refuse changes.
func NewSnapshotService(svc *snapshots.Service, sandbox *sandboxsnaps.Service, logger *slog.Logger) *SnapshotService {
	if logger == nil {
		logger = slog.Default()
	}
	return &SnapshotService{snapshots: svc, sandbox: sandbox, logger: logger}
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

// errNoSandboxSnapshots is a server built without the sandbox snapshot service.
var errNoSandboxSnapshots = connect.NewError(connect.CodeFailedPrecondition, errors.New("sandbox snapshots are not served by this control plane"))

// ListSandboxSnapshots returns every session image a sandbox provider keeps, the policy, and
// which configured providers keep them.
func (s *SnapshotService) ListSandboxSnapshots(
	ctx context.Context,
	_ *connect.Request[podiumv1.ListSandboxSnapshotsRequest],
) (*connect.Response[podiumv1.ListSandboxSnapshotsResponse], error) {
	if s.sandbox == nil {
		return connect.NewResponse(&podiumv1.ListSandboxSnapshotsResponse{Retention: &podiumv1.SandboxSnapshotRetention{}}), nil
	}
	rows, err := s.sandbox.List(ctx)
	if err != nil {
		return nil, storeError(fmt.Errorf("list sandbox snapshots: %w", err))
	}
	policy, err := s.sandbox.Retention(ctx)
	if err != nil {
		return nil, storeError(fmt.Errorf("list sandbox snapshots: %w", err))
	}
	providers, err := s.sandbox.Providers(ctx)
	if err != nil {
		return nil, storeError(fmt.Errorf("list sandbox snapshots: %w", err))
	}
	out := make([]*podiumv1.SandboxSnapshot, 0, len(rows))
	for _, r := range rows {
		out = append(out, &podiumv1.SandboxSnapshot{
			SessionId: r.SessionID,
			Provider:  r.Provider,
			ImageId:   r.ImageID,
			NodeId:    r.NodeID,
			TaskId:    r.TaskID,
			UpdatedAt: timestamppb.New(r.UpdatedAt),
			InUse:     r.InUse,
		})
	}
	return connect.NewResponse(&podiumv1.ListSandboxSnapshotsResponse{
		Snapshots: out,
		Retention: sandboxRetentionToProto(policy),
		Providers: providers,
	}), nil
}

// maxDeleteBatch bounds one DeleteSandboxSnapshots call: each image is a provider round trip.
const maxDeleteBatch = 500

// DeleteSandboxSnapshots deletes the images of the sessions named, one or many.
func (s *SnapshotService) DeleteSandboxSnapshots(
	ctx context.Context,
	req *connect.Request[podiumv1.DeleteSandboxSnapshotsRequest],
) (*connect.Response[podiumv1.DeleteSandboxSnapshotsResponse], error) {
	if s.sandbox == nil {
		return nil, errNoSandboxSnapshots
	}
	ids := req.Msg.GetSessionIds()
	if len(ids) == 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("delete sandbox snapshots: session_ids is required"))
	}
	if len(ids) > maxDeleteBatch {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("delete sandbox snapshots: at most %d at once", maxDeleteBatch))
	}
	removed, failed, err := s.sandbox.Delete(ctx, login(ctx), ids)
	if err != nil {
		return nil, storeError(fmt.Errorf("delete sandbox snapshots: %w", err))
	}
	return connect.NewResponse(&podiumv1.DeleteSandboxSnapshotsResponse{Removed: int32(removed), Failed: failed}), nil
}

// SetSandboxSnapshotRetention replaces the policy and sweeps once with it.
func (s *SnapshotService) SetSandboxSnapshotRetention(
	ctx context.Context,
	req *connect.Request[podiumv1.SetSandboxSnapshotRetentionRequest],
) (*connect.Response[podiumv1.SetSandboxSnapshotRetentionResponse], error) {
	if s.sandbox == nil {
		return nil, errNoSandboxSnapshots
	}
	age := req.Msg.GetMaxAgeSeconds()
	count := req.Msg.GetMaxCount()
	if age < 0 || count < 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("set sandbox snapshot retention: limits cannot be negative"))
	}
	if age > int64((math.MaxInt64-sandboxsnaps.TTLMargin)/time.Second) {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("set sandbox snapshot retention: max_age_seconds is too large"))
	}
	policy, removed, err := s.sandbox.SetRetention(ctx, login(ctx), store.SandboxSnapshotRetention{
		MaxAge:   time.Duration(age) * time.Second,
		MaxCount: count,
	})
	if err != nil {
		return nil, storeError(fmt.Errorf("set sandbox snapshot retention: %w", err))
	}
	return connect.NewResponse(&podiumv1.SetSandboxSnapshotRetentionResponse{
		Retention: sandboxRetentionToProto(policy),
		Removed:   int32(removed),
	}), nil
}

func sandboxRetentionToProto(p store.SandboxSnapshotRetention) *podiumv1.SandboxSnapshotRetention {
	out := &podiumv1.SandboxSnapshotRetention{
		MaxAgeSeconds: int64(p.MaxAge / time.Second),
		MaxCount:      p.MaxCount,
		UpdatedBy:     p.UpdatedBy,
	}
	if !p.UpdatedAt.IsZero() {
		out.UpdatedAt = timestamppb.New(p.UpdatedAt)
	}
	return out
}
