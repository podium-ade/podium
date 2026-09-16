-- name: BindReviewSurface :exec
insert into review_surfaces (source_key, kind, ref, created_at)
values (@source_key, @kind, @ref, @created_at)
on conflict (kind, ref) do nothing;

-- name: GetReviewSurface :one
select source_key, kind, ref, created_at
from review_surfaces
where kind = @kind and ref = @ref;

-- name: ListReviewSurfaces :many
select source_key, kind, ref, created_at
from review_surfaces
where source_key = @source_key and kind = @kind
order by created_at;
