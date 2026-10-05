-- One workspace snapshot per conversation, and one base snapshot per repository.
--
-- The bytes live in the same S3 bucket as artifacts, under snapshots/. These rows are the
-- only pointer. A new upload writes the object first and flips the row second, then
-- deletes the previous object, so a failed upload leaves the last good snapshot in place.

create table if not exists workspace_snapshots (
  session_id text        primary key,
  object_key text        not null,
  size_bytes bigint      not null,
  sha256     text        not null,
  node_id    text        not null default '',
  task_id    text        not null default '',
  updated_at timestamptz not null default now()
);

create table if not exists workspace_bases (
  repo       text        primary key,
  object_key text        not null,
  size_bytes bigint      not null,
  sha256     text        not null,
  updated_at timestamptz not null default now()
);
