-- name: AppendLogChunk :batchexec
insert into task_log_chunks (task_id, seq, stream, sidecar, ts, bytes, source_offset)
values (@task_id, @seq, @stream, @sidecar, @ts, @bytes, @source_offset)
on conflict (task_id, seq) do nothing;

-- name: MaxLogChunkSeq :one
select coalesce(max(seq), 0)::bigint as high_seq from task_log_chunks where task_id = @task_id;

-- name: ListLogChunks :many
select * from task_log_chunks
where task_id = @task_id and seq >= @from_seq
order by seq
limit @page_limit::int;

-- TaskStreamOffsets is the reconciliation answer for one task: how far into each of the
-- container's own streams the control plane has committed. Sidecar chunks are excluded —
-- an adopted task's sidecars are never re-attached, so their offsets mean nothing.
--
-- The stored roll-up watermarks are folded in with greatest() so the answer cannot go
-- backwards when a rolled-up task's chunks are pruned. A node resumed from a smaller
-- offset would re-send the whole container log under fresh sequence numbers, which is
-- exactly the duplicate step 12 exists to have fixed.
-- name: TaskStreamOffsets :one
select
  greatest(
    (select coalesce(max(c.source_offset), 0) from task_log_chunks c
      where c.task_id = @task_id and c.stream = 'stdout' and coalesce(c.sidecar, '') = ''),
    t.logs_stdout_offset)::bigint as stdout_offset,
  greatest(
    (select coalesce(max(c.source_offset), 0) from task_log_chunks c
      where c.task_id = @task_id and c.stream = 'stderr' and coalesce(c.sidecar, '') = ''),
    t.logs_stderr_offset)::bigint as stderr_offset
from tasks t
where t.id = @task_id;

-- MaxTaskSeq folds in tasks.logs_high_seq for the same reason TaskStreamOffsets folds in
-- the offsets: pruning a rolled-up task's chunks must not let the high-water mark drop, or
-- a synthetic event would collide with a sequence number that is already spent.
-- name: MaxTaskSeq :one
select greatest(
  (select coalesce(max(e.seq), 0) from task_events e     where e.task_id = @task_id),
  (select coalesce(max(c.seq), 0) from task_log_chunks c where c.task_id = @task_id),
  (select coalesce(max(t.logs_high_seq), 0) from tasks t where t.id = @task_id)
)::bigint as high_seq;
