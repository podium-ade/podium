-- name: AppendLogChunk :batchexec
insert into task_log_chunks (task_id, seq, stream, sidecar, ts, bytes)
values (@task_id, @seq, @stream, @sidecar, @ts, @bytes)
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
