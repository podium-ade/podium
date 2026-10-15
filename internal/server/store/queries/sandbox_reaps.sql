-- name: QueueSandboxReap :one
insert into sandbox_reaps (provider, sandbox_id, task_id, reason)
select sandbox_provider, sandbox_id, id, @reason::text from tasks
where id = @task_id and sandbox_id <> ''
on conflict do nothing
returning provider;

-- name: ListSandboxReaps :many
select * from sandbox_reaps where provider = @provider order by created_at;

-- name: DeleteSandboxReap :execrows
delete from sandbox_reaps where provider = @provider and sandbox_id = @sandbox_id;
