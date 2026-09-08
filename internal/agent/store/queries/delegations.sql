-- name: CreateDelegation :one
insert into delegations (id, session_id, turn_id, trigger_ref, playbook, instruction, status, created_at)
values (@id, @session_id, @turn_id, @trigger_ref, @playbook, @instruction, @status, @created_at)
returning *;

-- name: SetDelegationTask :exec
update delegations set task_id = @task_id where id = @id;

-- name: FinishDelegation :exec
update delegations
set status      = @status,
    finished_at = @finished_at,
    final_text  = @final_text
where id = @id;

-- name: GetDelegation :one
select * from delegations where id = @id;

-- name: ListDelegationsForTurn :many
select * from delegations where turn_id = @turn_id order by created_at, id;

-- name: ListRunningDelegations :many
select * from delegations where status = 'running' order by created_at;

-- name: ListRunningDelegationsForRef :many
select * from delegations
where trigger_ref = @trigger_ref and status = 'running'
order by created_at;
