-- Artifacts and log roll-up (step 10).
--
-- The artifacts table was trimmed out of 0001_init.sql for MVP-0, so this creates it from
-- scratch — with the kind column the step file asks for already in place, rather than as a
-- second migration nobody would ever run separately.
--
-- kind is 'file' for anything a task produced and 'log' for a rolled-up log stream. It is
-- text with a default rather than an enum because an unknown kind must never stop a row
-- from being read back.
create table if not exists artifacts (
  id           text        primary key,
  task_id      text        not null references tasks(id) on delete cascade,
  kind         text        not null default 'file',
  name         text        not null,
  object_key   text        not null,
  size_bytes   bigint      not null default 0,
  content_type text        not null default '',
  sha256       text        not null default '',
  created_at   timestamptz not null default now()
);

create index if not exists artifacts_task_idx on artifacts (task_id, created_at, id);

-- The log roll-up watermarks.
--
-- Rolling a task's logs up and then pruning task_log_chunks deletes the rows that
-- store.TaskStreamOffsets and store.MaxTaskSeq are computed from, and those two are what
-- reconciliation hands a restarting node so it can resume a container's output without a
-- gap or a duplicate (step 12). Losing them would let both answers go *backwards*, which
-- is the one thing that must never happen: a node told to resume from 0 re-sends the whole
-- container log under fresh sequence numbers.
--
-- Roll-up only ever touches terminal tasks, which a node is never told to adopt, so the
-- hazard should not arise. These columns make sure it cannot: both queries take the
-- greatest of what is still in task_log_chunks and what was true when the chunks were
-- rolled up, so pruning cannot move either number down.
alter table tasks add column if not exists logs_rolled_up_at  timestamptz;
alter table tasks add column if not exists logs_high_seq      bigint not null default 0;
alter table tasks add column if not exists logs_stdout_offset bigint not null default 0;
alter table tasks add column if not exists logs_stderr_offset bigint not null default 0;

-- The roll-up sweep asks for terminal tasks that have not been rolled up yet, and the
-- prune asks for tasks rolled up before a horizon.
create index if not exists tasks_rollup_pending_idx on tasks (finished_at)
  where logs_rolled_up_at is null and status in ('succeeded', 'failed', 'cancelled', 'lost');
create index if not exists tasks_rolled_up_idx on tasks (logs_rolled_up_at)
  where logs_rolled_up_at is not null;
