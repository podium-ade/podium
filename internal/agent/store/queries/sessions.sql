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
-- name: ListSessions :many
select * from sessions
where (
  @include_all::bool
  or exists (
    select 1 from chats c
    where c.source_key = sessions.source_key
      and chat_visible_to(c.id, c.login, @viewer::text)
  )
)
and (@after_id::text = '' or id < @after_id::text)
order by id desc
limit @page_limit::int;

-- name: TouchSession :exec
update sessions set last_turn_at = @last_turn_at where id = @id;

-- SetGitHubReviewID records the pull-request review this session submitted. A later turn
-- updates that review. Zero rows means the session does not exist yet.
-- name: SetGitHubReviewID :execrows
update sessions set github_review_id = @review_id where source_key = @source_key;
