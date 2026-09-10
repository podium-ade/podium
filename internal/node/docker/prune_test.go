package docker

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/docker/docker/api/types/image"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeImages is a Docker client that records what it was asked to remove and removes
// nothing. Every test here runs against it: the prune tests must never be able to delete a
// real image, because this build machine's engine holds the operator's own unrelated work.
type fakeImages struct {
	removed []string
	err     error
}

func (f *fakeImages) ImageRemove(_ context.Context, id string, _ image.RemoveOptions) ([]image.DeleteResponse, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.removed = append(f.removed, id)
	return []image.DeleteResponse{{Deleted: id}}, nil
}

func rec(ref string, size int64, lastUsed time.Time) ImageRecord {
	return ImageRecord{Ref: ref, ID: "sha256:" + ref, SizeBytes: size, LastUsedAt: lastUsed}
}

func TestImageCacheRecordsOnlyWhatPodiumPulled(t *testing.T) {
	dir := t.TempDir()
	c := NewImageCache(dir)

	assert.False(t, c.Owns("some-api:latest"), "an empty cache owns nothing")

	c.Pulled("alpine:3", "sha256:aaa", 8<<20)
	assert.True(t, c.Owns("alpine:3"))
	assert.False(t, c.Owns("some-api:latest"),
		"an image on the same engine that podium never pulled is never ours")

	// Using an image nobody pulled does not adopt it.
	c.Used("some-api:latest")
	assert.False(t, c.Owns("some-api:latest"))

	// It survives a restart, which is what makes the allow-list durable.
	reloaded := NewImageCache(dir)
	assert.True(t, reloaded.Owns("alpine:3"))
	assert.False(t, reloaded.Owns("some-api:latest"))
	require.FileExists(t, filepath.Join(dir, ImageCacheFile))
}

func TestImageCacheSurvivesACorruptFileByForgettingEverything(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, ImageCacheFile), []byte("{not json"), 0o600))

	c := NewImageCache(dir)
	assert.Empty(t, c.Records(), "an unreadable cache owns nothing, so the prune does nothing")
}

// The load-bearing test: the function that calls ImageRemove refuses anything the cache
// did not record, whatever the caller passes it.
func TestRemoveOwnedImageRefusesAnImagePodiumDidNotPull(t *testing.T) {
	cache := NewImageCache(t.TempDir())
	cache.Pulled("alpine:3", "sha256:alpine", 8<<20)
	cli := &fakeImages{}

	stranger := ImageRecord{Ref: "some-api:latest", ID: "sha256:stranger", SizeBytes: 4 << 30}
	err := removeOwnedImage(context.Background(), cli, cache, stranger)
	require.ErrorIs(t, err, ErrNotPodiumImage)
	assert.Contains(t, err.Error(), "some-api:latest")
	assert.Empty(t, cli.removed, "ImageRemove must not even be called for a stranger's image")

	// And the one it does own goes through, by ID rather than by ref.
	require.NoError(t, removeOwnedImage(context.Background(), cli, cache,
		ImageRecord{Ref: "alpine:3", ID: "sha256:alpine"}))
	assert.Equal(t, []string{"sha256:alpine"}, cli.removed)
	assert.False(t, cache.Owns("alpine:3"), "a removed image leaves the allow-list")
}

func TestRemoveOwnedImageKeepsAnImageTheEngineRefusedToDelete(t *testing.T) {
	cache := NewImageCache(t.TempDir())
	cache.Pulled("alpine:3", "sha256:alpine", 1)
	cli := &fakeImages{err: errors.New("Error response from daemon: No such image: sha256:alpine")}

	// An engine error the SDK does not classify as not-found is reported, and the image
	// stays in the allow-list: it is still ours and it is still there as far as we know.
	// (An error the SDK *does* classify as not-found drops it, so a phantom does not come
	// back as a candidate on every sweep.)
	err := removeOwnedImage(context.Background(), cli, cache, ImageRecord{Ref: "alpine:3", ID: "sha256:alpine"})
	require.Error(t, err)
	assert.True(t, cache.Owns("alpine:3"))
}

func TestSelectForPruneDoesNothingBelowTheWatermark(t *testing.T) {
	old := time.Now().Add(-90 * 24 * time.Hour)
	records := []ImageRecord{rec("a:1", 1<<30, old), rec("b:1", 1<<30, old)}

	got := SelectForPrune(records, PruneRequest{
		DiskUsedFraction: 0.42,
		HighWatermark:    0.80,
		TotalBytes:       100 << 30,
	})
	assert.Empty(t, got, "a disk that is 42%% full is not a reason to delete anything")
}

func TestSelectForPruneTakesTheLeastRecentlyUsedFirst(t *testing.T) {
	now := time.Now()
	records := []ImageRecord{
		rec("fresh:1", 5<<30, now),
		rec("ancient:1", 5<<30, now.Add(-30*24*time.Hour)),
		rec("stale:1", 5<<30, now.Add(-7*24*time.Hour)),
	}

	// 90% of 100GB used, low watermark 70% -> 20GB to free -> the four oldest, but there
	// are only three, and the freshest is only reached once the older two are not enough.
	got := SelectForPrune(records, PruneRequest{
		DiskUsedFraction: 0.90,
		HighWatermark:    0.80,
		TotalBytes:       100 << 30,
	})
	require.Len(t, got, 3)
	assert.Equal(t, []string{"ancient:1", "stale:1", "fresh:1"},
		[]string{got[0].Ref, got[1].Ref, got[2].Ref})
}

func TestSelectForPruneStopsOnceEnoughIsFreed(t *testing.T) {
	now := time.Now()
	records := []ImageRecord{
		rec("ancient:1", 30<<30, now.Add(-30*24*time.Hour)),
		rec("stale:1", 30<<30, now.Add(-7*24*time.Hour)),
		rec("fresh:1", 30<<30, now),
	}
	got := SelectForPrune(records, PruneRequest{
		DiskUsedFraction: 0.85,
		HighWatermark:    0.80,
		TotalBytes:       100 << 30, // 15GB to free; the first image is already more
	})
	require.Len(t, got, 1)
	assert.Equal(t, "ancient:1", got[0].Ref)
}

func TestSelectForPruneNeverTakesAnImageARunningTaskNeeds(t *testing.T) {
	now := time.Now()
	records := []ImageRecord{
		rec("ancient:1", 30<<30, now.Add(-30*24*time.Hour)),
		rec("stale:1", 30<<30, now.Add(-7*24*time.Hour)),
	}
	got := SelectForPrune(records, PruneRequest{
		DiskUsedFraction: 0.99,
		HighWatermark:    0.80,
		TotalBytes:       100 << 30,
		InUse:            []string{"ancient:1"},
	})
	require.Len(t, got, 1)
	assert.Equal(t, "stale:1", got[0].Ref, "the image a task is running is never a candidate")
}

func TestSelectForPruneOnlyEverSeesPodiumsOwnImages(t *testing.T) {
	// The engine this runs on holds images that belong to other work. They are absent
	// from the cache, so they are absent from the candidate list — the selection function
	// is never even shown them, and removeOwnedImage would refuse them if it were.
	cache := NewImageCache(t.TempDir())
	cache.Pulled("alpine:3", "sha256:alpine", 8<<20)

	got := SelectForPrune(cache.Records(), PruneRequest{
		DiskUsedFraction: 0.99,
		HighWatermark:    0.80,
		TotalBytes:       100 << 30,
	})
	require.Len(t, got, 1)
	assert.Equal(t, "alpine:3", got[0].Ref)
}
