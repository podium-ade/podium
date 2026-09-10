-- name: CreateNode :one
insert into nodes (id, name, tags, labels, capacity, node_key_hash, status, version, ts_stable_id)
values (@id, @name, @tags, @labels, @capacity, @node_key_hash, @status, @version, sqlc.narg(ts_stable_id)::text)
returning *;

-- name: GetNode :one
select * from nodes where id = @id;

-- name: GetNodeByKeyHash :one
select * from nodes where node_key_hash = @node_key_hash;

-- name: ListNodes :many
select * from nodes order by created_at, id;

-- name: UpdateNodeHeartbeat :one
update nodes
set status            = @status,
    capacity          = coalesce(sqlc.narg(capacity)::jsonb, capacity),
    version           = coalesce(sqlc.narg(version)::text, version),
    last_heartbeat_at = now()
where id = @id
returning *;

-- name: SetNodeStatus :execrows
update nodes set status = @status where id = @id;

-- name: DeleteNode :execrows
delete from nodes where id = @id;

-- name: SetNodeTSStableID :execrows
update nodes set ts_stable_id = sqlc.narg(ts_stable_id)::text where id = @id;

-- name: SetNodeDraining :execrows
update nodes set draining = @draining where id = @id;

-- name: SetNodeMaxTasks :execrows
update nodes set max_tasks_override = sqlc.narg(max_tasks_override)::int where id = @id;

-- name: SetNodeLabels :one
update nodes set labels = @labels where id = @id
returning *;
