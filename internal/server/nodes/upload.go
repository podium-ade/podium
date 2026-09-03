package nodes

import (
	"context"
	"errors"
	"fmt"
	"io"

	"connectrpc.com/connect"

	podiumv1 "github.com/alvaroibarguen/podium/internal/proto/podium/v1"
	"github.com/alvaroibarguen/podium/internal/server/artifacts"
	"github.com/alvaroibarguen/podium/internal/server/store"
)

// SetArtifacts wires in the object store. It is a setter because server.New builds the
// node service before it knows whether an object store is configured.
func (s *Service) SetArtifacts(a *artifacts.Service) { s.artifacts = a }

// UploadArtifact takes one file a task produced and puts it in the object store.
//
// The node is the only party that can reach the container, and the server is the only
// party that can reach S3, so the bytes cross the control plane. That is deliberate: a
// presigned PUT straight from the node would be one fewer hop and would also mean every
// worker needs a route and a credential to the object store, which is exactly the
// invariant the rest of the networking design is built to avoid.
//
// The first message must carry the metadata, with the same node_id and node_key Hello
// presents — an upload is its own HTTP request and carries no session. Every message after
// it is a chunk, and the body is streamed straight through to S3: nothing here buffers a
// whole artifact.
func (s *Service) UploadArtifact(
	ctx context.Context,
	stream *connect.ClientStream[podiumv1.UploadArtifactRequest],
) (*connect.Response[podiumv1.UploadArtifactResponse], error) {
	if s.artifacts == nil || !s.artifacts.Enabled() {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			errors.New("upload artifact: this control plane has no object store configured (PODIUM_S3_ENDPOINT)"))
	}
	if !stream.Receive() {
		if err := stream.Err(); err != nil {
			return nil, err
		}
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("upload artifact: empty stream"))
	}
	meta := stream.Msg().GetMetadata()
	if meta == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			errors.New("upload artifact: the first message must be the metadata"))
	}
	if err := s.authorizeUpload(ctx, meta); err != nil {
		return nil, err
	}
	if meta.GetName() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("upload artifact: name is required"))
	}

	art, err := s.artifacts.Put(ctx, artifacts.Upload{
		TaskID:      meta.GetTaskId(),
		Kind:        meta.GetKind(),
		Name:        meta.GetName(),
		ContentType: meta.GetContentType(),
		Size:        meta.GetSizeBytes(),
		Body:        &chunkReader{stream: stream},
	})
	if err != nil {
		return nil, connect.NewError(uploadCode(err), fmt.Errorf("upload artifact: %w", err))
	}
	s.logger.InfoContext(ctx, "node artifact stored",
		"node_id", meta.GetNodeId(), "task_id", art.TaskID, "artifact_id", art.ID,
		"name", art.Name, "bytes", art.SizeBytes)

	return connect.NewResponse(&podiumv1.UploadArtifactResponse{
		ArtifactId: art.ID,
		ObjectKey:  art.ObjectKey,
		SizeBytes:  art.SizeBytes,
		Sha256:     art.SHA256,
	}), nil
}

// uploadCode maps a failed upload onto a Connect code. A rejection the node should not
// retry is InvalidArgument; anything else is Unavailable, because the object store being
// down is a transient condition and the node's own error event says as much.
func uploadCode(err error) connect.Code {
	if errors.Is(err, artifacts.ErrTooLarge) || errors.Is(err, store.ErrNotFound) {
		return connect.CodeInvalidArgument
	}
	return connect.CodeUnavailable
}

// authorizeUpload checks the upload is coming from the node that holds the task. An
// upload is a separate request from the stream, so the node key is re-presented and
// checked exactly as Hello's is.
func (s *Service) authorizeUpload(ctx context.Context, meta *podiumv1.ArtifactMetadata) error {
	if meta.GetNodeId() == "" || meta.GetNodeKey() == "" {
		return connect.NewError(connect.CodeUnauthenticated,
			errors.New("upload artifact: node_id and node_key are required"))
	}
	node, err := s.store.GetNodeByKeyHash(ctx, store.HashToken(meta.GetNodeKey()))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return connect.NewError(connect.CodeUnauthenticated, errors.New("upload artifact: unknown node key"))
		}
		return connect.NewError(connect.CodeInternal, fmt.Errorf("upload artifact: %w", err))
	}
	if node.ID != meta.GetNodeId() {
		return connect.NewError(connect.CodeUnauthenticated, errors.New("upload artifact: node key does not match node_id"))
	}

	task, err := s.store.GetTask(ctx, meta.GetTaskId())
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return connect.NewError(connect.CodeNotFound, fmt.Errorf("upload artifact: no task %s", meta.GetTaskId()))
		}
		return connect.NewError(connect.CodeInternal, fmt.Errorf("upload artifact: %w", err))
	}
	if task.NodeID != node.ID {
		return connect.NewError(connect.CodePermissionDenied,
			fmt.Errorf("upload artifact: task %s is not on node %s", task.ID, node.ID))
	}
	return nil
}

// chunkReader turns the rest of a client stream into an io.Reader, so the object store
// pulls the body straight off the wire.
type chunkReader struct {
	stream *connect.ClientStream[podiumv1.UploadArtifactRequest]
	buf    []byte
	done   bool
	err    error
}

func (r *chunkReader) Read(p []byte) (int, error) {
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
			r.err = errors.New("upload artifact: metadata may only be the first message")
			continue
		}
		r.buf = msg.GetChunk()
	}
	n := copy(p, r.buf)
	r.buf = r.buf[n:]
	return n, nil
}
