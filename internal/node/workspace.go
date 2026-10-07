package node

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"connectrpc.com/connect"

	"github.com/podium-ade/podium/internal/node/docker"
	podiumv1 "github.com/podium-ade/podium/internal/proto/podium/v1"
	"github.com/podium-ade/podium/pkg/spec"
)

// workspaceHooks is how this node restores and stores a session volume. Nil means the
// task's volume stays ephemeral. The node streams the tar through the control plane and
// never talks to the object store itself.
func (n *Node) workspaceHooks(taskID string, sp *spec.TaskSpec) *docker.Workspace {
	if sp == nil {
		return nil
	}
	session := strings.TrimSpace(sp.WorkspaceSession)
	repo := strings.TrimSpace(sp.WorkspaceRepo)
	if session == "" && repo == "" {
		return nil
	}
	publish := sp.WorkspacePublishBase && repo != ""
	ws := &docker.Workspace{
		Restore: func(ctx context.Context) (io.ReadCloser, bool, error) {
			return n.downloadWorkspace(ctx, taskID, session, repo)
		},
	}
	// A repo on its own only seeds the volume. Publishing, or a session, is what stores.
	if session != "" || publish {
		ws.Save = func(ctx context.Context, tarPath string, size int64) error {
			var err error
			if session != "" {
				err = n.uploadWorkspace(ctx, taskID, session, "", size, tarPath)
			}
			if publish {
				if berr := n.uploadWorkspace(ctx, taskID, "", repo, size, tarPath); err == nil {
					err = berr
				}
			}
			return err
		}
	}
	return ws
}

func (n *Node) uploadWorkspace(ctx context.Context, taskID, session, repo string, size int64, tarPath string) error {
	f, err := os.Open(tarPath)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	stream := n.client.UploadWorkspaceSnapshot(ctx)
	err = stream.Send(&podiumv1.UploadWorkspaceSnapshotRequest{
		Msg: &podiumv1.UploadWorkspaceSnapshotRequest_Metadata{Metadata: &podiumv1.WorkspaceSnapshotMetadata{
			NodeId:    n.id.NodeID,
			NodeKey:   n.id.NodeKey,
			TaskId:    taskID,
			SessionId: session,
			Repo:      repo,
			SizeBytes: size,
		}},
	})
	if err != nil {
		return snapshotUploadError(stream, err)
	}

	buf := make([]byte, artifactChunkBytes)
	for {
		read, rerr := f.Read(buf)
		if read > 0 {
			chunk := make([]byte, read)
			copy(chunk, buf[:read])
			if serr := stream.Send(&podiumv1.UploadWorkspaceSnapshotRequest{
				Msg: &podiumv1.UploadWorkspaceSnapshotRequest_Chunk{Chunk: chunk},
			}); serr != nil {
				return snapshotUploadError(stream, serr)
			}
		}
		if rerr != nil {
			if errors.Is(rerr, io.EOF) {
				break
			}
			_, _ = stream.CloseAndReceive()
			return fmt.Errorf("read workspace tar: %w", rerr)
		}
	}
	if _, err := stream.CloseAndReceive(); err != nil {
		if connect.CodeOf(err) == connect.CodeFailedPrecondition {
			n.logger.Info("workspace snapshot skipped", "task_id", taskID, "error", err)
			return nil
		}
		return err
	}
	return nil
}

func snapshotUploadError(stream *connect.ClientStreamForClient[podiumv1.UploadWorkspaceSnapshotRequest, podiumv1.UploadWorkspaceSnapshotResponse], err error) error {
	if _, rerr := stream.CloseAndReceive(); rerr != nil {
		if connect.CodeOf(rerr) == connect.CodeFailedPrecondition {
			return nil
		}
		return rerr
	}
	if connect.CodeOf(err) == connect.CodeFailedPrecondition {
		return nil
	}
	return err
}

func (n *Node) downloadWorkspace(ctx context.Context, taskID, session, repo string) (io.ReadCloser, bool, error) {
	stream, err := n.client.DownloadWorkspaceSnapshot(ctx, connect.NewRequest(&podiumv1.DownloadWorkspaceSnapshotRequest{
		NodeId:    n.id.NodeID,
		NodeKey:   n.id.NodeKey,
		TaskId:    taskID,
		SessionId: session,
		Repo:      repo,
	}))
	if err != nil {
		if connect.CodeOf(err) == connect.CodeFailedPrecondition {
			n.logger.Info("workspace restore skipped", "task_id", taskID, "error", err)
			return nil, false, nil
		}
		return nil, false, err
	}
	if !stream.Receive() {
		_ = stream.Close()
		if err := stream.Err(); err != nil {
			if connect.CodeOf(err) == connect.CodeFailedPrecondition {
				n.logger.Info("workspace restore skipped", "task_id", taskID, "error", err)
				return nil, false, nil
			}
			return nil, false, err
		}
		return nil, false, errors.New("download workspace snapshot: empty stream")
	}
	info := stream.Msg().GetInfo()
	if info == nil {
		_ = stream.Close()
		return nil, false, errors.New("download workspace snapshot: the first message must be the info")
	}
	if !info.GetFound() {
		_ = stream.Close()
		return nil, false, nil
	}
	return &workspaceDownload{stream: stream}, true, nil
}

// workspaceDownload reads the chunk messages that follow the info message.
type workspaceDownload struct {
	stream *connect.ServerStreamForClient[podiumv1.DownloadWorkspaceSnapshotResponse]
	buf    []byte
	done   bool
	err    error
}

func (d *workspaceDownload) Read(p []byte) (int, error) {
	for len(d.buf) == 0 {
		if d.done {
			if d.err != nil {
				return 0, d.err
			}
			return 0, io.EOF
		}
		if !d.stream.Receive() {
			d.done = true
			d.err = d.stream.Err()
			continue
		}
		msg := d.stream.Msg()
		if msg.GetInfo() != nil {
			d.done = true
			d.err = errors.New("download workspace snapshot: info may only be the first message")
			continue
		}
		d.buf = msg.GetChunk()
	}
	n := copy(p, d.buf)
	d.buf = d.buf[n:]
	return n, nil
}

func (d *workspaceDownload) Close() error { return d.stream.Close() }
