-- AppendEvent is issued as a pgx batch, so a whole event batch costs one round trip.
-- The (task_id, seq) primary key plus DO NOTHING makes a replayed batch a no-op.
-- name: AppendEvent :batchexec
insert into task_events (task_id, seq, kind, ts, payload)
values (@task_id, @seq, @kind, @ts, @payload)
on conflict (task_id, seq) do nothing;

-- name: MaxEventSeq :one
select coalesce(max(seq), 0)::bigint as high_seq from task_events where task_id = @task_id;

-- name: ListEvents :many
select * from task_events
where task_id = @task_id and seq >= @from_seq
order by seq
limit @page_limit::int;

-- name: NotifyTaskEvents :exec
select pg_notify('podium_task_events', @task_id::text);
