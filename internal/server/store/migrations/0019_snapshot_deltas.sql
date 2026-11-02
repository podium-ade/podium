-- A session snapshot can be a difference from the repository base it started from
-- (internal/server/snapshots/delta.go).
--
-- base_key is the base object a snapshot is a difference from; empty is a whole tree.
-- workspace_session_origins is the base a session was started from, recorded when it is
-- served, so its first upload knows what to be a difference from. A base that is replaced
-- is not deleted while a snapshot or a session still refers to it: it waits in
-- workspace_bases_retired until nothing does.

alter table workspace_snapshots add column if not exists base_key text not null default '';

create table if not exists workspace_session_origins (
  session_id  text        primary key,
  base_key    text        not null,
  recorded_at timestamptz not null default now()
);

create table if not exists workspace_bases_retired (
  object_key text        primary key,
  retired_at timestamptz not null default now()
);
