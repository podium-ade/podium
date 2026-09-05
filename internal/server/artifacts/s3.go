package artifacts

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// PresignTTL is how long a presigned URL is good for.
const PresignTTL = 15 * time.Minute

// maxNameLength caps the sanitised half of an object key.
const maxNameLength = 128

// S3 is a bucket on an S3-compatible endpoint. It is safe for concurrent use.
type S3 struct {
	cli    *minio.Client
	bucket string
	// ready latches the last bucket probe so /readyz does not have to round-trip on every
	// call, and so a failed start is visible rather than fatal.
	ready atomic.Bool
}

// NewS3 builds a client for cfg. It does not touch the network: the endpoint may be down
// when the server starts and that must not stop it, because a task does not need
// artifacts to run.
func NewS3(cfg Config) (*S3, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if !cfg.Enabled() {
		return nil, errors.New("artifacts: no PODIUM_S3_ENDPOINT configured")
	}
	cli, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure: cfg.UseSSL,
		Region: cfg.Region,
		// Path style: an object store in a compose network has no wildcard DNS, and a bucket name
		// with a dot in it breaks virtual-host style against TLS anyway.
		BucketLookup: minio.BucketLookupPath,
	})
	if err != nil {
		return nil, fmt.Errorf("artifacts: build s3 client: %w", err)
	}
	return &S3{cli: cli, bucket: cfg.Bucket}, nil
}

// Bucket is the bucket every key lives in.
func (s *S3) Bucket() string { return s.bucket }

// EnsureBucket creates the bucket if it is missing. It is called at start and again by
// every readiness probe that finds the store unready, so an endpoint that comes up late
// heals without a restart.
func (s *S3) EnsureBucket(ctx context.Context) error {
	exists, err := s.cli.BucketExists(ctx, s.bucket)
	if err != nil {
		s.ready.Store(false)
		return fmt.Errorf("artifacts: reach bucket %s: %w", s.bucket, err)
	}
	if !exists {
		if err := s.cli.MakeBucket(ctx, s.bucket, minio.MakeBucketOptions{}); err != nil {
			// Another server may have won the race.
			if ok, existsErr := s.cli.BucketExists(ctx, s.bucket); existsErr != nil || !ok {
				s.ready.Store(false)
				return fmt.Errorf("artifacts: create bucket %s: %w", s.bucket, err)
			}
		}
	}
	s.ready.Store(true)
	return nil
}

// Ready is the /readyz probe: the object store is reachable and the bucket is there.
func (s *S3) Ready(ctx context.Context) error { return s.EnsureBucket(ctx) }

// Put streams size bytes from r to key. A negative size streams until EOF, which costs a
// multipart upload; the upload paths all know their length.
func (s *S3) Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) (int64, error) {
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	opts := minio.PutObjectOptions{ContentType: contentType}
	if size == 0 {
		// An empty artifact is a real one — the agent runtime's transcript.jsonl is empty
		// whenever a turn produces no SDK message — and it needs one option of its own.
		// Over plain HTTP minio-go signs a PUT with the streaming signature, which hands
		// net/http a non-nil body and a ContentLength of 0; net/http reads that pair as "I
		// do not know the length" and sends Transfer-Encoding: chunked with no
		// Content-Length, which S3 answers with 411 MissingContentLength. Without the
		// streaming signer the zero-length body is dropped altogether and the request goes
		// out as the Content-Length: 0 PUT it always was. The payload hash it gives up is
		// the hash of nothing.
		opts.DisableContentSha256 = true
	}
	info, err := s.cli.PutObject(ctx, s.bucket, key, r, size, opts)
	if err != nil {
		s.ready.Store(false)
		return 0, fmt.Errorf("artifacts: put %s: %w", key, err)
	}
	return info.Size, nil
}

// Get opens an object for reading. The caller closes it.
func (s *S3) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	obj, err := s.cli.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("artifacts: get %s: %w", key, err)
	}
	// GetObject is lazy: it does not talk to the endpoint until the first read, so a
	// missing object or an unreachable endpoint surfaces here rather than at the caller's
	// first Read, where it would already have written a 200.
	if _, err := obj.Stat(); err != nil {
		_ = obj.Close()
		return nil, fmt.Errorf("artifacts: get %s: %w", key, err)
	}
	return obj, nil
}

// Remove deletes an object. It is only used to unwind a half-finished upload.
func (s *S3) Remove(ctx context.Context, key string) error {
	if err := s.cli.RemoveObject(ctx, s.bucket, key, minio.RemoveObjectOptions{}); err != nil {
		return fmt.Errorf("artifacts: remove %s: %w", key, err)
	}
	return nil
}

// PresignGet mints a short-lived download URL. filename, when set, becomes the
// Content-Disposition the object store sends back, so a browser saves the artifact under
// the name the task gave it rather than under its key.
func (s *S3) PresignGet(ctx context.Context, key, filename string, ttl time.Duration) (*url.URL, error) {
	if ttl <= 0 {
		ttl = PresignTTL
	}
	params := url.Values{}
	if filename != "" {
		params.Set("response-content-disposition", `attachment; filename="`+SanitizeName(filename)+`"`)
	}
	u, err := s.cli.PresignedGetObject(ctx, s.bucket, key, ttl, params)
	if err != nil {
		return nil, fmt.Errorf("artifacts: presign get %s: %w", key, err)
	}
	return u, nil
}

// PresignPut mints a short-lived upload URL bound to a content type.
//
// Nothing in Podium uses it yet: the node upload path goes through the server
// (NodeService.UploadArtifact) precisely so that a node never needs a route to S3. It is
// the interface that direct-to-S3 upload would be built on, and it is what makes the
// signing path testable from both ends. maxSize is advisory — a presigned PUT cannot
// carry a size limit in the URL, only a POST policy can — so the caller must still refuse
// anything larger when it records the artifact.
func (s *S3) PresignPut(ctx context.Context, key, contentType string, maxSize int64, ttl time.Duration) (*url.URL, error) {
	if ttl <= 0 {
		ttl = PresignTTL
	}
	if maxSize > 0 && maxSize > MaxArtifactBytes {
		return nil, fmt.Errorf("artifacts: %d bytes is over the %d byte limit", maxSize, MaxArtifactBytes)
	}
	if contentType == "" {
		return s.cli.PresignedPutObject(ctx, s.bucket, key, ttl)
	}
	// A signed Content-Type header is what binds the upload to a type: PresignedPutObject
	// signs nothing but the host.
	u, err := s.cli.PresignHeader(ctx, "PUT", s.bucket, key, ttl, url.Values{},
		map[string][]string{"Content-Type": {contentType}})
	if err != nil {
		return nil, fmt.Errorf("artifacts: presign put %s: %w", key, err)
	}
	return u, nil
}

// ArtifactKey is where one of a task's files lives: tasks/<task>/artifacts/<id>-<name>.
// The artifact ID makes the key unique even when two files share a name, and the
// sanitised name makes it readable in a bucket browser.
func ArtifactKey(taskID, artifactID, name string) string {
	return path.Join("tasks", taskID, "artifacts", artifactID+"-"+SanitizeName(name))
}

// LogKey is where a rolled-up log stream lives: tasks/<task>/logs/<stream>.log.zst.
// stream is "stdout", "stderr", or "sidecar-<name>".
func LogKey(taskID, stream string) string {
	return path.Join("tasks", taskID, "logs", SanitizeName(stream)+".log.zst")
}

// LogContentType is what a rolled-up log stream is stored as.
const LogContentType = "text/plain+zstd"

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// SanitizeName reduces a producer's name to [A-Za-z0-9._-], at most 128 characters. The
// original is kept in the database; this is only what reaches the object key.
//
// A path separator becomes an underscore rather than a slash, so an auto-collected
// "shots/a.png" cannot climb out of its task's prefix, and a leading dot is dropped so a
// name can never be ".." or an invisible file.
func SanitizeName(name string) string {
	name = unsafeName.ReplaceAllString(strings.TrimSpace(name), "_")
	name = strings.Trim(name, "._-")
	if name == "" {
		name = "artifact"
	}
	if len(name) > maxNameLength {
		name = name[:maxNameLength]
	}
	return name
}
