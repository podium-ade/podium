-- last_seen_at is when this login last identified to the control plane (a Google
-- session or a tailnet WhoIs). Existing rows start as first_seen_at so they are
-- not "just now" the moment this migration runs.
alter table users add column if not exists last_seen_at timestamptz;
update users set last_seen_at = first_seen_at where last_seen_at is null;
alter table users alter column last_seen_at set default now();
alter table users alter column last_seen_at set not null;
