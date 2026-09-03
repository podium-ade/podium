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

-- name: PruneLogChunks :execrows
delete from task_log_chunks where ts < @older_than;

-- TaskStreamOffsets is the reconciliation answer for one task: how far into each of the
-- container's own streams the control plane has committed. Sidecar chunks are excluded —
-- an adopted task's sidecars are never re-attached, so their offsets mean nothing.
-- name: TaskStreamOffsets :many
select stream, coalesce(max(source_offset), 0)::bigint as source_offset
from task_log_chunks
where task_id = @task_id and coalesce(sidecar, '') = ''
group by stream;

-- name: MaxTaskSeq :one
select greatest(
  (select coalesce(max(e.seq), 0) from task_events e     where e.task_id = @task_id),
  (select coalesce(max(c.seq), 0) from task_log_chunks c where c.task_id = @task_id)
)::bigint as high_seq;
