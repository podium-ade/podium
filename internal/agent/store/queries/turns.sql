-- name: CreateTurn :one
insert into turns (id, session_id, task_id, trigger_ref, status, started_at,
                   agent, model, effort, provider, started_by)
values (@id, @session_id, @task_id, @trigger_ref, @status, @started_at,
        nullif(@agent::text, ''), nullif(@model::text, ''), nullif(@effort::text, ''), nullif(@provider::text, ''),
        @started_by)
returning *;

-- name: SetTurnTask :exec
update turns set task_id = @task_id where id = @id;

-- name: FinishTurn :exec
update turns
set status      = @status,
    finished_at = @finished_at,
    num_turns   = @num_turns,
    cost_usd    = @cost_usd,
    final_text  = @final_text
where id = @id;

-- name: ListTurns :many
select * from turns
where session_id = @session_id
order by started_at desc, id desc
limit @page_limit::int;

-- name: ListRunningTurns :many
select * from turns where status = 'running' order by started_at;

-- name: GetTurn :one
select * from turns where id = @id;

-- ListChatTurnRunners is every turn of the conversation a chat records, oldest first, with who
-- it ran as and the tasks it ran: its own (a playbook's turn) and every one it delegated. It is
-- what tells a reader of a Linear issue's conversation which of its rows are somebody else's
-- work (0049, api/runner.go): a row with a task id belongs to the turn that ran that task, and
-- the assistant's own rows to the turn that was running when they were said — a conversation
-- runs one turn at a time. routine_name and routine_system name a routine's run.
-- name: ListChatTurnRunners :many
select t.id, coalesce(t.task_id, '')::text as task_id, t.started_by, t.started_at,
       coalesce(r.name, '')::text as routine_name,
       coalesce(r.scope = 'system', false)::bool as routine_system,
       coalesce(r.owner_login, '')::text as routine_owner,
       coalesce((select array_agg(d.task_id order by d.created_at) from delegations d
                  where d.turn_id = t.id and d.task_id is not null), '{}')::text[] as delegated
  from turns t
  join sessions s on s.id = t.session_id
  left join routine_runs rr on rr.id = t.routine_run_id
  left join routines r on r.id = rr.routine_id
 where s.source_key = (select c.source_key from chats c where c.id = @chat_id)
 order by t.started_at, t.id;

-- SetTurnCredential records what paid for a turn (0056), once the conductor has decided: that
-- happens after the row exists, because the turn's person and conversation decide it.
-- name: SetTurnCredential :exec
update turns
set auth_kind          = nullif(@auth_kind::text, ''),
    credential_owner   = @credential_owner::text,
    credential_account = @credential_account::text
where id = @id;

-- SetTurnTokens records the tokens the runtime reported for a turn.
-- name: SetTurnTokens :exec
update turns
set input_tokens       = @input_tokens,
    output_tokens      = @output_tokens,
    reasoning_tokens   = @reasoning_tokens,
    cache_read_tokens  = @cache_read_tokens,
    cache_write_tokens = @cache_write_tokens
where id = @id;
