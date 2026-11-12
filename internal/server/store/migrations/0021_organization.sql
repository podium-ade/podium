-- What this deployment is called, and the organization it belongs to. A singleton, like
-- instance and snapshot_retention.
--
-- Empty means "not set": the UI and the agent fall back to "Podium" for the deployment
-- name and say nothing about an organization. Nothing here is a credential, and the
-- deployment name is shown before sign-in (it is the page's title).

create table if not exists organization (
  id                int         primary key default 1 check (id = 1),
  organization_name text        not null default '' check (char_length(organization_name) <= 80),
  deployment_name   text        not null default '' check (char_length(deployment_name) <= 40),
  updated_by        text        not null default '',
  updated_at        timestamptz not null default now()
);
