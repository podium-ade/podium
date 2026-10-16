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

-- name: SandboxesKept :many
-- Sandboxes still up with no task running in them: held previews and queued deletions.
select provider, count(distinct sandbox_id)::int as kept from (
  select t.sandbox_provider as provider, t.sandbox_id from previews p
  join tasks t on t.id = p.task_id
  where p.released_at is null and p.expires_at is not null and t.sandbox_id <> ''
  union all
  select provider, sandbox_id from sandbox_reaps
) k
group by provider;
