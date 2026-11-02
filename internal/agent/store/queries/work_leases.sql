-- AcquireLease takes work that nobody holds, or whose holder stopped renewing, or that this
-- owner already holds. It returns the row only when the caller now owns it.
-- name: AcquireLease :one
insert into work_leases (key, owner, expires_at)
values (@key, @owner, now() + make_interval(secs => @ttl_seconds::double precision))
on conflict (key) do update
    set owner = excluded.owner, expires_at = excluded.expires_at,
        pending = case when work_leases.owner = excluded.owner then work_leases.pending end,
        warm_task_id = case when work_leases.owner = excluded.owner then work_leases.warm_task_id else '' end
    where work_leases.owner = excluded.owner or work_leases.expires_at < now()
returning owner;

-- RenewLeases keeps this owner's work its own.
-- name: RenewLeases :exec
update work_leases
set expires_at = now() + make_interval(secs => @ttl_seconds::double precision)
where owner = @owner and key = any(@keys::text[]);

-- HandOffToOwner leaves a message for the live owner of a session, unless it has a warm task
-- to inject into instead, whose id is returned. No row means nobody holds the session any
-- more, and the caller takes it itself.
-- name: HandOffToOwner :one
update work_leases
set pending = case when warm_task_id = '' then @pending::jsonb else pending end
where key = @key and owner <> @caller and expires_at >= now()
returning warm_task_id;

-- ReleaseLease lets go of work only if nothing was handed off meanwhile. When a message
-- was, the row stays and TakePending hands it over.
-- name: ReleaseLease :execrows
delete from work_leases
where key = @key and owner = @owner and pending is null;

-- TakePending is the message another conductor left, cleared as it is taken.
-- name: TakePending :one
with taken as (
    select l.key, l.pending from work_leases l
    where l.key = @key and l.owner = @owner and l.pending is not null
    for update
)
update work_leases w set pending = null
from taken
where w.key = taken.key
returning taken.pending::jsonb as pending;

-- SetWarmTask records the owner's task that is waiting for a follow-up, or clears it.
-- name: SetWarmTask :exec
update work_leases set warm_task_id = @warm_task_id
where key = @key and owner = @owner;
