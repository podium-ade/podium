-- Scheduler, leases, reconciliation and drain (step 12).
--
-- Four groups of additions, all to existing tables:
--
--  1. tasks.last_schedule_attempt_at / queued_reason: why a task is still queued. Without
--     them "it is queued" is the whole of what an operator can learn about a task no node
--     can run, and the answer ("nothing carries label browser") is the only useful part.
--  2. tasks.cancel_requested_at / cancel_reason / cancel_status: cancellation becomes
--     durable. It used to be a map in server memory, so a restart between `podium task
--     cancel` and the node's finished event lost the intent and the task landed succeeded.
--     cancel_status is the terminal state the intent produces: 'cancelled' for a user
--     cancel, 'failed' for a server-side timeout, which is a stop with a different name.
--  3. task_log_chunks.source_offset: how many bytes of the container's own stream the
--     chunk ends at. Redaction rewrites the bytes that are stored, so sum(length(bytes))
--     is not the producer's offset; only the node knows it, and reconciliation hands the
--     maximum back so an adopting node resumes exactly where the last one stopped.
--  4. nodes.draining: the operator's standing "no new work here", which has to survive
--     both daemons restarting.
--
-- Plus the queue wake-up: pg_notify on every row that becomes queued, so the scheduler
-- reacts to a submission in milliseconds instead of on its next tick.

alter table tasks add column if not exists last_schedule_attempt_at timestamptz;
alter table tasks add column if not exists queued_reason            text;
alter table tasks add column if not exists cancel_requested_at      timestamptz;
alter table tasks add column if not exists cancel_reason            text;
alter table tasks add column if not exists cancel_status            text;

alter table task_log_chunks add column if not exists source_offset bigint not null default 0;

alter table nodes add column if not exists draining boolean not null default false;

-- The scheduler and the watchdog both sweep every non-terminal task on every tick, so the
-- lease bookkeeping needs an index that does not depend on node_id being set.
create index if not exists tasks_active_idx on tasks (status)
  where status in ('queued', 'scheduled', 'provisioning', 'running');

-- Reconciliation asks "what does this node hold?" with a status list, which the existing
-- partial index cannot serve because the planner cannot prove a parameter array satisfies
-- its predicate.
create index if not exists tasks_node_id_idx on tasks (node_id);

-- `podium node rm` needs to be possible. tasks.node_id referenced nodes(id), so a node that
-- had ever run anything could never be deleted — the constraint outlived the last task by
-- the whole of the history table. Referential integrity between a live inventory (nodes)
-- and an append-only record (tasks) is the wrong constraint: a finished task's node_id is
-- history, and history is allowed to name something that no longer exists. The column stays
-- and keeps its value; only the constraint goes.
alter table tasks drop constraint if exists tasks_node_id_fkey;

create or replace function podium_notify_task_queued() returns trigger as $$
begin
  if new.status = 'queued' then
    perform pg_notify('podium_tasks_queued', new.id);
  end if;
  return null;
end;
$$ language plpgsql;

drop trigger if exists tasks_queued_notify on tasks;
create trigger tasks_queued_notify
  after insert or update of status on tasks
  for each row execute function podium_notify_task_queued();
