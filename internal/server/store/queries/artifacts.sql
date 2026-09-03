-- name: CreateArtifact :one
insert into artifacts (id, task_id, kind, name, object_key, size_bytes, content_type, sha256)
values (@id, @task_id, @kind, @name, @object_key, @size_bytes, @content_type, @sha256)
returning *;

-- name: ListArtifacts :many
select * from artifacts where task_id = @task_id order by created_at, id;

-- name: GetArtifact :one
select * from artifacts where id = @id;

-- ListArtifactsByKind is how the roll-up finds the objects it wrote for a task, so a
-- pruned task's logs can be served back from the object store.
-- name: ListArtifactsByKind :many
select * from artifacts where task_id = @task_id and kind = @kind order by name;

-- TasksPendingLogRollUp is every terminal task whose logs are still only in Postgres.
-- finished_at is null for a task the control plane wrote off without a node event, so
-- coalesce it to created_at rather than skipping the task forever.
-- name: TasksPendingLogRollUp :many
select id from tasks
where logs_rolled_up_at is null
  and status in ('succeeded', 'failed', 'cancelled', 'lost')
  and coalesce(finished_at, created_at) < @finished_before::timestamptz
order by coalesce(finished_at, created_at)
limit @page_limit::int;

-- name: MarkLogsRolledUp :exec
update tasks
set logs_rolled_up_at  = now(),
    logs_high_seq      = greatest(logs_high_seq, @high_seq),
    logs_stdout_offset = greatest(logs_stdout_offset, @stdout_offset),
    logs_stderr_offset = greatest(logs_stderr_offset, @stderr_offset)
where id = @id;

-- PruneRolledUpLogChunks drops the hot rows of tasks whose logs are safely in the object
-- store and have been for longer than the grace period. It is deliberately scoped by task
-- rather than by chunk age: a task still running after the horizon would otherwise have
-- its own log truncated under it.
-- name: PruneRolledUpLogChunks :execrows
delete from task_log_chunks c
using tasks t
where c.task_id = t.id
  and t.logs_rolled_up_at is not null
  and t.logs_rolled_up_at < @older_than::timestamptz;

-- name: TaskLogRollUp :one
select logs_rolled_up_at, logs_high_seq, logs_stdout_offset, logs_stderr_offset
from tasks where id = @id;
