-- Phase 3 of docs/agent.md: one transcript, a delivery outbox, and turns
-- that say who started them.

-- chat_outbox is every answer the bot owes a surface other than the web chat: the Slack thread
-- or pull request a turn started from, and the Slack threads bound to a pull request. The web
-- chat is the record and is written first; this table is how the copy on the surface is made
-- sure of. A row is written BEFORE the first attempt, with next_at a grace period away, so a
-- conductor that dies mid-post leaves a row another conductor finishes. That is at least once,
-- not exactly once: a crash between the surface accepting the post and the row being marked
-- sent posts it again. The alternative, at most once, loses answers, and a lost answer is the
-- failure this table exists to stop.
--
-- message_seq is the bot's own row in chat_messages, which is how the UI hangs "not delivered
-- to Slack" on the right message. payload is the Outbound the source is handed again on a
-- retry: type, text, task id and speaker, exactly as the first attempt had them.
--
-- status is 'pending' (due at next_at), 'sent', or 'failed' (gave up after max attempts; a
-- person can put it back to pending with RetryOutbox).
create table if not exists chat_outbox (
  id           text        primary key,
  chat_id      text        not null references chats(id) on delete cascade,
  message_seq  bigint      not null,
  surface_kind text        not null,
  surface_ref  text        not null,
  payload      jsonb       not null,
  status       text        not null default 'pending',
  attempts     int         not null default 0,
  next_at      timestamptz not null,
  last_error   text        not null default '',
  external_id  text,
  created_at   timestamptz not null,
  updated_at   timestamptz not null
);

create index if not exists chat_outbox_due_idx on chat_outbox (next_at) where status = 'pending';
create index if not exists chat_outbox_chat_idx on chat_outbox (chat_id, message_seq);

-- started_by is the Podium login whose turn this was, and whose credentials it ran with.
-- 'github-app' is a review nobody in Podium asked for: an automatic review, or a review
-- command from someone who has not linked GitHub. Empty is a turn recorded before this column,
-- or one with no person at all (a dev turn). Usage reads it to say who spent what.
alter table turns add column if not exists started_by text not null default '';
create index if not exists turns_started_by_idx on turns (started_by, started_at);
