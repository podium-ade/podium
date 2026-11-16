-- A Linear issue's or project's conversation (0048): what the webhooks said about it, who
-- touched it, and the Linear view's list.

-- UpsertLinearIssue records what a delivery said about an issue or project. A field it left
-- empty keeps what an earlier delivery said: an agent session's issue names no state, and a
-- comment's names no title.
-- name: UpsertLinearIssue :exec
insert into linear_issues (source_key, kind, linear_id, identifier, title, url, team_key, state_name, state_type, updated_at)
values (@source_key, @kind, @linear_id, @identifier, @title, @url, @team_key, @state_name, @state_type, @updated_at)
on conflict (source_key) do update set
  identifier = coalesce(nullif(excluded.identifier, ''), linear_issues.identifier),
  title      = coalesce(nullif(excluded.title, ''), linear_issues.title),
  url        = coalesce(nullif(excluded.url, ''), linear_issues.url),
  team_key   = coalesce(nullif(excluded.team_key, ''), linear_issues.team_key),
  state_name = coalesce(nullif(excluded.state_name, ''), linear_issues.state_name),
  state_type = coalesce(nullif(excluded.state_type, ''), linear_issues.state_type),
  updated_at = greatest(linear_issues.updated_at, excluded.updated_at);

-- GetLinearIssue is one conversation's issue or project, as the webhooks described it.
-- name: GetLinearIssue :one
select * from linear_issues where source_key = @source_key;

-- SetLinearIssueAsked marks a conversation as waiting on a person: a question was asked in it.
-- A conversation the webhooks never described gets a row of its own, so it can still wait.
-- name: SetLinearIssueAsked :exec
insert into linear_issues (source_key, kind, linear_id, asked_at, asked_of, updated_at)
values (@source_key, @kind, @linear_id, @asked_at, @asked_of, @asked_at)
on conflict (source_key) do update set asked_at = excluded.asked_at, asked_of = excluded.asked_of;

-- ClearLinearIssueAsked is the question answered, or the conversation moved on.
-- name: ClearLinearIssueAsked :exec
update linear_issues set asked_at = null, asked_of = '' where source_key = @source_key and asked_at is not null;

-- InsertLinearPerson records that somebody touched an issue or project. A repeat moves
-- last_seen only.
-- name: InsertLinearPerson :exec
insert into linear_people (source_key, linear_user, login, via, first_seen, last_seen)
values (@source_key, @linear_user, @login, @via, @seen_at, @seen_at)
on conflict (source_key, linear_user, login, via) do update
  set last_seen = greatest(linear_people.last_seen, excluded.last_seen);

-- LinearTouched is whether viewer touched the issue or project a conversation is about.
-- name: LinearTouched :one
select linear_touched(@source_key::text, @viewer::text)::bool as touched;

-- ListLinearConversations is the Linear view: one row per issue or project conversation, most
-- recently active first, with what the webhooks said about it and where Podium is with it.
--
-- see_all is an admin, an owner or the dev token, who read every one. Anybody else lists what
-- chat_visible_to allows: a conversation they spoke in, own, were shared, or whose issue they
-- touched (linear_touched). mine narrows either to the last of those.
--
-- state is what Podium is doing about it: 'waiting' on a person (a question nobody answered),
-- 'working' (a turn or a task it started is running), 'failed' (its last turn failed), 'done'
-- (its last turn ended), or 'new' (no turn yet). filter_state is '' for every one, a state, or
-- 'attention' for waiting and failed together.
--
-- The page is keyed by (last_activity_at, chat_id), newest first: after_at and after_id are the
-- last row of the page before, and after_at null is the first page.
-- name: ListLinearConversations :many
with conv as (
  select c.id as chat_id, c.source_key, c.title as chat_title, c.login as owner_login,
         coalesce(c.source_url, '')::text as source_url,
         coalesce(li.kind, case when c.source_key like 'linear:project:%' then 'project' else 'issue' end)::text as kind,
         coalesce(li.identifier, '')::text as identifier,
         coalesce(li.title, '')::text as issue_title,
         coalesce(li.url, '')::text as issue_url,
         coalesce(li.team_key, '')::text as team_key,
         coalesce(li.state_name, '')::text as state_name,
         coalesce(li.state_type, '')::text as state_type,
         li.asked_at as asked_at,
         coalesce(li.asked_of, '')::text as asked_of,
         coalesce(last.ts, c.created_at)::timestamptz as last_activity_at,
         coalesce(last.text, '')::text as last_text,
         coalesce(s.id, '')::text as session_id,
         exists (select 1 from turns t where t.session_id = s.id and t.status = 'running') as turn_running,
         exists (select 1 from delegations d where d.session_id = s.id and d.status = 'running') as task_running,
         coalesce((select t.status from turns t where t.session_id = s.id order by t.started_at desc limit 1), '')::text as last_turn_status,
         (coalesce((select sum(t.cost_usd) from turns t where t.session_id = s.id), 0)
          + coalesce((select sum(d.cost_usd) from delegations d where d.session_id = s.id), 0))::float8 as cost_usd,
         chat_role(c.id, @viewer::text)::text as my_role,
         chat_visible_to(c.id, c.login, @viewer::text)::bool as mine
    from chats c
    left join linear_issues li on li.source_key = c.source_key
    left join sessions s on s.source_key = c.source_key
    left join lateral (
      select m.ts, m.text from chat_messages m
       where m.chat_id = c.id and m.role not in ('activity', 'notice')
       order by m.seq desc limit 1
    ) last on true
   where c.origin = 'linear'
     and c.source_key like 'linear:%'
     and c.source_key not like 'linear:session:%'
     and c.deleted_at is null
     and (@see_all::bool or chat_visible_to(c.id, c.login, @viewer::text))
), listed as (
  select conv.*,
         (case
            when conv.asked_at is not null then 'waiting'
            when conv.turn_running or conv.task_running then 'working'
            when conv.last_turn_status in ('failed', 'lost', 'timeout') then 'failed'
            when conv.last_turn_status <> '' then 'done'
            else 'new'
          end)::text as state
    from conv
)
select * from listed
 where (not @only_mine::bool or listed.mine)
   and (@team::text = '' or listed.team_key = @team::text)
   and (@filter_state::text = ''
        or listed.state = @filter_state::text
        or (@filter_state::text = 'attention' and listed.state in ('waiting', 'failed')))
   and (sqlc.narg('after_at')::timestamptz is null
        or (listed.last_activity_at, listed.chat_id) < (sqlc.narg('after_at')::timestamptz, @after_id::text))
 order by listed.last_activity_at desc, listed.chat_id desc
 limit @page_limit::int;

-- LinearConversationTeams is every team a conversation the caller may list is in, for the
-- Linear view's team filter.
-- name: LinearConversationTeams :many
select distinct li.team_key::text as team_key
  from chats c
  join linear_issues li on li.source_key = c.source_key
 where c.origin = 'linear'
   and c.deleted_at is null
   and li.team_key <> ''
   and (@see_all::bool or chat_visible_to(c.id, c.login, @viewer::text))
 order by 1;
