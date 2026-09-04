-- MarkRelayed is the whole of the relay's exactly-once guarantee: the insert either takes
-- the (task_id, seq) pair or finds it already taken, and the caller posts only when it
-- took it. A replayed stream returns no rows and therefore says nothing twice.
-- name: MarkRelayed :one
insert into relayed (task_id, seq) values (@task_id, @seq)
on conflict (task_id, seq) do nothing
returning seq;

-- name: MaxRelayedSeq :one
select coalesce(max(seq), 0)::bigint as high_seq from relayed where task_id = @task_id;

-- name: CountRelayed :one
select count(*)::bigint from relayed where task_id = @task_id;
