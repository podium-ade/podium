-- What each turn actually ran on. Recorded per turn rather than derived from the playbook,
-- because the playbook's model is a default: the web chat's picker overrides agent, model
-- and effort for a single turn, and editing a playbook would otherwise relabel every turn
-- that ever ran under it. Only a column written at the time can answer "what did this cost
-- us on Opus" a month later.
--
-- Null for every turn that ran before this migration, and for a turn whose resolution
-- failed. The usage screen groups those as unrecorded rather than guessing.
alter table turns add column if not exists agent    text;
alter table turns add column if not exists model    text;
alter table turns add column if not exists effort   text;
alter table turns add column if not exists provider text;

-- The breakdown groups on these four across a date range.
create index if not exists turns_backend_idx on turns (started_at desc, provider, model);
