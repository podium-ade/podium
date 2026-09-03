package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"sync"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/image"
)

// ImageCacheFile is where the node records the images it pulled itself, inside the data
// dir. It is both the LRU bookkeeping and — far more importantly — the allow-list.
const ImageCacheFile = "images.json"

// ErrNotPodiumImage is what the remover answers for an image Podium did not pull.
//
// This is the load-bearing safety property of the whole file, so it is worth saying why it
// exists rather than a comment on the caller. A Podium node shares its Docker engine with
// everything else on the machine: on a developer's laptop that is their own work, and on a
// shared build host it is somebody else's. "Remove images no running task references, least
// recently used first" would, on such an engine, delete images that have nothing to do with
// Podium and cannot be recovered without whatever built them. So Podium's cache is not "the
// engine's images"; it is "the images Podium pulled", recorded at the moment it pulled
// them, and nothing else is ever a candidate. The check lives in the one function that
// calls ImageRemove, not in the policy above it, so a future caller cannot get it wrong.
var ErrNotPodiumImage = errors.New("docker: image was not pulled by podium; refusing to remove it")

// LowWatermarkMargin is how far below the high watermark a prune drives disk usage before
// it stops, so a node at the line does not prune on every sweep.
const LowWatermarkMargin = 0.10

// ImageRecord is one image Podium pulled.
type ImageRecord struct {
	// Ref is what was pulled, exactly as the spec named it.
	Ref string `json:"ref"`
	// ID is the engine's image id, which is what actually gets removed. A ref that has
	// since been retagged elsewhere would otherwise take an unrelated image with it.
	ID string `json:"id"`
	// SizeBytes is what the engine reported at pull time; it is the estimate the prune
	// uses to decide how many images to remove.
	SizeBytes  int64     `json:"size_bytes"`
	PulledAt   time.Time `json:"pulled_at"`
	LastUsedAt time.Time `json:"last_used_at"`
}

// imageCacheDoc is the on-disk shape, keyed by ref.
type imageCacheDoc struct {
	Images map[string]ImageRecord `json:"images"`
}

// ImageCache is the node's record of the images it pulled, and the only thing that may
// authorise removing one. It is safe for concurrent use.
type ImageCache struct {
	path string

	mu      sync.Mutex
	records map[string]ImageRecord
}

// NewImageCache loads the cache from dir, creating an empty one when there is no file. A
// corrupt file yields an empty cache: forgetting which images are ours makes the prune do
// nothing, which is the safe direction to fail in.
func NewImageCache(dir string) *ImageCache {
	c := &ImageCache{
		path:    filepath.Join(dir, ImageCacheFile),
		records: make(map[string]ImageRecord),
	}
	raw, err := os.ReadFile(c.path) //nolint:gosec // the path is the node's own data dir
	if err != nil {
		return c
	}
	var doc imageCacheDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		return c
	}
	for ref, rec := range doc.Images {
		rec.Ref = ref
		c.records[ref] = rec
	}
	return c
}

// Pulled records that Podium pulled ref, which is the only way an image ever becomes a
// prune candidate.
func (c *ImageCache) Pulled(ref, id string, size int64) {
	now := time.Now().UTC()
	c.mu.Lock()
	c.records[ref] = ImageRecord{Ref: ref, ID: id, SizeBytes: size, PulledAt: now, LastUsedAt: now}
	c.mu.Unlock()
	c.save()
}

// Used marks an image as recently used, so the LRU order reflects what tasks actually run.
// An image Podium never pulled is not recorded: being used does not make it ours.
func (c *ImageCache) Used(ref string) {
	c.mu.Lock()
	rec, ok := c.records[ref]
	if ok {
		rec.LastUsedAt = time.Now().UTC()
		c.records[ref] = rec
	}
	c.mu.Unlock()
	if ok {
		c.save()
	}
}

// Owns reports whether ref is an image Podium pulled. It is the allow-list test.
func (c *ImageCache) Owns(ref string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.records[ref]
	return ok
}

// Records is a copy of the cache, oldest use first.
func (c *ImageCache) Records() []ImageRecord {
	c.mu.Lock()
	out := make([]ImageRecord, 0, len(c.records))
	for _, rec := range c.records {
		out = append(out, rec)
	}
	c.mu.Unlock()
	sort.Slice(out, func(i, j int) bool {
		if !out[i].LastUsedAt.Equal(out[j].LastUsedAt) {
			return out[i].LastUsedAt.Before(out[j].LastUsedAt)
		}
		return out[i].Ref < out[j].Ref
	})
	return out
}

func (c *ImageCache) forget(ref string) {
	c.mu.Lock()
	delete(c.records, ref)
	c.mu.Unlock()
	c.save()
}

// save rewrites the file. The lock is held across the write, not just the copy: two writers
// racing on one temp path would clobber each other, and this is a rare, tiny file.
func (c *ImageCache) save() {
	c.mu.Lock()
	defer c.mu.Unlock()

	doc := imageCacheDoc{Images: make(map[string]ImageRecord, len(c.records))}
	for ref, rec := range c.records {
		doc.Images[ref] = rec
	}
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(c.path), 0o700); err != nil {
		return
	}
	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return
	}
	_ = os.Rename(tmp, c.path)
}

// imageRemover is the sliver of the Docker client the prune touches. It is an interface so
// the selection and the allow-list guard can be tested against fabricated image lists,
// without an engine and without deleting anything real.
type imageRemover interface {
	ImageRemove(ctx context.Context, imageID string, options image.RemoveOptions) ([]image.DeleteResponse, error)
}

// PruneRequest is one prune decision's inputs.
type PruneRequest struct {
	// DiskUsedFraction is how full the filesystem holding the data dir is, 0..1.
	DiskUsedFraction float64
	// HighWatermark is the fraction above which pruning starts.
	HighWatermark float64
	// TotalBytes is the size of that filesystem, used to turn "how much to free" into
	// bytes. Zero disables the size arithmetic and prunes one image per sweep.
	TotalBytes int64
	// InUse are the image references running tasks need. They are never candidates,
	// whatever the cache says.
	InUse []string
}

// SelectForPrune is the whole LRU policy, as a pure function so it can be tested without
// an engine: given how full the disk is and what is in use, which of *Podium's own* images
// should go, least recently used first, and how far down that gets us.
//
// It returns nothing at all when usage is below the high watermark, when nothing is in the
// cache, or when everything in the cache is in use.
func SelectForPrune(records []ImageRecord, req PruneRequest) []ImageRecord {
	if req.HighWatermark <= 0 || req.HighWatermark >= 1 || req.DiskUsedFraction < req.HighWatermark {
		return nil
	}
	low := req.HighWatermark - LowWatermarkMargin
	if low < 0 {
		low = 0
	}
	// How many bytes have to go to get from where we are to the low watermark.
	want := int64(1)
	if req.TotalBytes > 0 {
		want = int64((req.DiskUsedFraction - low) * float64(req.TotalBytes))
	}

	ordered := slices.Clone(records)
	sort.SliceStable(ordered, func(i, j int) bool {
		if !ordered[i].LastUsedAt.Equal(ordered[j].LastUsedAt) {
			return ordered[i].LastUsedAt.Before(ordered[j].LastUsedAt)
		}
		return ordered[i].Ref < ordered[j].Ref
	})

	var out []ImageRecord
	var freed int64
	for _, rec := range ordered {
		if freed >= want {
			break
		}
		if slices.Contains(req.InUse, rec.Ref) {
			continue
		}
		out = append(out, rec)
		freed += max(rec.SizeBytes, 1)
	}
	return out
}

// removeOwnedImage is the only place in Podium that calls ImageRemove, and it refuses
// anything the cache did not record. See ErrNotPodiumImage.
func removeOwnedImage(ctx context.Context, cli imageRemover, cache *ImageCache, rec ImageRecord) error {
	if !cache.Owns(rec.Ref) {
		return fmt.Errorf("%w: %s", ErrNotPodiumImage, rec.Ref)
	}
	target := rec.ID
	if target == "" {
		target = rec.Ref
	}
	if _, err := cli.ImageRemove(ctx, target, image.RemoveOptions{}); err != nil {
		if cerrdefs.IsNotFound(err) {
			cache.forget(rec.Ref)
			return nil
		}
		return fmt.Errorf("remove image %s: %w", rec.Ref, err)
	}
	cache.forget(rec.Ref)
	return nil
}

// PruneImages removes the least recently used of *Podium's own* images until the disk is
// back under the low watermark. It never touches an image Podium did not pull, never runs
// a bulk prune of any kind, and reports what it removed.
//
// The whole feature is off unless the operator turned it on: see Config.ImageCachePrune.
func (e *Executor) PruneImages(ctx context.Context, req PruneRequest) ([]ImageRecord, error) {
	if e.images == nil {
		return nil, nil
	}
	candidates := SelectForPrune(e.images.Records(), req)
	if len(candidates) == 0 {
		return nil, nil
	}
	var removed []ImageRecord
	var errs []error
	for _, rec := range candidates {
		if err := removeOwnedImage(ctx, e.cli, e.images, rec); err != nil {
			// An image still referenced by a stopped container is a normal refusal, not
			// a problem: it just is not a candidate this time round.
			e.log.Info("image cache prune skipped an image", "ref", rec.Ref, "error", err)
			errs = append(errs, err)
			continue
		}
		e.log.Info("image cache pruned an image podium pulled",
			"ref", rec.Ref, "id", rec.ID, "size_bytes", rec.SizeBytes, "last_used_at", rec.LastUsedAt)
		removed = append(removed, rec)
	}
	if len(removed) == 0 && len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return removed, nil
}

// Images is the node's image cache, so the daemon can record use and drive the prune.
func (e *Executor) Images() *ImageCache { return e.images }
