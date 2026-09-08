-- The usage screen reads every turn in a date range across all sessions. turns_session_idx
-- is keyed on session_id first, so it cannot serve that scan; this one can.
create index if not exists turns_started_idx on turns (started_at desc);
