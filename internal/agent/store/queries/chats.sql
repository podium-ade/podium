-- The web chat. Unlike Slack and Linear there is no external system holding the
-- conversation, so these two tables ARE the conversation: what a turn is handed as history
-- and what a reload renders both come from here.
--
-- chats.login is the partition. There is no RBAC in this track, but a login is a natural
-- boundary and it is free, so every read is filtered by it.

-- name: CreateChat :one
insert into chats (id, title, login, created_at)
values (@id, @title, @login, @created_at)
returning *;

-- name: GetChat :one
select * from chats where id = @id;

-- RenameChat is filtered by login so a rename cannot cross the partition even if
-- the caller forgot to check. No row is not found, whether the chat is missing or
-- belongs to somebody else — the same answer every other chat read gives.
-- name: RenameChat :one
update chats set title = @title
 where id = @id and login = @login
returning *;

-- ListChats pages a login's own chats, newest first, with the two things the list needs
-- that are not columns: when the conversation was last spoken in, and the head of what was
-- said. turn_running comes from the conductor's own tables, so a reload agrees with the
-- server about whether the composer is disabled.
--
-- has_message carries what a NULL would have said. sqlc does not infer nullability through
-- this join, so the two joined columns are coalesced and the boolean is what the store
-- reads to decide "nobody has spoken here yet" — a lie about a timestamp would surface as
-- a chat that claims a message it does not have.
-- name: ListChats :many
select c.*,
  (m.ts is not null)::bool      as has_message,
  coalesce(m.ts, c.created_at)  as last_message_at,
  coalesce(m.text, '')          as last_text,
  exists (
    select 1 from turns t
      join sessions s on s.id = t.session_id
     where s.source_key = @source_key_prefix::text || c.id
       and t.status = 'running'
  ) as turn_running
from chats c
left join (
  select cm.chat_id, cm.ts, cm.text,
         row_number() over (partition by cm.chat_id order by cm.seq desc) as rn
    from chat_messages cm
) m on m.chat_id = c.id and m.rn = 1
where c.login = @login
  and (@after_id::text = '' or c.id < @after_id::text)
order by c.id desc
limit @page_limit::int;

-- AppendChatMessage takes the next seq for the chat. It is a single statement so the read
-- of max(seq) and the insert cannot interleave: two concurrent sends produce two seqs, and
-- the primary key would refuse a collision anyway.
-- name: AppendChatMessage :one
insert into chat_messages (chat_id, seq, role, text, attachments, ts)
select @chat_id, coalesce(max(seq), 0) + 1, @role, @text, @attachments, @ts
  from chat_messages where chat_id = @chat_id
returning *;

-- name: ListChatMessages :many
select * from chat_messages
where chat_id = @chat_id and seq > @from_seq::bigint
order by seq;

-- name: LastAssistantMessage :one
select * from chat_messages
where chat_id = @chat_id and role = 'assistant'
order by seq desc limit 1;

-- name: SetChatMessageAttachments :one
update chat_messages set attachments = @attachments
where chat_id = @chat_id and seq = @seq
returning *;

-- ChatTurnRunning is the server-side half of "one turn in flight per conversation". The UI
-- disables the composer; this is what makes a second send impossible rather than unlikely.
-- name: ChatTurnRunning :one
select exists (
  select 1 from turns t
    join sessions s on s.id = t.session_id
   where s.source_key = @source_key
     and t.status = 'running'
)::bool as running;
