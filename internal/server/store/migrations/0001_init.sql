-- MVP-0 schema. Tables trimmed to the demo loop: no artifacts, secrets, users or audit_log
-- (steps 09/10 and the RBAC slice bring those back). Every timestamp is timestamptz.

create table if not exists nodes (
  id                text        primary key,
  name              text        not null,
  tags              text[]      not null default '{}',
  labels            jsonb       not null default '[]',
  capacity          jsonb       not null default '{}',
  node_key_hash     bytea       not null,
  status            text        not null default 'offline',
  version           text,
  last_heartbeat_at timestamptz,
  created_at        timestamptz not null default now()
);

create unique index if not exists nodes_key_hash_idx on nodes (node_key_hash);

create table if not exists enrollment_tokens (
  id              text        primary key,
  token_hash      bytea       not null unique,
  labels          jsonb       not null default '[]',
  expires_at      timestamptz not null,
  used_at         timestamptz,
  used_by_node_id text,
  created_by      text        not null,
  created_at      timestamptz not null default now()
);

create table if not exists tasks (
  id               text        primary key,
  spec             jsonb       not null,
  status           text        not null default 'queued',
  priority         int         not null default 0,
  requested_by     text        not null,
  node_id          text        references nodes(id),
  lease_id         text,
  lease_expires_at timestamptz,
  attempts         int         not null default 0,
  max_attempts     int         not null default 1,
  created_at       timestamptz not null default now(),
  scheduled_at     timestamptz,
  started_at       timestamptz,
  finished_at      timestamptz,
  exit_code        int,
  usage            jsonb,
  failure_reason   text
);

create index if not exists tasks_queue_idx on tasks (status, priority desc, created_at) where status = 'queued';
create index if not exists tasks_node_idx on tasks (node_id) where status in ('scheduled', 'provisioning', 'running');

create table if not exists task_events (
  task_id text        not null references tasks(id) on delete cascade,
  seq     bigint      not null,
  kind    text        not null,
  ts      timestamptz not null,
  payload jsonb       not null,
  primary key (task_id, seq)
);

create table if not exists task_log_chunks (
  task_id text        not null references tasks(id) on delete cascade,
  seq     bigint      not null,
  stream  text        not null,
  sidecar text,
  ts      timestamptz not null,
  bytes   bytea       not null,
  primary key (task_id, seq)
);

create index if not exists task_log_chunks_ts_idx on task_log_chunks (ts);
