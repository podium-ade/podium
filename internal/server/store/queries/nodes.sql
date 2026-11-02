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

-- ClaimNodeSession records that server holds node's stream.
-- name: ClaimNodeSession :exec
update nodes set session_server = @server where id = @id;

-- EndNodeSession marks a node unreachable as its stream ends, unless another server has its
-- stream by now: a node that reconnected elsewhere is not unreachable.
-- name: EndNodeSession :execrows
update nodes set status = 'unreachable', session_server = ''
where id = @id and session_server = @server;

-- NodesHeldElsewhere is how many nodes another server holds and has heard from recently.
-- name: NodesHeldElsewhere :one
select count(*)::int from nodes
where session_server <> '' and session_server <> @server
  and last_heartbeat_at > now() - make_interval(secs => @fresh_seconds::int);

-- ActiveSandboxTasks is how many tasks each sandbox provider's nodes are working on, across
-- every server: a provider's cap is the provider's, whichever server its nodes stream to.
-- name: ActiveSandboxTasks :many
select l.provider::text as provider, count(*)::int as active
from tasks t
join nodes n on n.id = t.node_id
cross join lateral (
  select substr(v, length('sandbox.provider=') + 1) as provider
  from jsonb_array_elements_text(n.labels) v
  where v like 'sandbox.provider=%'
) l
where t.status in ('scheduled', 'provisioning', 'running')
group by l.provider;

-- name: EnqueueNodeCommand :one
insert into node_commands (node_id, payload) values (@node_id, @payload) returning id;

-- TakeNodeCommand is the holder's claim on a command: whoever deletes it sends it.
-- name: TakeNodeCommand :one
delete from node_commands where id = @id and node_id = any(@node_ids::text[])
returning payload;

-- name: NodeCommandWaiting :one
select exists(select 1 from node_commands where id = @id);

-- name: DropNodeCommand :execrows
delete from node_commands where id = @id;

-- name: PruneNodeCommands :exec
delete from node_commands where created_at < now() - interval '1 minute';
