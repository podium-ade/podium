package node

import (
	"context"
	"errors"
	"fmt"
	"io"

	"connectrpc.com/connect"

	"github.com/alvaroibarguen/podium/internal/node/docker"
	podiumv1 "github.com/alvaroibarguen/podium/internal/proto/podium/v1"
)

// artifactChunkBytes is how much of an artifact one stream message carries.
const artifactChunkBytes = 1 << 20

// artifactUploader is the node's implementation of docker.ArtifactUploader: it streams a
// file the executor copied out of a container through NodeService.UploadArtifact.
//
// The node never talks to the object store. It has no credential for one and, under the
// tailnet transport, no route to one either — the control plane is the only thing a worker
// is ever expected to reach.
type artifactUploader struct{ n *Node }

// UploadArtifact streams one artifact to the control plane and returns what was stored.
func (u artifactUploader) UploadArtifact(ctx context.Context, up docker.ArtifactUpload) (docker.ArtifactStored, error) {
	stream := u.n.client.UploadArtifact(ctx)
	err := stream.Send(&podiumv1.UploadArtifactRequest{
		Msg: &podiumv1.UploadArtifactRequest_Metadata{Metadata: &podiumv1.ArtifactMetadata{
			NodeId:      u.n.id.NodeID,
			NodeKey:     u.n.id.NodeKey,
			TaskId:      up.TaskID,
			LeaseId:     up.LeaseID,
			Name:        up.Name,
			ContentType: up.ContentType,
			SizeBytes:   up.Size,
		}},
	})
	if err != nil {
		return docker.ArtifactStored{}, uploadError(stream, err)
	}

	buf := make([]byte, artifactChunkBytes)
	for {
		n, rerr := up.Body.Read(buf)
		if n > 0 {
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			if serr := stream.Send(&podiumv1.UploadArtifactRequest{
				Msg: &podiumv1.UploadArtifactRequest_Chunk{Chunk: chunk},
			}); serr != nil {
				return docker.ArtifactStored{}, uploadError(stream, serr)
			}
		}
		if rerr != nil {
			if errors.Is(rerr, io.EOF) {
				break
			}
			_, _ = stream.CloseAndReceive()
			return docker.ArtifactStored{}, fmt.Errorf("read artifact %s: %w", up.Name, rerr)
		}
	}

	res, err := stream.CloseAndReceive()
	if err != nil {
		return docker.ArtifactStored{}, err
	}
	return docker.ArtifactStored{
		ID:        res.Msg.GetArtifactId(),
		ObjectKey: res.Msg.GetObjectKey(),
		Size:      res.Msg.GetSizeBytes(),
	}, nil
}

// uploadError closes a half-open stream and prefers the server's own answer to the write
// error, which for a rejected upload is only ever "broken pipe".
func uploadError(stream *connect.ClientStreamForClient[podiumv1.UploadArtifactRequest, podiumv1.UploadArtifactResponse], err error) error {
	if _, rerr := stream.CloseAndReceive(); rerr != nil {
		return rerr
	}
	return err
}
