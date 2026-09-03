-- name: CreateTask :one
insert into tasks (id, spec, status, priority, requested_by, max_attempts)
values (@id, @spec, @status, @priority, @requested_by, @max_attempts)
returning *;

-- name: GetTask :one
select * from tasks where id = @id;

-- name: GetTaskForUpdate :one
select * from tasks where id = @id for update;

-- name: ListTasks :many
select * from tasks
where (cardinality(@statuses::text[]) = 0 or status = any (@statuses::text[]))
  and (@node_id::text = '' or node_id = @node_id::text)
  and (@requested_by::text = '' or requested_by = @requested_by::text)
  and (@after_id::text = '' or id < @after_id::text)
order by id desc
limit @page_limit::int;

-- ClaimQueuedTasks must run inside a transaction: FOR UPDATE SKIP LOCKED only partitions
-- candidates between concurrent claimers for as long as their transactions stay open.
-- name: ClaimQueuedTasks :many
select * from tasks
where status = 'queued'
order by priority desc, created_at
for update skip locked
limit @page_limit::int;

-- ClaimActiveTasks is the reconciliation sweep: every task the control plane still owes
-- someone an answer for. It is deliberately not paginated — a control plane with more than
-- a few thousand tasks in flight has a bigger problem than this query.
-- name: ClaimActiveTasks :many
select * from tasks
where status in ('scheduled', 'provisioning', 'running')
order by id;

-- name: ListTasksOnNode :many
select * from tasks
where node_id = @node_id and status = any (@statuses::text[])
order by id;

-- name: MarkScheduleAttempt :exec
update tasks
set last_schedule_attempt_at = now(),
    queued_reason            = @queued_reason::text
where id = @id and status = 'queued';

-- RequestCancel records a durable stop intent. TransitionTask reads it back and rewrites
-- the terminal status the node's finished event would otherwise have produced, so a server
-- restart between the request and the event cannot lose it.
-- name: RequestCancel :one
update tasks
set cancel_requested_at = coalesce(cancel_requested_at, now()),
    cancel_reason       = coalesce(cancel_reason, @cancel_reason::text),
    cancel_status       = coalesce(cancel_status, @cancel_status::text)
where id = @id
  and status in ('queued', 'scheduled', 'provisioning', 'running')
returning *;

-- ExtendLease pushes a live task's lease out. It is guarded by the lease id as well as the
-- id, so a stale scheduler cannot extend a lease that has already moved to another node.
-- name: ExtendLease :execrows
update tasks
set lease_expires_at = @lease_expires_at
where id = @id
  and lease_id = @lease_id
  and status in ('scheduled', 'provisioning', 'running');

-- name: AssignTask :one
update tasks
set status           = 'scheduled',
    queued_reason    = null,
    node_id          = @node_id,
    lease_id         = @lease_id,
    lease_expires_at = @lease_expires_at,
    scheduled_at     = now(),
    attempts         = attempts + 1
where id = @id and status = 'queued'
returning *;

-- UpdateTaskTransition applies the patch columns; a nil patch field leaves the column alone.
-- Requeueing (to_status = 'queued') always clears the node and lease bookkeeping.
-- name: UpdateTaskTransition :one
update tasks
set status           = @to_status::text,
    queued_reason    = null,
    started_at       = coalesce(sqlc.narg(started_at)::timestamptz, started_at),
    finished_at      = coalesce(sqlc.narg(finished_at)::timestamptz, finished_at),
    exit_code        = coalesce(sqlc.narg(exit_code)::int, exit_code),
    usage            = coalesce(sqlc.narg(usage)::jsonb, usage),
    failure_reason   = coalesce(sqlc.narg(failure_reason)::text, failure_reason),
    node_id          = case when @to_status::text = 'queued' then null
                            else coalesce(sqlc.narg(node_id)::text, node_id) end,
    lease_id         = case when @to_status::text = 'queued' then null
                            else coalesce(sqlc.narg(lease_id)::text, lease_id) end,
    lease_expires_at = case when @to_status::text = 'queued' then null
                            else coalesce(sqlc.narg(lease_expires_at)::timestamptz, lease_expires_at) end
where id = @id
returning *;
