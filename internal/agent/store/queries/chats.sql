-- The web chat. Unlike Slack and Linear there is no external system holding the
-- conversation, so these two tables ARE the conversation: what a turn is handed as history
-- and what a reload renders both come from here.
--
-- chats.login is the partition. There is no RBAC in this track, but a login is a natural
-- boundary and it is free, so every read is filtered by it.

-- CreateChat takes login as a nullable text: a web chat is owned by the login that made
-- it, and a mirrored Slack thread is owned by nobody. Only the dev token lists those.
-- personality_id is null for Podium and for a mirrored thread. A personal chat stores
-- the id and a snapshot of the display name, so the thread stays labeled after the
-- personality is deleted. An empty id is written as null, not as an empty string.
-- name: CreateChat :one
insert into chats (
  id, title, login, created_at, auto_title, source_key, started_by, origin,
  personality_id, personality_display, multiplayer
)
values (
  @id, @title, sqlc.narg('login'), @created_at, @auto_title, @source_key, @started_by, @origin,
  sqlc.narg('personality_id'), @personality_display,
  -- A mirrored conversation always has other people in it (decision 33).
  @origin::text <> 'web'
)
returning *;

-- name: GetChat :one
select * from chats where id = @id;

-- RenameChat is filtered by login so a rename cannot cross the partition even if
-- the caller forgot to check. No row is not found, whether the chat is missing or
-- belongs to somebody else — the same answer every other chat read gives.
--
-- It clears auto_title: a name a human typed is theirs, the same rule as a title
-- supplied at create, so no later turn renames the chat over the top of it.
--
-- include_shared lets the dev token rename a mirrored thread (login is null). A signed-in
-- user never sets it, so a thread they cannot see cannot be renamed by guessing the id.
--
-- Phase 5: an editor may rename too — the owner and everybody chat_role makes an editor
-- (a participant, or somebody it was shared with as one). A viewer may not. A deleted
-- mirrored chat (0036) is nobody's to rename, its owner included.
-- name: RenameChat :one
update chats set title = @title, auto_title = false
 where id = @id and (
   (deleted_at is null and login = @login)
   or chat_role(id, @login::text) in ('owner', 'editor')
   or (@include_shared::bool and login is null)
 )
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
     where s.source_key = c.source_key
       and t.status = 'running'
  ) as turn_running,
  -- A delegated task outlives the turn that started it, and it is the conversation's work
  -- for as long as it runs.
  exists (
    select 1 from delegations d
      join turns t on t.id = d.turn_id
      join sessions s on s.id = t.session_id
     where s.source_key = c.source_key
       and d.status = 'running'
  ) as task_running,
  -- Phase 5: what the caller may do here and why they can see it, for the list's sections
  -- and its owner/other menus. The dev token is answered by the API (it is the operator, an
  -- owner of everything), so these are computed for the viewer even then and overridden.
  chat_role(c.id, @viewer::text)::text as my_role,
  (case
    when c.login is not null and c.login = @viewer::text then 'owner'
    when exists (select 1 from chat_participants p where p.chat_id = c.id and p.login = @viewer::text) then 'participant'
    when exists (select 1 from chat_access a where a.chat_id = c.id and a.login = @viewer::text) then 'shared'
    when c.general_access = 'members' then 'workspace'
    else ''
  end)::text as access_reason,
  coalesce((select a.granted_by from chat_access a where a.chat_id = c.id and a.login = @viewer::text), '')::text as shared_by,
  chat_unread(c.id, @viewer::text)::bigint as unread_count
from chats c
left join (
  select cm.chat_id, cm.ts, cm.text,
         row_number() over (partition by cm.chat_id order by cm.seq desc) as rn
    from chat_messages cm
   where cm.role not in ('activity', 'notice')
) m on m.chat_id = c.id and m.rn = 1
-- include_all is the shared dev token, which has no per-user identity and is the operator
-- view: every web chat and every mirrored thread. A signed-in user (Google Workspace, or
-- any other named login) is the other branch and sees what chat_visible_to allows: what
-- they own, and every conversation they have taken part in from any surface. A mirrored
-- thread nobody in Podium has spoken in matches neither, and stays hidden.
--
-- section narrows a signed-in caller's list (phase 5). '' is their own chats: owned, spoken
-- in, or shared with them — chat_visible_to. 'shared' is the part of that somebody else owns.
-- 'workspace' is the chats shared with every member that are NOT already in their own list:
-- a members-shared chat appears there until they open it, and in their own list after.
where (
    (@include_all::bool and @section::text = '')
    or (@section::text in ('', 'shared') and chat_visible_to(c.id, c.login, @viewer::text)
        and (@section::text = '' or c.login is distinct from @viewer::text))
    or (@section::text = 'workspace'
        and c.general_access = 'members'
        and c.deleted_at is null
        and not chat_visible_to(c.id, c.login, @viewer::text)
        and exists (select 1 from identities i where i.provider = 'web' and i.external_id = @viewer::text))
  )
  and (@after_id::text = '' or c.id < @after_id::text)
  -- filter_personality is opt-in. Left false, the list is every chat the caller could
  -- already see, which is what every existing caller asks for. Set, an empty
  -- personality_id is Podium (the column is null) and a set id is that voice only.
  --
  -- A chat with somebody ELSE's personal assistant is listed under Podium (phase 4). The
  -- assistant stays private: the viewer has no picker entry for it and could never select
  -- its id, so without this branch a conversation they take part in would be in no list at
  -- all. The UI shows it as shared, labelled with personality_display.
  and (
    not @filter_personality::bool
    or (
      (@personality_id::text = '' and (
        c.personality_id is null
        or (not @include_all::bool and c.login is distinct from @viewer::text)
      ))
      or (@personality_id::text <> '' and c.personality_id = @personality_id
          and (@include_all::bool or c.login = @viewer::text))
    )
  )
order by c.id desc
limit @page_limit::int;

-- AppendChatMessage takes the next seq for the chat by bumping chats.last_seq (0032). The
-- update locks the chat row until the insert commits, so concurrent writers to one chat —
-- on this conductor or another — are put in order instead of computing the same max(seq)+1
-- and one of them failing on the primary key. No row back means the chat is not there.
-- name: AppendChatMessage :one
with next as (
  update chats set last_seq = last_seq + 1 where id = @chat_id returning last_seq
)
insert into chat_messages (
  chat_id, seq, role, text, attachments, ts, task_id, author,
  author_login, author_handle, author_avatar, via, external_id, external_url, addressed,
  mentions
)
select @chat_id, next.last_seq, @role, @text, @attachments, @ts, @task_id, @author,
       sqlc.narg('author_login'), @author_handle, @author_avatar, @via,
       sqlc.narg('external_id'), @external_url, @addressed, @mentions::text[]
  from next
returning *;

-- RecordChatMessage is AppendChatMessage for a message that has an identity on its own
-- surface. A second arrival of the same message (a retry, a redelivery, another conductor
-- hearing the same event) inserts nothing and returns no row, and that is the whole of the
-- dedupe: the caller starts a turn only for a row this statement actually wrote. The seq the
-- losing insert took is left as a gap, which nothing reads as meaningful.
-- name: RecordChatMessage :one
with next as (
  update chats set last_seq = last_seq + 1 where id = @chat_id returning last_seq
)
insert into chat_messages (
  chat_id, seq, role, text, attachments, ts, task_id, author,
  author_login, author_handle, author_avatar, via, external_id, external_url, addressed,
  mentions
)
select @chat_id, next.last_seq, @role, @text, @attachments, @ts, @task_id, @author,
       sqlc.narg('author_login'), @author_handle, @author_avatar, @via,
       @external_id::text, @external_url, @addressed, @mentions::text[]
  from next
on conflict (chat_id, via, external_id) where external_id is not null do nothing
returning *;

-- EditChatMessageByExternal applies an edit made on the message's own surface. The text is
-- replaced and edited_at stamped; nothing else about the row moves, so its place in the
-- conversation is where it was said, not where it was edited.
-- name: EditChatMessageByExternal :one
update chat_messages set text = @text, edited_at = @edited_at
where chat_id = @chat_id and via = @via and external_id = @external_id::text
returning *;

-- DeleteChatMessageByExternal marks a message deleted on its own surface. The text stays
-- (decision 12); the API sends none for it and the transcript leaves it out.
-- name: DeleteChatMessageByExternal :one
update chat_messages set deleted_at = coalesce(deleted_at, @deleted_at)
where chat_id = @chat_id and via = @via and external_id = @external_id::text
returning *;

-- GetChatMessage reads one message, which is how a conductor rebuilds a frame another
-- conductor announced.
-- name: GetChatMessage :one
select * from chat_messages where chat_id = @chat_id and seq = @seq;

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
   where s.source_key = (select c.source_key from chats c where c.id = @chat_id)
     and t.status = 'running'
)::bool as running;

-- SetChatTitle rewrites an auto-named chat. A title the caller supplied at create
-- (auto_title = false) is left alone.
-- SetChatChoice records what this chat is answered on: the override a person picked, empty
-- for the assistant's own. Last-write-wins on purpose — switching back to the default is a
-- choice too, and it is expressed by sending nothing.
-- name: SetChatChoice :one
update chats set agent = @agent, model = @model, effort = @effort
where id = @id
returning *;

-- name: SetChatTitle :one
update chats set title = @title
where id = @id and auto_title
returning *;

-- DeleteChat takes the messages with the chat via ON DELETE CASCADE. The login is in the
-- query so another owner's chat cannot be removed even if the id is known; zero rows
-- means it was not there or not theirs. include_shared is the dev token removing a
-- mirrored copy. It does not let that token delete a web chat it does not own.
-- name: DeleteChat :execrows
delete from chats where id = @id and origin = 'web' and (
  login = @login
  or (@include_shared::bool and login is null)
);

-- TombstoneChat is DeleteChat for a MIRRORED chat (0036): the row stays, so the conversation's
-- next message finds it rather than opening a new record, and deleted_at takes it away from
-- everyone. Deleting a tombstone again is a no-op that still reports the row, so a second
-- click is not a "not found".
-- name: TombstoneChat :execrows
update chats set deleted_at = coalesce(deleted_at, @deleted_at)
where id = @id and origin <> 'web' and (
  login = @login
  or (@include_shared::bool and login is null)
);

-- LinkChatPullRequest is the automatic half of a chat's pull requests (0007): a turn's
-- answer named this one. It is on-conflict-do-nothing, which is both halves of "one link
-- per URL per chat" — the same turn saying it three times, and a row a human already
-- detached. Every read below filters detached_at, so a detached link is gone as far as the
-- UI is concerned and still there as far as this insert is concerned, which is the whole
-- point of the tombstone.
-- name: LinkChatPullRequest :execrows
insert into chat_pull_requests (chat_id, url, owner, repo, number, source, created_at)
values (@chat_id, @url, @owner, @repo, @number, 'turn', @created_at)
on conflict (chat_id, url) do nothing;

-- AttachChatPullRequest is the manual half. Unlike the automatic one it revives a detached
-- row and takes it over: a person re-attaching what they removed means it, and after that
-- the link is theirs rather than a turn's.
-- name: AttachChatPullRequest :one
insert into chat_pull_requests (chat_id, url, owner, repo, number, source, created_at)
values (@chat_id, @url, @owner, @repo, @number, 'human', @created_at)
on conflict (chat_id, url) do update
  set source = 'human', detached_at = null
returning *;

-- DetachChatPullRequest tombstones one link. Zero rows means it was not linked, or was
-- already detached; both are the same answer.
-- name: DetachChatPullRequest :execrows
update chat_pull_requests set detached_at = @detached_at
where chat_id = @chat_id and url = @url and detached_at is null;

-- ListChatPullRequests is oldest first: the order they were linked in is the order the
-- work happened in, and a bar that appends on the right does not reshuffle itself when a
-- turn finds another one.
-- name: ListChatPullRequests :many
select * from chat_pull_requests
where chat_id = @chat_id and detached_at is null
order by created_at, number;

-- GetChatBySourceKey is how the mirror finds the chat for a conversation it has already
-- seen. One statement rather than a lookup-then-insert, because two messages arriving
-- together in one Slack thread must not make two chats: the caller upserts on this key.
-- name: GetChatBySourceKey :one
select * from chats where source_key = @source_key;

-- ChatAuthors is who has spoken in a conversation by display name, first appearance first:
-- the distinct authors of the human messages. For a mirrored thread that is everyone in it,
-- in Podium or not; for a web chat it is nobody, because a web chat's messages carry no
-- author. store.ChatParticipants merges it with chat_participants, which knows who is who.
--
-- via and avatar are the surface and picture of the author's first message, which is what
-- the header draws beside a name that has no Podium identity behind it.
-- name: ChatAuthors :many
select author,
       (array_agg(via order by seq))[1]::text           as via,
       (array_agg(author_avatar order by seq))[1]::text as avatar
  from chat_messages
 where chat_id = @chat_id and role = 'user' and author <> ''
 group by author
 order by min(seq);

-- NameWebAuthors names who typed each earlier web message of a chat that has just become
-- multiplayer. A single-player chat leaves author empty (the reader is the asker), so without
-- this the owner's messages from before the share would be the only unnamed ones in the thread.
-- name: NameWebAuthors :exec
update chat_messages set author = author_login
where chat_id = @chat_id and role = 'user' and via = 'web' and author = ''
  and coalesce(author_login, '') <> '';

-- MarkChatMultiplayer makes a chat multiplayer for good (0034) and returns it only when this
-- call is what changed it, so the caller announces the change once. force is a share or a
-- mirrored conversation; otherwise it takes a second Podium person among the participants.
-- name: MarkChatMultiplayer :one
update chats set multiplayer = true
where id = @id and not multiplayer and (
  @force::bool
  or (select count(*) from chat_participants p where p.chat_id = chats.id) >= 2
)
returning *;

-- SetChatStartedFrom records that a chat was opened by a turn of another conversation.
-- name: SetChatStartedFrom :one
update chats
set started_from_chat = sqlc.narg('started_from_chat'), started_by_turn = @started_by_turn
where id = @id
returning *;

-- CountChatsStartedByTurn is how many chats one turn has opened.
-- name: CountChatsStartedByTurn :one
select count(*)::int from chats where started_by_turn = @started_by_turn::text;
