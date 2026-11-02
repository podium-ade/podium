-- name: GetWorkspaceSnapshot :one
select * from workspace_snapshots where session_id = @session_id;

-- name: UpsertWorkspaceSnapshot :one
insert into workspace_snapshots (
  session_id, object_key, size_bytes, sha256, node_id, task_id, base_key, updated_at
) values (
  @session_id, @object_key, @size_bytes, @sha256, @node_id, @task_id, @base_key, now()
)
on conflict (session_id) do update set
  object_key = excluded.object_key,
  base_key   = excluded.base_key,
  size_bytes = excluded.size_bytes,
  sha256     = excluded.sha256,
  node_id    = excluded.node_id,
  task_id    = excluded.task_id,
  updated_at = now()
returning *;

-- name: GetWorkspaceBase :one
select * from workspace_bases where repo = @repo;

-- name: UpsertWorkspaceBase :one
insert into workspace_bases (repo, object_key, size_bytes, sha256, updated_at)
values (@repo, @object_key, @size_bytes, @sha256, now())
on conflict (repo) do update set
  object_key = excluded.object_key,
  size_bytes = excluded.size_bytes,
  sha256     = excluded.sha256,
  updated_at = now()
returning *;

-- name: ListWorkspaceSnapshots :many
-- in_use is a task of the session that has not finished. Its snapshot is either being
-- restored or about to be replaced, so the sweep leaves it alone.
select s.*, exists (
  select 1 from tasks t
  where t.status in ('queued', 'scheduled', 'provisioning', 'running')
    and t.spec->>'workspace_session' = s.session_id
)::boolean as in_use
from workspace_snapshots s
order by s.updated_at desc, s.session_id;

-- name: DeleteWorkspaceSnapshot :execrows
-- Only while the row still points at object_key: an upload that landed in between has
-- replaced it, and the new snapshot is not the one that was chosen for deletion.
delete from workspace_snapshots where session_id = @session_id and object_key = @object_key;

-- name: GetSnapshotRetention :one
select * from snapshot_retention where id = 1;

-- name: SetSnapshotRetention :one
insert into snapshot_retention (id, max_age_seconds, max_total_bytes, updated_by, updated_at)
values (1, @max_age_seconds, @max_total_bytes, @updated_by, now())
on conflict (id) do update set
  max_age_seconds = excluded.max_age_seconds,
  max_total_bytes = excluded.max_total_bytes,
  updated_by      = excluded.updated_by,
  updated_at      = now()
returning *;

-- name: SetSessionOrigin :exec
insert into workspace_session_origins (session_id, base_key, recorded_at)
values (@session_id, @base_key, now())
on conflict (session_id) do update set base_key = excluded.base_key, recorded_at = now();

-- name: GetSessionOrigin :one
select base_key from workspace_session_origins where session_id = @session_id;

-- name: DeleteSessionOrigin :exec
delete from workspace_session_origins where session_id = @session_id;

-- name: RetireWorkspaceBase :exec
insert into workspace_bases_retired (object_key) values (@object_key) on conflict do nothing;

-- name: ListUnreferencedRetiredBases :many
-- A retired base nothing restores from any more: no snapshot is a difference from it, and no
-- session that started from it has uploaded yet.
select r.object_key from workspace_bases_retired r
where not exists (select 1 from workspace_snapshots s where s.base_key = r.object_key)
  and not exists (select 1 from workspace_session_origins o where o.base_key = r.object_key);

-- name: DeleteRetiredBase :exec
delete from workspace_bases_retired where object_key = @object_key;
