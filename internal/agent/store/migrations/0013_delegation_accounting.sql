-- What a delegated task cost, and what ran it.
--
-- These columns existed on `turns` only, which was right while every turn was a task: the
-- turn row WAS the unit of spend. A conversation is now answered by the assistant, which
-- delegates the work, so the expensive half of this bot runs as a delegation and belonged to
-- no row that the usage screen read. The conductor received each task's accounting message
-- and dropped it — at Debug level, which is off — so the money was not merely unattributed,
-- it was gone.
--
-- Same types and same nullability as turns': null for a delegation that ran before this
-- migration, and for one whose accounting never arrived. The usage screen reports those as
-- unrecorded rather than as zero.
alter table delegations add column if not exists num_turns integer;
alter table delegations add column if not exists cost_usd  numeric(12,6);
alter table delegations add column if not exists agent     text;
alter table delegations add column if not exists model     text;
alter table delegations add column if not exists effort    text;
alter table delegations add column if not exists provider  text;

-- The breakdown groups on these across a date range, exactly as turns_backend_idx does.
create index if not exists delegations_backend_idx on delegations (created_at desc, provider, model);
