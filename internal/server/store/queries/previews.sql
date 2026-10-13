-- UpsertPreview records where a task's ports were published. The node reports it once per
-- run, before the command starts, so a row that is already there belongs to an earlier
-- attempt and this one replaces it.
-- name: UpsertPreview :one
insert into previews (task_id, node_id, via, address, urls, ttl_ms)
values (@task_id, @node_id, @via, @address, @urls, @ttl_ms)
on conflict (task_id) do update
set node_id = excluded.node_id, via = excluded.via, address = excluded.address,
    urls = excluded.urls, ttl_ms = excluded.ttl_ms,
    expires_at = null, released_at = null, release_reason = ''
returning *;

-- OrphanedPreviews is every live preview whose task ended without its command finishing
-- held — cancelled, lost, failed before it ran — so there is nothing on a node to keep.
-- name: OrphanedPreviews :many
select p.* from previews p join tasks t on t.id = p.task_id
where p.released_at is null and p.expires_at is null
  and t.status in ('succeeded', 'failed', 'cancelled', 'lost')
limit 100;

-- name: GetPreview :one
select * from previews where task_id = @task_id;

-- name: ListPreviews :many
select * from previews where task_id = any(@task_ids::text[]);

-- HoldPreview starts a live preview's ttl, from the moment its command exited.
-- name: HoldPreview :one
update previews
set expires_at = sqlc.arg(exited_at)::timestamptz + ttl_ms * interval '1 millisecond'
where task_id = @task_id and released_at is null and expires_at is null
returning *;

-- name: ReleasePreview :one
update previews
set released_at = now(), release_reason = @release_reason
where task_id = @task_id and released_at is null
returning *;

-- name: ExpiredPreviews :many
select * from previews
where released_at is null and expires_at is not null and expires_at <= @now::timestamptz
order by expires_at
limit 100;
