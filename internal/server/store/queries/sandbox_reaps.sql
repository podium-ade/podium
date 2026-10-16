-- name: QueueSandboxReap :one
insert into sandbox_reaps (provider, sandbox_id, task_id, reason)
select sandbox_provider, sandbox_id, id, @reason::text from tasks
where id = @task_id and sandbox_id <> '' and sandbox_deleted_at is null
on conflict do nothing
returning provider;

-- name: ListSandboxReaps :many
select * from sandbox_reaps where provider = @provider order by created_at;

-- name: DeleteSandboxReap :execrows
delete from sandbox_reaps where provider = @provider and sandbox_id = @sandbox_id;

-- name: MarkSandboxDeleted :execrows
update tasks set sandbox_deleted_at = now()
where sandbox_provider = @provider and sandbox_id = @sandbox_id and sandbox_deleted_at is null;

-- name: SandboxesKept :many
-- Sandboxes still up with no task running in them: those of finished tasks no node has
-- said are deleted, and queued deletions. One whose node never says so stops counting
-- grace_seconds after its task finished or its preview was released; a held preview counts until
-- it is released.
select provider, count(distinct sandbox_id)::int as kept from (
  select t.sandbox_provider as provider, t.sandbox_id from tasks t
  left join previews p on p.task_id = t.id
  where t.sandbox_id <> '' and t.sandbox_deleted_at is null and t.finished_at is not null
    and (t.finished_at > now() - make_interval(secs => @grace_seconds::int)
      or (p.expires_at is not null and p.released_at is null)
      or p.released_at > now() - make_interval(secs => @grace_seconds::int))
  union all
  select provider, sandbox_id from sandbox_reaps
) k
group by provider;
