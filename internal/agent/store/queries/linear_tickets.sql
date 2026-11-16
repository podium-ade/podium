-- Tickets named in a chat, and the panel of an issue's conversation (docs/agent.md, "Tickets in
-- a chat"). Nothing here writes: what the webhooks said about an issue is linear_issues's, and
-- who touched it linear_people's (0048).

-- FindLinearIssue is an issue the webhooks described, by identifier ("ENG-12") or Linear id.
-- name: FindLinearIssue :one
select * from linear_issues
 where kind = 'issue'
   and ((@identifier::text <> '' and upper(identifier) = upper(@identifier::text))
        or (@linear_id::text <> '' and linear_id = @linear_id::text))
 order by updated_at desc
 limit 1;

-- LinearIssueTeamKeys is every team an issue the webhooks described is in.
-- name: LinearIssueTeamKeys :many
select distinct upper(team_key)::text as team_key from linear_issues where team_key <> '' order by 1;

-- ListSessionDelegations is the tasks one conversation started, newest first.
-- name: ListSessionDelegations :many
select * from delegations
 where session_id = @session_id
 order by created_at desc
 limit @max_rows::int;

-- ListChatRoutineRuns is the routine runs that happened in one chat other than the routine's
-- own: a Linear issue's or project's conversation (routine_runs.chat_id, 0047). Newest first,
-- with the routine's name.
-- name: ListChatRoutineRuns :many
select rr.*, r.name::text as routine_name
  from routine_runs rr
  join routines r on r.id = rr.routine_id
 where rr.chat_id = @chat_id::text
 order by rr.created_at desc
 limit @max_rows::int;
