package artifacts_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/server/artifacts"
	"github.com/podium-ade/podium/internal/server/artifacts/fakes3"
)

func newS3(t *testing.T) (*artifacts.S3, *fakes3.Server) {
	t.Helper()
	fake := fakes3.Start(t)
	s3, err := artifacts.NewS3(fake.Config())
	require.NoError(t, err)
	require.NoError(t, s3.EnsureBucket(context.Background()))
	return s3, fake
}

func TestEnsureBucketIsIdempotent(t *testing.T) {
	s3, _ := newS3(t)
	ctx := context.Background()
	require.NoError(t, s3.EnsureBucket(ctx))
	require.NoError(t, s3.Ready(ctx))
}

func TestPutAndGetRoundTrip(t *testing.T) {
	s3, _ := newS3(t)
	ctx := context.Background()

	// Larger than one chunk of anything, so the streaming upload path is what is tested.
	body := bytes.Repeat([]byte("podium artifact bytes\n"), 20_000)
	key := artifacts.ArtifactKey("task_01j", "art_01j", "report.txt")
	require.Equal(t, "tasks/task_01j/artifacts/art_01j-report.txt", key)

	n, err := s3.Put(ctx, key, bytes.NewReader(body), int64(len(body)), "text/plain")
	require.NoError(t, err)
	require.Equal(t, int64(len(body)), n)

	rc, err := s3.Get(ctx, key)
	require.NoError(t, err)
	defer func() { _ = rc.Close() }()
	got, err := io.ReadAll(rc)
	require.NoError(t, err)
	require.Equal(t, body, got, "the object must come back byte-identical")
}

// TestPutOfAnEmptyObjectSucceeds pins the one case a declared size must not be rounded
// away: an empty file is a legitimate artifact — the agent runtime's transcript.jsonl is
// one whenever a turn produces no SDK message — and its length is known exactly. Passing
// -1 for it instead makes minio-go stream with no Content-Length, which S3 refuses.
func TestPutOfAnEmptyObjectSucceeds(t *testing.T) {
	s3, _ := newS3(t)
	ctx := context.Background()
	key := artifacts.ArtifactKey("task_01j", "art_01j", "transcript.jsonl")

	n, err := s3.Put(ctx, key, bytes.NewReader(nil), 0, "application/x-ndjson")
	require.NoError(t, err)
	require.Zero(t, n)

	rc, err := s3.Get(ctx, key)
	require.NoError(t, err)
	defer func() { _ = rc.Close() }()
	got, err := io.ReadAll(rc)
	require.NoError(t, err)
	require.Empty(t, got)
}

func TestGetOfAMissingObjectFails(t *testing.T) {
	s3, _ := newS3(t)
	_, err := s3.Get(context.Background(), "tasks/nope/artifacts/nope")
	require.Error(t, err)
}

func TestPresignedGetIsSignedAndFetchable(t *testing.T) {
	s3, fake := newS3(t)
	ctx := context.Background()

	body := []byte("presigned bytes")
	// A name with a space and a slash in it: the key is sanitised, so the signature has
	// to survive percent-encoding in the canonical URI.
	key := artifacts.ArtifactKey("task_01j", "art_01j", "shots/first shot.png")
	require.Equal(t, "tasks/task_01j/artifacts/art_01j-shots_first_shot.png", key)
	_, err := s3.Put(ctx, key, bytes.NewReader(body), int64(len(body)), "image/png")
	require.NoError(t, err)

	u, err := s3.PresignGet(ctx, key, "first shot.png", artifacts.PresignTTL)
	require.NoError(t, err)
	require.Contains(t, u.RawQuery, "X-Amz-Signature=")

	resp, err := http.Get(u.String()) //nolint:noctx // a test fetching a presigned URL
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	got, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(got))
	require.Equal(t, body, got)
	require.Zero(t, fake.Rejected)
}

func TestATamperedPresignedURLIsRefused(t *testing.T) {
	s3, fake := newS3(t)
	ctx := context.Background()
	key := artifacts.ArtifactKey("task_01j", "art_01j", "r.txt")
	_, err := s3.Put(ctx, key, strings.NewReader("x"), 1, "text/plain")
	require.NoError(t, err)

	u, err := s3.PresignGet(ctx, key, "r.txt", artifacts.PresignTTL)
	require.NoError(t, err)

	for name, tamper := range map[string]func(string) string{
		"signature": func(raw string) string {
			return strings.Replace(raw, "X-Amz-Signature=", "X-Amz-Signature=0", 1)
		},
		"key": func(raw string) string {
			return strings.Replace(raw, "art_01j-r.txt", "art_01j-other.txt", 1)
		},
	} {
		t.Run(name, func(t *testing.T) {
			resp, err := http.Get(tamper(u.String())) //nolint:noctx // a test fetching a presigned URL
			require.NoError(t, err)
			defer func() { _ = resp.Body.Close() }()
			require.Equal(t, http.StatusForbidden, resp.StatusCode,
				"a presigned URL that has been altered must not work")
		})
	}
	require.Equal(t, 2, fake.Rejected)
}

func TestAnExpiredPresignedURLIsRefused(t *testing.T) {
	s3, _ := newS3(t)
	ctx := context.Background()
	key := artifacts.ArtifactKey("task_01j", "art_01j", "r.txt")
	_, err := s3.Put(ctx, key, strings.NewReader("x"), 1, "text/plain")
	require.NoError(t, err)

	u, err := s3.PresignGet(ctx, key, "r.txt", time.Second)
	require.NoError(t, err)
	time.Sleep(1100 * time.Millisecond)

	resp, err := http.Get(u.String()) //nolint:noctx // a test fetching a presigned URL
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusForbidden, resp.StatusCode)
}

func TestPresignedPutIsSignedAndAccepted(t *testing.T) {
	s3, fake := newS3(t)
	ctx := context.Background()
	key := artifacts.ArtifactKey("task_01j", "art_01j", "uploaded.txt")

	u, err := s3.PresignPut(ctx, key, "text/plain", 1024, artifacts.PresignTTL)
	require.NoError(t, err)

	body := "uploaded through a presigned put"
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, u.String(), strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "text/plain")
	req.ContentLength = int64(len(body))
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Zero(t, fake.Rejected)

	rc, err := s3.Get(ctx, key)
	require.NoError(t, err)
	defer func() { _ = rc.Close() }()
	got, err := io.ReadAll(rc)
	require.NoError(t, err)
	require.Equal(t, body, string(got))
}

func TestPresignPutRefusesAnOversizedUpload(t *testing.T) {
	s3, _ := newS3(t)
	_, err := s3.PresignPut(context.Background(), "tasks/t/artifacts/x", "text/plain",
		artifacts.MaxArtifactBytes+1, artifacts.PresignTTL)
	require.Error(t, err)
}

func TestAnUnreachableEndpointIsNotReady(t *testing.T) {
	s3, err := artifacts.NewS3(fakes3.DeadConfig(t))
	require.NoError(t, err, "building the client must not need the endpoint")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.Error(t, s3.Ready(ctx))
}

func TestObjectKeyLayout(t *testing.T) {
	require.Equal(t, "tasks/task_1/logs/stdout.log.zst", artifacts.LogKey("task_1", "stdout"))
	require.Equal(t, "tasks/task_1/logs/sidecar-db.log.zst", artifacts.LogKey("task_1", "sidecar-db"))
}

func TestSanitizeName(t *testing.T) {
	for in, want := range map[string]string{
		"report.txt":       "report.txt",
		"shots/a b.png":    "shots_a_b.png",
		"../../etc/passwd": "etc_passwd",
		"..":               "artifact",
		"":                 "artifact",
		"   ":              "artifact",
		"a$b&c":            "a_b_c",
	} {
		require.Equal(t, want, artifacts.SanitizeName(in), "sanitizing %q", in)
	}
	require.Len(t, artifacts.SanitizeName(strings.Repeat("a", 400)), 128)
}

func TestConfigFromEndpointURL(t *testing.T) {
	t.Setenv("PODIUM_S3_ENDPOINT", "https://objectstore.example:9000")
	t.Setenv("PODIUM_S3_BUCKET", "b")
	t.Setenv("PODIUM_S3_ACCESS_KEY", "a")
	t.Setenv("PODIUM_S3_SECRET_KEY", "s")
	cfg := artifacts.ConfigFromEnv()
	require.Equal(t, "objectstore.example:9000", cfg.Endpoint)
	require.True(t, cfg.UseSSL)
	require.Equal(t, artifacts.DefaultRegion, cfg.Region)
	require.NoError(t, cfg.Validate())
}

func TestConfigWithoutAnEndpointIsDisabledAndValid(t *testing.T) {
	var cfg artifacts.Config
	require.False(t, cfg.Enabled())
	require.NoError(t, cfg.Validate())
}

func TestConfigWithAnEndpointNeedsABucketAndCredentials(t *testing.T) {
	require.Error(t, artifacts.Config{Endpoint: "127.0.0.1:9000"}.Validate())
	require.Error(t, artifacts.Config{Endpoint: "127.0.0.1:9000", Bucket: "b"}.Validate())
}
