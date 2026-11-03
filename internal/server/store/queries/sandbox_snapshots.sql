-- name: GetSandboxSnapshot :one
select * from sandbox_snapshots where session_id = @session_id;

-- name: UpsertSandboxSnapshot :one
insert into sandbox_snapshots (session_id, provider, image_id, node_id, task_id, updated_at)
values (@session_id, @provider, @image_id, @node_id, @task_id, now())
on conflict (session_id) do update set
  provider   = excluded.provider,
  image_id   = excluded.image_id,
  node_id    = excluded.node_id,
  task_id    = excluded.task_id,
  updated_at = now()
returning *;

-- name: LockSandboxSnapshot :one
select * from sandbox_snapshots where session_id = @session_id for update;

-- name: ListSandboxSnapshots :many
-- in_use is a task of the session that has not finished, as for workspace_snapshots.
select s.*, exists (
  select 1 from tasks t
  where t.status in ('queued', 'scheduled', 'provisioning', 'running')
    and t.spec->>'workspace_session' = s.session_id
)::boolean as in_use
from sandbox_snapshots s
order by s.updated_at desc, s.session_id;

-- name: DeleteSandboxSnapshot :execrows
-- Only while the row still points at image_id: a newer image that landed in between is not
-- the one chosen for deletion.
delete from sandbox_snapshots where session_id = @session_id and image_id = @image_id;

-- name: RetireSandboxSnapshot :exec
insert into sandbox_snapshots_retired (image_id, provider) values (@image_id, @provider)
on conflict do nothing;

-- name: ListRetiredSandboxSnapshots :many
select * from sandbox_snapshots_retired order by retired_at;

-- name: DeleteRetiredSandboxSnapshot :exec
delete from sandbox_snapshots_retired where image_id = @image_id;

-- name: FailRetiredSandboxSnapshot :exec
update sandbox_snapshots_retired set attempts = attempts + 1, last_error = @last_error
where image_id = @image_id;

-- name: GetSandboxSnapshotRetention :one
select * from sandbox_snapshot_retention where id = 1;

-- name: SetSandboxSnapshotRetention :one
insert into sandbox_snapshot_retention (id, max_age_seconds, max_count, updated_by, updated_at)
values (1, @max_age_seconds, @max_count, @updated_by, now())
on conflict (id) do update set
  max_age_seconds = excluded.max_age_seconds,
  max_count       = excluded.max_count,
  updated_by      = excluded.updated_by,
  updated_at      = now()
returning *;
