-- The chat is the whole record of a conversation (docs/agent.md, phase
-- 2). Every message from every surface lands here — including what nobody addressed to
-- Podium and what was said by people who are not in Podium — labelled with who said it and
-- where, so the UI can show a Slack thread or a pull request as the people in it saw it.

-- author_login is the Podium person who said it, null for somebody who is not in Podium and
-- for the bot. author is still the display name (0014); handle and avatar are what the UI
-- draws beside it.
alter table chat_messages add column if not exists author_login  text;
alter table chat_messages add column if not exists author_handle text not null default '';
alter table chat_messages add column if not exists author_avatar text not null default '';

-- via is the surface a message came in through: 'web', 'slack', 'github', or 'bot' for what
-- the conductor itself wrote. Unconstrained for the same reason chats.origin is (0014): a
-- closed set in code, and a CHECK would need a migration per surface.
alter table chat_messages add column if not exists via text not null default 'web';
update chat_messages m set via = c.origin
  from chats c
 where c.id = m.chat_id and c.origin <> 'web' and m.role = 'user' and m.via = 'web';
update chat_messages set via = 'bot' where role <> 'user' and via = 'web';

-- external_id is the message's identity on its own surface: a Slack ts, or a GitHub comment
-- or review id with its kind in front (the three kinds number independently). It is how a
-- retried or redelivered event is recognised, and how an edit or a delete finds its row.
-- external_url is the link back to it.
alter table chat_messages add column if not exists external_id  text;
alter table chat_messages add column if not exists external_url text not null default '';

-- addressed is whether the message was put to Podium: a mention, a DM, a review command, or
-- any message of a single-player web chat. Everything before this migration was, because
-- nothing else was recorded.
alter table chat_messages add column if not exists addressed boolean not null default true;

-- An edit or a delete on the surface keeps the row. A deleted message keeps its text too
-- (decision 12): the UI shows a placeholder and the transcript leaves it out.
alter table chat_messages add column if not exists edited_at  timestamptz;
alter table chat_messages add column if not exists deleted_at timestamptz;

-- The dedupe. One message is one row however many times its event arrives — a Slack retry,
-- a GitHub redelivery, two conductors hearing the same webhook — and the insert that finds
-- the row already there is the one that does not start a turn.
create unique index if not exists chat_messages_external_idx
  on chat_messages (chat_id, via, external_id) where external_id is not null;

-- last_seq is the chat's message counter. Taking the next seq by bumping it locks the chat
-- row, which puts concurrent writers to one chat in order across every conductor; the old
-- max(seq)+1 let two of them compute the same seq and one of them fail on the primary key.
alter table chats add column if not exists last_seq bigint not null default 0;
update chats c
   set last_seq = coalesce((select max(m.seq) from chat_messages m where m.chat_id = c.id), 0);
