-- A routine may run on Linear events instead of a schedule (docs/agent.md, "On a Linear
-- event"): "when an issue is created, do this". The Linear agent app's webhook hands every data
-- change it receives to the routines that ask for it, and the leading conductor's routine tick
-- runs each such routine with the events that are waiting for it.

-- trigger_kind is what starts the routine's runs: its cron schedule, or Linear events. Every
-- routine before this one runs on a schedule. A linear routine keeps schedule empty and never
-- reads it; next_run_at is still written, because the column is not null, and is never due.
alter table routines add column if not exists trigger_kind text not null default 'schedule';
alter table routines drop constraint if exists routines_trigger_kind_check;
alter table routines add constraint routines_trigger_kind_check check (trigger_kind in ('schedule', 'linear'));
-- events are the Linear events a linear routine runs on, "<Type>.<action>" each ("Issue.create").
-- teams are Linear team keys ("ENG"); empty is every team.
alter table routines add column if not exists events text[] not null default '{}';
alter table routines add column if not exists teams text[] not null default '{}';

-- A run of a linear routine is started by the events it was given: trigger "event".
alter table routine_runs drop constraint if exists routine_runs_trigger_check;
alter table routine_runs add constraint routine_runs_trigger_check check (trigger in ('schedule', 'manual', 'event'));

-- routine_events is one Linear event waiting for, or given to, one routine's run. run_id is
-- null while it waits. unique (routine_id, delivery_id) stores a delivery Linear retried, or one
-- that reached two conductors, once.
--
-- payload is the delivery as Linear sent it, without its signature or anything else secret:
-- action, type, data, actor, url, updatedFrom and createdAt. One over 256 KB is kept as a stub
-- that says so.
create table if not exists routine_events (
  id          bigserial   primary key,
  routine_id  text        not null references routines (id) on delete cascade,
  delivery_id text        not null,
  event       text        not null,
  payload     jsonb       not null,
  received_at timestamptz not null,
  run_id      text        references routine_runs (id) on delete cascade,
  unique (routine_id, delivery_id)
);
create index if not exists routine_events_pending_idx on routine_events (routine_id, received_at) where run_id is null;
create index if not exists routine_events_run_idx on routine_events (run_id) where run_id is not null;
create index if not exists routine_events_received_idx on routine_events (received_at);
