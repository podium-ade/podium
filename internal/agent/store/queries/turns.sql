-- name: CreateTurn :one
insert into turns (id, session_id, task_id, trigger_ref, status, started_at)
values (@id, @session_id, @task_id, @trigger_ref, @status, @started_at)
returning *;

-- name: SetTurnTask :exec
update turns set task_id = @task_id where id = @id;

-- name: FinishTurn :exec
update turns
set status      = @status,
    finished_at = @finished_at,
    num_turns   = @num_turns,
    cost_usd    = @cost_usd,
    final_text  = @final_text
where id = @id;

-- name: ListTurns :many
select * from turns
where session_id = @session_id
order by started_at desc, id desc
limit @page_limit::int;

-- name: ListRunningTurns :many
select * from turns where status = 'running' order by started_at;

-- name: GetTurn :one
select * from turns where id = @id;
