-- How long a conversation's workspace snapshot is kept. A singleton, like instance.
--
-- Zero in either column means that rule is off, and both are off by default: a deployment
-- that upgrades keeps every snapshot until an operator says otherwise. The sweep applies
-- both rules to session snapshots only; repository bases are never swept.

create table if not exists snapshot_retention (
  id              int         primary key default 1 check (id = 1),
  max_age_seconds bigint      not null default 0 check (max_age_seconds >= 0),
  max_total_bytes bigint      not null default 0 check (max_total_bytes >= 0),
  updated_by      text        not null default '',
  updated_at      timestamptz not null default now()
);
