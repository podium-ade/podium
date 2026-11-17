-- Phase 4 of docs/agent.md: a turn per person, in order, and a chat that
-- knows it has more than one person in it.

-- turn_queue is every message that was put to Podium while the session it belongs to was
-- already answering something. It replaces work_leases.pending, which held ONE message and
-- overwrote it: right while a conversation had one person in it (only the latest thing they
-- said needed answering, the rest was in the transcript), and wrong the moment two people
-- tag Podium at once — the second person's turn would run as nobody, or as the first.
--
-- A row is written BEFORE anybody tries to serve it, and served by whichever conductor holds
-- the session's lease. That ordering is the whole correctness argument across conductors: a
-- conductor that fails to take the lease has already left its message where the owner's
-- loop will find it, and an owner always looks again after letting go (conductor/queue.go).
--
-- event is the conductor's InboundEvent, exactly as accept saw it after the speaker was
-- resolved: the credentials are worked out again when the turn STARTS, never from this row.
-- login, chat_id and message_seq are copies for the UI and for removing one's own row; the
-- event is what runs.
create table if not exists turn_queue (
  id          bigserial   primary key,
  session_id  text        not null references sessions(id) on delete cascade,
  source_kind text        not null,
  login       text        not null default '',
  author      text        not null default '',
  chat_id     text        not null default '',
  message_seq bigint      not null default 0,
  event       jsonb       not null,
  created_at  timestamptz not null default now()
);

create index if not exists turn_queue_session_idx on turn_queue (session_id, id);

-- Who a session's warm task is answering, and on which surface. A message from somebody else,
-- or from another surface, must not be injected into it: the task runs with its starter's
-- credentials and answers where its turn began.
alter table work_leases add column if not exists warm_login text not null default '';
alter table work_leases add column if not exists warm_kind  text not null default '';

-- multiplayer is sticky: once a chat has had more than one person in it (or is a mirrored
-- conversation, which always has), a web message has to tag Podium to start a turn, and it
-- stays that way even if people leave, so the rule never changes under anybody's feet.
-- Phase 5's sharing sets it too.
alter table chats add column if not exists multiplayer boolean not null default false;
update chats set multiplayer = true where origin <> 'web' and not multiplayer;

-- mentions is the Podium logins a message named with the composer's person chip, so phase 5
-- can tell them. Parsed from the text by the web source; empty everywhere else.
alter table chat_messages add column if not exists mentions text[] not null default '{}';
