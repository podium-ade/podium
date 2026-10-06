-- name: GetWorkspaceSnapshot :one
select * from workspace_snapshots where session_id = @session_id;

-- name: UpsertWorkspaceSnapshot :one
insert into workspace_snapshots (
  session_id, object_key, size_bytes, sha256, node_id, task_id, updated_at
) values (
  @session_id, @object_key, @size_bytes, @sha256, @node_id, @task_id, now()
)
on conflict (session_id) do update set
  object_key = excluded.object_key,
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
