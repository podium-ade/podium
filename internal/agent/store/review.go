package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	db "github.com/podium-ade/podium/internal/agent/store/db"
)

// ReviewSurfaceSlack is the kind a Slack thread is stored under on review_surfaces.
const ReviewSurfaceSlack = "slack"

// ErrSurfaceBound is a Slack thread (or other surface) that is already reviewing a
// different pull request. One thread is one review; a second PR URL in the same thread
// is refused rather than silently switching.
var ErrSurfaceBound = errors.New("this surface is already bound to another pull request")

// ReviewSurface is one door into a GitHub pull-request review session.
type ReviewSurface struct {
	SourceKey string
	Kind      string
	Ref       string
	CreatedAt time.Time
}

// BindReviewSurface records that ref (for kind) is a door into sourceKey. A first bind
// takes; binding the same pair again is a no-op; binding a ref that already points at a
// different sourceKey is ErrSurfaceBound.
func (s *Store) BindReviewSurface(ctx context.Context, sourceKey, kind, ref string) error {
	switch {
	case sourceKey == "":
		return errors.New("bind review surface: a source key is required")
	case kind == "":
		return errors.New("bind review surface: a kind is required")
	case ref == "":
		return errors.New("bind review surface: a ref is required")
	}
	if err := s.q.BindReviewSurface(ctx, db.BindReviewSurfaceParams{
		SourceKey: sourceKey,
		Kind:      kind,
		Ref:       ref,
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		return fmt.Errorf("bind review surface %s %s: %w", kind, ref, err)
	}
	got, err := s.GetReviewSurface(ctx, kind, ref)
	if err != nil {
		return err
	}
	if got.SourceKey != sourceKey {
		return fmt.Errorf("%w: %s", ErrSurfaceBound, got.SourceKey)
	}
	return nil
}

// GetReviewSurface looks up one surface. ErrNotFound means it is not bound.
func (s *Store) GetReviewSurface(ctx context.Context, kind, ref string) (ReviewSurface, error) {
	row, err := s.q.GetReviewSurface(ctx, db.GetReviewSurfaceParams{Kind: kind, Ref: ref})
	if noRows(err) {
		return ReviewSurface{}, fmt.Errorf("%w: review surface %s %s", ErrNotFound, kind, ref)
	}
	if err != nil {
		return ReviewSurface{}, fmt.Errorf("get review surface %s %s: %w", kind, ref, err)
	}
	return ReviewSurface{
		SourceKey: row.SourceKey,
		Kind:      row.Kind,
		Ref:       row.Ref,
		CreatedAt: row.CreatedAt.UTC(),
	}, nil
}

// ListReviewSurfaces returns every surface of kind bound to sourceKey, oldest first.
func (s *Store) ListReviewSurfaces(ctx context.Context, sourceKey, kind string) ([]ReviewSurface, error) {
	rows, err := s.q.ListReviewSurfaces(ctx, db.ListReviewSurfacesParams{
		SourceKey: sourceKey,
		Kind:      kind,
	})
	if err != nil {
		return nil, fmt.Errorf("list review surfaces %s %s: %w", sourceKey, kind, err)
	}
	out := make([]ReviewSurface, 0, len(rows))
	for _, r := range rows {
		out = append(out, ReviewSurface{
			SourceKey: r.SourceKey,
			Kind:      r.Kind,
			Ref:       r.Ref,
			CreatedAt: r.CreatedAt.UTC(),
		})
	}
	return out, nil
}
