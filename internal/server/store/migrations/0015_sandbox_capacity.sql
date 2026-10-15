-- Where a sandbox node ran a task: the provider and the sandbox the node made for it, set
-- when the node reports it. A new attempt overwrites them.
alter table tasks add column if not exists sandbox_provider text not null default '';
alter table tasks add column if not exists sandbox_id text not null default '';

-- max_concurrent caps how many sandboxes of a provider run at once, across every node of
-- it. Zero is no cap of its own.
alter table sandbox_providers add column if not exists max_concurrent integer not null default 0;

-- Sandboxes whose task the control plane gave up on with their node gone. Any node of the
-- provider deletes one and the row goes.
create table if not exists sandbox_reaps (
  provider   text        not null,
  sandbox_id text        not null,
  task_id    text        not null,
  reason     text        not null,
  created_at timestamptz not null default now(),
  primary key (provider, sandbox_id)
);
