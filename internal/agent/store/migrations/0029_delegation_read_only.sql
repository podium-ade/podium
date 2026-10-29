-- A delegation that only reads its conversation's workspace: the task restores the session's
-- snapshot and stores nothing back. Recorded on the row because the conductor decides who
-- may use a pull request's one workspace from the running rows, and a restart must not
-- mistake a reader for a writer.
alter table delegations add column if not exists read_only boolean not null default false;
