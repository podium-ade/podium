package nodes

import (
	"context"
	"errors"
	"fmt"
	"io"

	"connectrpc.com/connect"

	podiumv1 "github.com/podium-ade/podium/internal/proto/podium/v1"
	"github.com/podium-ade/podium/internal/server/snapshots"
	"github.com/podium-ade/podium/internal/server/store"
)

// SetSnapshots wires in workspace snapshot storage. Nil means the control plane has no
// object store, and both RPCs refuse the call.
func (s *Service) SetSnapshots(snaps *snapshots.Service) { s.snapshots = snaps }

// UploadWorkspaceSnapshot stores one workspace tar. The node streams it. The server is
// the only process that talks to S3, same as UploadArtifact.
func (s *Service) UploadWorkspaceSnapshot(
	ctx context.Context,
	stream *connect.ClientStream[podiumv1.UploadWorkspaceSnapshotRequest],
) (*connect.Response[podiumv1.UploadWorkspaceSnapshotResponse], error) {
	if s.snapshots == nil || !s.snapshots.Enabled() {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			errors.New("upload workspace snapshot: this control plane has no object store configured (PODIUM_S3_ENDPOINT)"))
	}
	if !stream.Receive() {
		if err := stream.Err(); err != nil {
			return nil, err
		}
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("upload workspace snapshot: empty stream"))
	}
	meta := stream.Msg().GetMetadata()
	if meta == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			errors.New("upload workspace snapshot: the first message must be the metadata"))
	}
	if err := s.authorizeSnapshot(ctx, meta.GetNodeId(), meta.GetNodeKey(), meta.GetTaskId()); err != nil {
		return nil, err
	}
	stored, err := s.snapshots.Put(ctx, snapshots.Upload{
		SessionID: meta.GetSessionId(),
		Repo:      meta.GetRepo(),
		NodeID:    meta.GetNodeId(),
		TaskID:    meta.GetTaskId(),
		Size:      meta.GetSizeBytes(),
		Body:      &snapshotChunkReader{stream: stream},
	})
	if err != nil {
		code := connect.CodeUnavailable
		if errors.Is(err, snapshots.ErrTooLarge) {
			code = connect.CodeInvalidArgument
		}
		return nil, connect.NewError(code, fmt.Errorf("upload workspace snapshot: %w", err))
	}
	s.logger.InfoContext(ctx, "workspace snapshot stored",
		"node_id", meta.GetNodeId(), "task_id", meta.GetTaskId(),
		"session_id", meta.GetSessionId(), "repo", meta.GetRepo(),
		"key", stored.ObjectKey, "bytes", stored.SizeBytes)
	return connect.NewResponse(&podiumv1.UploadWorkspaceSnapshotResponse{
		ObjectKey: stored.ObjectKey,
		SizeBytes: stored.SizeBytes,
		Sha256:    stored.SHA256,
	}), nil
}

// DownloadWorkspaceSnapshot streams the latest tar. A missing snapshot is found=false,
// not an error: a new session has nothing to restore.
func (s *Service) DownloadWorkspaceSnapshot(
	ctx context.Context,
	req *connect.Request[podiumv1.DownloadWorkspaceSnapshotRequest],
	stream *connect.ServerStream[podiumv1.DownloadWorkspaceSnapshotResponse],
) error {
	if s.snapshots == nil || !s.snapshots.Enabled() {
		return connect.NewError(connect.CodeFailedPrecondition,
			errors.New("download workspace snapshot: this control plane has no object store configured (PODIUM_S3_ENDPOINT)"))
	}
	msg := req.Msg
	if err := s.authorizeSnapshot(ctx, msg.GetNodeId(), msg.GetNodeKey(), msg.GetTaskId()); err != nil {
		return err
	}
	row, body, err := s.snapshots.Open(ctx, msg.GetSessionId(), msg.GetRepo())
	if errors.Is(err, snapshots.ErrNotFound) {
		return stream.Send(&podiumv1.DownloadWorkspaceSnapshotResponse{
			Msg: &podiumv1.DownloadWorkspaceSnapshotResponse_Info{
				Info: &podiumv1.WorkspaceSnapshotInfo{Found: false},
			},
		})
	}
	if err != nil {
		return connect.NewError(connect.CodeUnavailable, fmt.Errorf("download workspace snapshot: %w", err))
	}
	defer body.Close()
	if err := stream.Send(&podiumv1.DownloadWorkspaceSnapshotResponse{
		Msg: &podiumv1.DownloadWorkspaceSnapshotResponse_Info{
			Info: &podiumv1.WorkspaceSnapshotInfo{
				Found:     true,
				ObjectKey: row.ObjectKey,
				SizeBytes: row.SizeBytes,
				Sha256:    row.SHA256,
			},
		},
	}); err != nil {
		return err
	}
	buf := make([]byte, 1<<20)
	for {
		n, rerr := body.Read(buf)
		if n > 0 {
			chunk := append([]byte(nil), buf[:n]...)
			if err := stream.Send(&podiumv1.DownloadWorkspaceSnapshotResponse{
				Msg: &podiumv1.DownloadWorkspaceSnapshotResponse_Chunk{Chunk: chunk},
			}); err != nil {
				return err
			}
		}
		if rerr == io.EOF {
			return nil
		}
		if rerr != nil {
			return connect.NewError(connect.CodeUnavailable, fmt.Errorf("download workspace snapshot: %w", rerr))
		}
	}
}

func (s *Service) authorizeSnapshot(ctx context.Context, nodeID, nodeKey, taskID string) error {
	if nodeID == "" || nodeKey == "" {
		return connect.NewError(connect.CodeUnauthenticated,
			errors.New("workspace snapshot: node_id and node_key are required"))
	}
	node, err := s.store.GetNodeByKeyHash(ctx, store.HashToken(nodeKey))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return connect.NewError(connect.CodeUnauthenticated, errors.New("workspace snapshot: unknown node key"))
		}
		return connect.NewError(connect.CodeInternal, fmt.Errorf("workspace snapshot: %w", err))
	}
	if node.ID != nodeID {
		return connect.NewError(connect.CodeUnauthenticated, errors.New("workspace snapshot: node key does not match node_id"))
	}
	task, err := s.store.GetTask(ctx, taskID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return connect.NewError(connect.CodeNotFound, fmt.Errorf("workspace snapshot: no task %s", taskID))
		}
		return connect.NewError(connect.CodeInternal, fmt.Errorf("workspace snapshot: %w", err))
	}
	if task.NodeID != node.ID {
		return connect.NewError(connect.CodePermissionDenied,
			fmt.Errorf("workspace snapshot: task %s is not on node %s", task.ID, node.ID))
	}
	return nil
}

type snapshotChunkReader struct {
	stream *connect.ClientStream[podiumv1.UploadWorkspaceSnapshotRequest]
	buf    []byte
	done   bool
	err    error
}

func (r *snapshotChunkReader) Read(p []byte) (int, error) {
	for len(r.buf) == 0 {
		if r.done {
			if r.err != nil {
				return 0, r.err
			}
			return 0, io.EOF
		}
		if !r.stream.Receive() {
			r.done = true
			r.err = r.stream.Err()
			continue
		}
		msg := r.stream.Msg()
		if msg.GetMetadata() != nil {
			r.done = true
			r.err = errors.New("upload workspace snapshot: metadata may only be the first message")
			continue
		}
		r.buf = msg.GetChunk()
	}
	n := copy(p, r.buf)
	r.buf = r.buf[n:]
	return n, nil
}
