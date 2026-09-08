-- name: CreateTurn :one
insert into turns (id, session_id, task_id, trigger_ref, status, started_at,
                   agent, model, effort, provider)
values (@id, @session_id, @task_id, @trigger_ref, @status, @started_at,
        nullif(@agent::text, ''), nullif(@model::text, ''), nullif(@effort::text, ''), nullif(@provider::text, ''))
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

-- UsageByDay buckets spend into the caller's own days. The offset is added to the stored
-- UTC instant before the date is taken, so a turn at 23:30 in New York lands on the day the
-- operator ran it rather than on the next one.
-- name: UsageByDay :many
-- The day leaves as text rather than as a date, because a date would arrive as pgtype.Date
-- and the point of the overrides in sqlc.yaml is that no store signature speaks pgx.
--
-- Both halves of what this bot spends, and it has to be both: a turn is what a Slack mention
-- or a Linear ticket costs, and a DELEGATION is what a conversation costs, because the
-- assistant answers on the host and hands the work to a container. Reading turns alone
-- reported the relay's pennies as the whole bill.
select to_char((r.started_at + make_interval(mins => @tz_offset_minutes::int))::date,
               'YYYY-MM-DD')::text                                           as day,
       coalesce(sum(r.cost_usd), 0)::float8                                  as cost_usd,
       count(*)::int                                                         as turns,
       coalesce(sum(r.num_turns), 0)::int                                    as model_turns,
       count(*) filter (where r.cost_usd is null)::int                       as unpriced
from (
  select t.started_at, t.cost_usd, t.num_turns from turns t
   where t.started_at >= @from_time and t.started_at < @to_time
  union all
  select d.created_at, d.cost_usd, d.num_turns from delegations d
   where d.created_at >= @from_time and d.created_at < @to_time
) r
group by day
order by day;

-- ListTurnCosts is one row per unit of spend in the range — a turn, or a task a turn
-- delegated — with the session fields that say what spent it. The join is to sessions and no
-- further: a task_id is a string this database has no opinion about, and the browser is what
-- puts the two halves together.
--
-- The PLAYBOOK comes from different places on purpose. A turn's is the session's, because a
-- thread runs one playbook; a delegation's is its own, because the session it belongs to is a
-- conversation and runs none. That is what makes the breakdown read "assistant" for what the
-- host answered and the playbook's name for what the container did.
-- name: ListTurnCosts :many
select t.id, t.task_id, t.session_id, t.status, t.started_at, t.finished_at,
       t.num_turns, t.cost_usd, t.agent, t.model, t.effort, t.provider,
       s.source_kind, s.source_key, s.playbook, s.profile
from turns t
join sessions s on s.id = t.session_id
where t.started_at >= @from_time and t.started_at < @to_time
union all
select d.id, coalesce(d.task_id, '')::text, d.session_id, d.status, d.created_at, d.finished_at,
       d.num_turns, d.cost_usd, d.agent, d.model, d.effort, d.provider,
       s.source_kind, s.source_key, d.playbook, s.profile
from delegations d
join sessions s on s.id = d.session_id
where d.created_at >= @from_time and d.created_at < @to_time
order by started_at desc, id desc
limit @page_limit::int;

-- UsageByBackend is spend grouped by what actually ran it. It is a server-side aggregate for
-- the same reason the day rows are: the costs page is capped, and grouping a capped page
-- would under-report whichever model happened to fall off the end of it.
--
-- Rows from before the columns existed group under empty strings, which the API reports as
-- unrecorded rather than as a model named "". Delegations are in here for the same reason
-- they are in the other two: a conversation's spend is its delegated tasks', and a bill that
-- left them out was not a bill.
-- name: UsageByBackend :many
select coalesce(r.provider, '')::text                as provider,
       coalesce(r.agent, '')::text                   as agent,
       coalesce(r.model, '')::text                   as model,
       coalesce(r.effort, '')::text                  as effort,
       coalesce(sum(r.cost_usd), 0)::float8          as cost_usd,
       count(*)::int                                 as turns,
       coalesce(sum(r.num_turns), 0)::int            as model_turns,
       count(*) filter (where r.cost_usd is null)::int as unpriced
from (
  select provider, agent, model, effort, cost_usd, num_turns from turns
   where started_at >= @from_time and started_at < @to_time
  union all
  select provider, agent, model, effort, cost_usd, num_turns from delegations
   where created_at >= @from_time and created_at < @to_time
) r
group by r.provider, r.agent, r.model, r.effort
order by cost_usd desc, turns desc;
