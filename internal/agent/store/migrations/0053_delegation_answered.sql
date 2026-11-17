-- answered_at is when a running delegated task last gave its answer and began waiting for a
-- follow-up; null while it is working. Recorded on the row because a conductor that restarts
-- resumes following a task after its answer was relayed, and must still know the task is
-- only waiting: ending a waiting task is a release (end_wait, exit 0), not a cancel.
alter table delegations add column if not exists answered_at timestamptz;
