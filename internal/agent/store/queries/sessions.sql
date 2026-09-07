-- UpsertSession is keyed on source_key, which is the conversation's identity. The playbook is
-- deliberately NOT updated: one session, one playbook, fixed at creation.
-- name: UpsertSession :one
insert into sessions (id, source_kind, source_key, profile, playbook, created_at)
values (@id, @source_kind, @source_key, @profile, @playbook, @created_at)
on conflict (source_key) do update set source_kind = sessions.source_kind
returning *;

-- name: GetSession :one
select * from sessions where id = @id;

-- name: GetSessionByKey :one
select * from sessions where source_key = @source_key;

-- name: ListSessions :many
select * from sessions
where (@after_id::text = '' or id < @after_id::text)
order by id desc
limit @page_limit::int;

-- name: TouchSession :exec
update sessions set last_turn_at = @last_turn_at where id = @id;
