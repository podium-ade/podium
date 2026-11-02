-- More than one podium-server on one database.
--
-- A node streams to exactly one server, and only that server can send it anything: an
-- assignment, a cancel, an inject. session_server says which server that is, so the others
-- know a node is held — connected, just not to them — and relay what they have for it.
-- Empty is a node no server holds.
alter table nodes add column if not exists session_server text not null default '';

-- A command for a node held by another server: cancel, inject, end a wait, release a preview,
-- drain, set slots. The sender writes the row and wakes every server with pg_notify; the one
-- holding the node deletes it as it sends it, and that delete is the acknowledgement the
-- sender waits for. A row nobody takes is deleted by its sender, which then says the node has
-- no session, exactly as a single server does.
create table if not exists node_commands (
    id         bigserial   primary key,
    node_id    text        not null,
    payload    jsonb       not null,
    created_at timestamptz not null default now()
);
