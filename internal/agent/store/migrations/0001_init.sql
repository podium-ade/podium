-- The conductor's own schema, in its own database (podium_agent). It never shares a
-- connection with the control plane's, and nothing here references a Podium table: a
-- task_id is a string this database has no opinion about.
--
-- linear_cursor, chats and chat_messages are created empty on purpose. Steps 20 and 21 own
-- them; creating them now means neither step has to ship a migration for a table shape that
-- is already decided.

create table if not exists sessions (
  id           text        primary key,
  source_kind  text        not null,
  source_key   text        not null unique,
  profile      text        not null,
  skill        text        not null,
  created_at   timestamptz not null,
  last_turn_at timestamptz
);

create table if not exists turns (
  id          text        primary key,
  session_id  text        not null references sessions(id),
  task_id     text,
  trigger_ref text        not null,
  status      text        not null
                check (status in ('running', 'succeeded', 'failed', 'lost', 'cancelled', 'timeout')),
  started_at  timestamptz not null,
  finished_at timestamptz,
  num_turns   int,
  cost_usd    numeric(12, 6),
  final_text  text
);

create index if not exists turns_session_idx on turns (session_id, started_at desc);
-- The recovery pass on start reads exactly this: every turn that was in flight when the
-- process died.
create index if not exists turns_running_idx on turns (status) where status = 'running';

-- relayed is the exactly-once ledger for the relay. A task's events live in one seq space
-- (step 15), so (task_id, seq) is the identity of a thing that has been said out loud.
create table if not exists relayed (
  task_id text   not null,
  seq     bigint not null,
  primary key (task_id, seq)
);

create table if not exists settings (
  key        text        primary key,
  value      jsonb       not null,
  updated_at timestamptz not null
);

create table if not exists linear_cursor (
  key        text        primary key,
  updated_at timestamptz not null
);

create table if not exists chats (
  id         text        primary key,
  title      text        not null,
  login      text        not null,
  created_at timestamptz not null
);

create table if not exists chat_messages (
  chat_id     text        not null references chats(id),
  seq         bigint      not null,
  role        text        not null,
  text        text        not null,
  attachments jsonb       not null default '[]',
  ts          timestamptz not null,
  primary key (chat_id, seq)
);
