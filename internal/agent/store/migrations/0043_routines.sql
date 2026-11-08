-- Routines (docs/agent.md, "Routines"): a recurring job an agent does. On a schedule the
-- conductor starts a turn in the routine's one chat with the routine's instruction; the agent
-- decides what needs doing and does it.
--
-- A routine is personal in this phase: one person owns it, it runs as them, and its chat is
-- theirs alone and never shared. scope is here for the system routines that come next, which
-- an admin configures and which run as nobody.
create table if not exists routines (
  id                     text        primary key,
  scope                  text        not null default 'personal' check (scope in ('personal', 'system')),
  owner_login            text,
  name                   text        not null,
  -- schedule is a five-field cron expression, read in timezone (an IANA name).
  schedule               text        not null,
  timezone               text        not null,
  instruction            text        not null,
  instruction_updated_at timestamptz not null,
  -- notes are standing instructions the owner keeps on the routine card ("leave #42 alone").
  -- Every run is briefed with them next to the instruction. proposed_note is one the agent
  -- suggested through routine_finish, waiting for the owner to accept or dismiss it.
  notes                  text        not null default '',
  proposed_note          text        not null default '',
  -- personality_id is one of the owner's personal assistants, null for Podium.
  personality_id         text,
  model                  text        not null default '',
  effort                 text        not null default '',
  enabled                boolean     not null default true,
  paused_reason          text        not null default '',
  -- state is the agent's own JSON across runs: exact facts, such as which commit it already
  -- tried. routine_finish writes it.
  state                  jsonb       not null default '{}',
  state_updated_at       timestamptz,
  chat_id                text        not null unique references chats (id) on delete cascade,
  next_run_at            timestamptz not null,
  last_run_at            timestamptz,
  -- idle_count is how many runs since the last one that did something found nothing to do.
  idle_count             int         not null default 0,
  created_at             timestamptz not null,
  updated_at             timestamptz not null,
  check ((scope = 'system') = (owner_login is null))
);
create index if not exists routines_due_idx on routines (next_run_at) where enabled;
create index if not exists routines_owner_idx on routines (owner_login) where owner_login is not null;

-- One row per firing. unique (routine_id, scheduled_for, trigger) is what makes a tick fire a
-- routine once however many conductors share this database and whichever of them leads.
create table if not exists routine_runs (
  id            text        primary key,
  routine_id    text        not null references routines (id) on delete cascade,
  scheduled_for timestamptz not null,
  trigger       text        not null check (trigger in ('schedule', 'manual')),
  status        text        not null check (status in (
                  'running', 'done', 'idle', 'failed', 'skipped_overlap', 'skipped_paused')),
  summary       text        not null default '',
  -- reported is what the run's agent said of itself through routine_finish, while its turn
  -- was still running: idle (nothing to do) or done. The run is settled from it once the
  -- turn has ended, by whichever conductor ticks.
  reported      text        not null default '' check (reported in ('', 'idle', 'done')),
  turn_id       text,
  cost_usd      numeric(12, 6),
  created_at    timestamptz not null,
  finished_at   timestamptz,
  unique (routine_id, scheduled_for, trigger)
);
create index if not exists routine_runs_routine_idx on routine_runs (routine_id, created_at desc);
create index if not exists routine_runs_running_idx on routine_runs (status) where status = 'running';

-- The turn a run started. Set by the conductor as the turn is created, from the event.
alter table turns add column if not exists routine_run_id text references routine_runs (id) on delete set null;
create index if not exists turns_routine_run_idx on turns (routine_run_id) where routine_run_id is not null;

-- The routine a chat belongs to. A routine chat is an ordinary web chat in every other way —
-- its owner writes in it, deletes it, reads it — except that it is never shared.
alter table chats add column if not exists routine_id text references routines (id) on delete set null;
