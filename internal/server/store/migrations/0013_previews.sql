-- Previews: a finished task whose spec exposes ports, kept up on its node until its ttl runs
-- out or somebody releases it.
--
-- A preview is not a task status. The task ends when its command does, with its exit code,
-- exactly as it would without expose; this row is what outlives it. expires_at stays null
-- while the command still runs, because the ttl counts from the exit.
create table if not exists previews (
  task_id        text        primary key references tasks(id) on delete cascade,
  node_id        text        not null default '',
  via            text        not null,
  address        text        not null,
  urls           jsonb       not null default '{}',
  ttl_ms         bigint      not null,
  expires_at     timestamptz,
  released_at    timestamptz,
  release_reason text        not null default '',
  created_at     timestamptz not null default now()
);

-- The expiry sweep asks for live previews whose time is up.
create index if not exists previews_expiry_idx on previews (expires_at)
  where released_at is null and expires_at is not null;
