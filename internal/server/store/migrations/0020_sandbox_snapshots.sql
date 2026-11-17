-- A conversation's workspace kept by a sandbox provider as an image of its own (a Modal
-- directory snapshot) rather than as a tar in the object store. Podium holds only the ID.
--
-- sandbox_snapshots is one image per session, like workspace_snapshots. An image that is
-- replaced, or chosen for deletion, waits in sandbox_snapshots_retired until the provider
-- has deleted it, so a failed delete is tried again by the sweep and nothing is left behind.
-- sandbox_snapshot_retention is their own policy, a singleton: they have no size, so it
-- counts them instead. Zero turns a rule off, and both are off by default.

create table if not exists sandbox_snapshots (
  session_id text        primary key,
  provider   text        not null,
  image_id   text        not null,
  node_id    text        not null default '',
  task_id    text        not null default '',
  updated_at timestamptz not null default now()
);

create table if not exists sandbox_snapshots_retired (
  image_id   text        primary key,
  provider   text        not null,
  attempts   int         not null default 0,
  last_error text        not null default '',
  retired_at timestamptz not null default now()
);

create table if not exists sandbox_snapshot_retention (
  id              int         primary key default 1 check (id = 1),
  max_age_seconds bigint      not null default 0 check (max_age_seconds >= 0),
  max_count       bigint      not null default 0 check (max_count >= 0),
  updated_by      text        not null default '',
  updated_at      timestamptz not null default now()
);
