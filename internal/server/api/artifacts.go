package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	podiumv1 "github.com/alvaroibarguen/podium/internal/proto/podium/v1"
	"github.com/alvaroibarguen/podium/internal/server/artifacts"
	"github.com/alvaroibarguen/podium/internal/server/store"
)

// ArtifactDownloadPrefix is the HTTP path the server proxies artifact bytes on. It is not
// a Connect procedure on purpose: a 512 MB artifact must stream, and a unary response
// would have to hold all of it in memory at both ends.
const ArtifactDownloadPrefix = "/artifacts/"

// ArtifactService implements podium.v1.ArtifactService.
type ArtifactService struct {
	artifacts *artifacts.Service
	logger    *slog.Logger
}

// NewArtifactService returns the operator-facing artifact handlers.
func NewArtifactService(a *artifacts.Service, logger *slog.Logger) *ArtifactService {
	if logger == nil {
		logger = slog.Default()
	}
	return &ArtifactService{artifacts: a, logger: logger}
}

// ListArtifacts returns everything stored for a task, rolled-up logs included.
func (s *ArtifactService) ListArtifacts(
	ctx context.Context,
	req *connect.Request[podiumv1.ListArtifactsRequest],
) (*connect.Response[podiumv1.ListArtifactsResponse], error) {
	taskID := req.Msg.GetTaskId()
	if taskID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("list artifacts: task_id is required"))
	}
	rows, err := s.artifacts.List(ctx, taskID)
	if err != nil {
		return nil, storeError(fmt.Errorf("list artifacts: %w", err))
	}
	out := make([]*podiumv1.Artifact, 0, len(rows))
	for _, r := range rows {
		out = append(out, artifactToProto(r))
	}
	return connect.NewResponse(&podiumv1.ListArtifactsResponse{Artifacts: out}), nil
}

// GetArtifactURL mints a presigned GET. A caller with no route to the object store fetches
// the same bytes from ArtifactDownloadPrefix instead.
func (s *ArtifactService) GetArtifactURL(
	ctx context.Context,
	req *connect.Request[podiumv1.GetArtifactURLRequest],
) (*connect.Response[podiumv1.GetArtifactURLResponse], error) {
	id := req.Msg.GetArtifactId()
	if id == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("get artifact url: artifact_id is required"))
	}
	art, err := s.artifacts.Get(ctx, id)
	if err != nil {
		return nil, storeError(fmt.Errorf("get artifact url: %w", err))
	}
	u, expires, err := s.artifacts.PresignGet(ctx, art)
	if err != nil {
		if errors.Is(err, artifacts.ErrDisabled) {
			return nil, connect.NewError(connect.CodeFailedPrecondition, err)
		}
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}
	return connect.NewResponse(&podiumv1.GetArtifactURLResponse{
		Url:       u.String(),
		ExpiresAt: timestamppb.New(expires),
		Artifact:  artifactToProto(art),
	}), nil
}

// NewArtifactDownloadHandler streams an artifact's bytes through the server. It is what
// `podium artifact get` uses by default: the CLI is guaranteed a route to the control
// plane and is not guaranteed one to the object store.
func NewArtifactDownloadHandler(a *artifacts.Service, logger *slog.Logger) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		id := strings.Trim(strings.TrimPrefix(r.URL.Path, ArtifactDownloadPrefix), "/")
		if id == "" || strings.Contains(id, "/") {
			http.Error(w, "artifact id is required", http.StatusBadRequest)
			return
		}
		art, err := a.Get(r.Context(), id)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				http.Error(w, "no such artifact", http.StatusNotFound)
				return
			}
			logger.ErrorContext(r.Context(), "reading an artifact row failed", "artifact_id", id, "error", err)
			http.Error(w, "artifact lookup failed", http.StatusInternalServerError)
			return
		}
		body, err := a.Open(r.Context(), art)
		if err != nil {
			if errors.Is(err, artifacts.ErrDisabled) {
				http.Error(w, err.Error(), http.StatusServiceUnavailable)
				return
			}
			logger.ErrorContext(r.Context(), "opening an artifact failed",
				"artifact_id", id, "key", art.ObjectKey, "error", err)
			http.Error(w, "the object store did not return this artifact", http.StatusBadGateway)
			return
		}
		defer func() { _ = body.Close() }()

		contentType := art.ContentType
		if contentType == "" {
			contentType = "application/octet-stream"
		}
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Content-Length", strconv.FormatInt(art.SizeBytes, 10))
		w.Header().Set("Content-Disposition",
			`attachment; filename="`+artifacts.SanitizeName(art.Name)+`"`)
		if r.Method == http.MethodHead {
			return
		}
		if _, err := io.Copy(w, body); err != nil {
			// The status is already written; all that is left is to say so.
			logger.WarnContext(r.Context(), "artifact download ended early",
				"artifact_id", id, "error", err)
		}
	})
}

func artifactToProto(a store.Artifact) *podiumv1.Artifact {
	return &podiumv1.Artifact{
		Id:          a.ID,
		TaskId:      a.TaskID,
		Name:        a.Name,
		ObjectKey:   a.ObjectKey,
		SizeBytes:   a.SizeBytes,
		ContentType: a.ContentType,
		Sha256:      a.SHA256,
		Kind:        a.Kind,
		CreatedAt:   timestamppb.New(a.CreatedAt),
	}
}
