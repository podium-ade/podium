-- Which conductor is doing a piece of work, when more than one conductor shares this
-- database. The key names the work: "session:<id>" is running a session's turns, and
-- "delegation:<id>" is following a delegated task. No row means nobody is, and the next
-- conductor to look takes it.
--
-- owner is a conductor's replica id, and expires_at is renewed while it works. A conductor
-- that dies stops renewing, and its work is anybody's once its leases run out — which is
-- how a surviving conductor adopts what a dead one was doing.
--
-- pending and warm_task_id are a session's alone. pending is a message another conductor
-- heard while this one owned the session: the owner takes it as its next turn instead of
-- letting go. warm_task_id is the owner's task that has answered and is waiting for a
-- follow-up, so another conductor can inject into it directly.
create table if not exists work_leases (
    key          text primary key,
    owner        text not null,
    expires_at   timestamptz not null,
    pending      jsonb,
    warm_task_id text not null default ''
);
