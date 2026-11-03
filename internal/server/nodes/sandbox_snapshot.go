package nodes

import (
	"context"
	"errors"
	"fmt"
	"time"

	"connectrpc.com/connect"

	podiumv1 "github.com/podium-ade/podium/internal/proto/podium/v1"
	"github.com/podium-ade/podium/internal/server/sandboxsnaps"
	"github.com/podium-ade/podium/internal/server/store"
)

// SetSandboxSnapshots wires in the store of provider-kept workspaces. Nil refuses both RPCs.
func (s *Service) SetSandboxSnapshots(svc *sandboxsnaps.Service) { s.sandboxSnaps = svc }

// GetSandboxSnapshot is the image a session resumes from, and how long a new one lives.
func (s *Service) GetSandboxSnapshot(
	ctx context.Context,
	req *connect.Request[podiumv1.GetSandboxSnapshotRequest],
) (*connect.Response[podiumv1.GetSandboxSnapshotResponse], error) {
	if s.sandboxSnaps == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("get sandbox snapshot: not served by this control plane"))
	}
	msg := req.Msg
	if msg.GetSessionId() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("get sandbox snapshot: session_id is required"))
	}
	if err := s.authorizeSnapshot(ctx, msg.GetNodeId(), msg.GetNodeKey(), msg.GetTaskId()); err != nil {
		return nil, err
	}
	row, ttl, found, err := s.sandboxSnaps.Get(ctx, msg.GetSessionId())
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("get sandbox snapshot: %w", err))
	}
	out := &podiumv1.GetSandboxSnapshotResponse{TtlSeconds: -1}
	if ttl > 0 {
		out.TtlSeconds = int64(ttl / time.Second)
	}
	// An image of another provider is no use to this one: the session starts as new here.
	if found && row.Provider == msg.GetProvider() {
		out.Found, out.ImageId = true, row.ImageID
	}
	return connect.NewResponse(out), nil
}

// PutSandboxSnapshot records the image a session's workspace is now kept as, and drops the
// session's tar: a session on a provider that keeps images keeps nothing in the object store.
// An empty image_id forgets the image the session had, which the provider no longer has.
func (s *Service) PutSandboxSnapshot(
	ctx context.Context,
	req *connect.Request[podiumv1.PutSandboxSnapshotRequest],
) (*connect.Response[podiumv1.PutSandboxSnapshotResponse], error) {
	if s.sandboxSnaps == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("put sandbox snapshot: not served by this control plane"))
	}
	msg := req.Msg
	if msg.GetSessionId() == "" || msg.GetProvider() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("put sandbox snapshot: session_id and provider are required"))
	}
	if err := s.authorizeSnapshot(ctx, msg.GetNodeId(), msg.GetNodeKey(), msg.GetTaskId()); err != nil {
		return nil, err
	}
	if msg.GetImageId() == "" {
		row, _, found, err := s.sandboxSnaps.Get(ctx, msg.GetSessionId())
		if err == nil && found && row.Provider == msg.GetProvider() {
			err = s.sandboxSnaps.Forget(ctx, row.SessionID, row.ImageID)
		}
		if err != nil {
			return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("put sandbox snapshot: %w", err))
		}
		return connect.NewResponse(&podiumv1.PutSandboxSnapshotResponse{}), nil
	}
	if err := s.sandboxSnaps.Put(ctx, store.SandboxSnapshot{
		SessionID: msg.GetSessionId(), Provider: msg.GetProvider(), ImageID: msg.GetImageId(),
		NodeID: msg.GetNodeId(), TaskID: msg.GetTaskId(),
	}); err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("put sandbox snapshot: %w", err))
	}
	if s.snapshots != nil {
		if err := s.snapshots.Discard(ctx, msg.GetSessionId()); err != nil {
			s.logger.WarnContext(ctx, "dropping the tar of a session now kept as a sandbox snapshot failed",
				"session_id", msg.GetSessionId(), "error", err)
		}
	}
	s.logger.InfoContext(ctx, "sandbox snapshot stored", "node_id", msg.GetNodeId(), "task_id", msg.GetTaskId(),
		"session_id", msg.GetSessionId(), "provider", msg.GetProvider(), "image_id", msg.GetImageId())
	return connect.NewResponse(&podiumv1.PutSandboxSnapshotResponse{}), nil
}
