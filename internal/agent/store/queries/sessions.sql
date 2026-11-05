-- UpsertSession is keyed on source_key, which is the conversation's identity. The playbook is
-- deliberately NOT updated here: a THREAD keeps the playbook it started with, and a chat
-- window that may change its mind says so through SetSessionPlaybook instead.
-- name: UpsertSession :one
insert into sessions (id, source_kind, source_key, profile, playbook, created_at)
values (@id, @source_kind, @source_key, @profile, @playbook, @created_at)
on conflict (source_key) do update set source_kind = sessions.source_kind
returning *;

-- SetSessionPlaybook changes which playbook a session runs. Only a conversation does this —
-- a chat window, where the person picks per message — and never a Slack thread or a Linear
-- issue, whose one session is one piece of work.
-- name: SetSessionPlaybook :exec
update sessions set playbook = @playbook where id = @id;

-- name: GetSession :one
select * from sessions where id = @id;

-- name: GetSessionByKey :one
select * from sessions where source_key = @source_key;

-- ListSessions is the same partition as ListChats. include_all is the dev token and returns
-- every source. Otherwise a session is visible exactly when the chat that holds it is: a web
-- chat's own row, or the mirror of a Slack thread or pull request (joined on source_key, the
-- key both are written under). A Linear or dev session has no chat and stays the operator's.
--
-- Phase 5 adds the same sections ListChats has, and what a row needs to say about sharing:
-- the chat it is the record of, the caller's role and why they can see it, who shared it, and
-- how much of it they have not read. A session with no chat (Linear, dev) has none of these.
-- name: ListSessions :many
select s.*,
  coalesce(c.id, '')::text as chat_id,
  coalesce(chat_role(c.id, @viewer::text), '')::text as my_role,
  (case
    when c.id is null then ''
    when c.login is not null and c.login = @viewer::text then 'owner'
    when exists (select 1 from chat_participants p where p.chat_id = c.id and p.login = @viewer::text) then 'participant'
    when exists (select 1 from chat_access a where a.chat_id = c.id and a.login = @viewer::text) then 'shared'
    when c.general_access = 'members' then 'workspace'
    else ''
  end)::text as access_reason,
  coalesce((select a.granted_by from chat_access a where a.chat_id = c.id and a.login = @viewer::text), '')::text as shared_by,
  coalesce(chat_unread(c.id, @viewer::text), 0)::bigint as unread_count
from sessions s
left join chats c on c.source_key = s.source_key
where (
  (@include_all::bool and @section::text = '')
  or (c.id is not null and @section::text in ('', 'shared')
      and chat_visible_to(c.id, c.login, @viewer::text)
      and (@section::text = '' or c.login is distinct from @viewer::text))
  or (c.id is not null and @section::text = 'workspace'
      and c.general_access = 'members'
      and c.deleted_at is null
      and not chat_visible_to(c.id, c.login, @viewer::text)
      and exists (select 1 from identities i where i.provider = 'web' and i.external_id = @viewer::text))
)
and (@after_id::text = '' or s.id < @after_id::text)
order by s.id desc
limit @page_limit::int;

-- name: TouchSession :exec
update sessions set last_turn_at = @last_turn_at where id = @id;

-- SetGitHubReviewID records the pull-request review this session submitted. A later turn
-- updates that review. Zero rows means the session does not exist yet.
-- name: SetGitHubReviewID :execrows
update sessions set github_review_id = @review_id where source_key = @source_key;
