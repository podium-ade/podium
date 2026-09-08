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
select to_char((t.started_at + make_interval(mins => @tz_offset_minutes::int))::date,
               'YYYY-MM-DD')::text                                           as day,
       coalesce(sum(t.cost_usd), 0)::float8                                  as cost_usd,
       count(*)::int                                                         as turns,
       coalesce(sum(t.num_turns), 0)::int                                    as model_turns,
       count(*) filter (where t.cost_usd is null)::int                       as unpriced
from turns t
where t.started_at >= @from_time and t.started_at < @to_time
group by day
order by day;

-- ListTurnCosts is one row per turn in the range, with the session fields that say what
-- spent it. The join is to sessions and no further: a task_id is a string this database has
-- no opinion about, and the browser is what puts the two halves together.
-- name: ListTurnCosts :many
select t.id, t.task_id, t.session_id, t.status, t.started_at, t.finished_at,
       t.num_turns, t.cost_usd, t.agent, t.model, t.effort, t.provider,
       s.source_kind, s.source_key, s.playbook, s.profile
from turns t
join sessions s on s.id = t.session_id
where t.started_at >= @from_time and t.started_at < @to_time
order by t.started_at desc, t.id desc
limit @page_limit::int;

-- UsageByBackend is spend grouped by what actually ran it. It is a server-side aggregate for
-- the same reason the day rows are: the costs page is capped, and grouping a capped page
-- would under-report whichever model happened to fall off the end of it.
--
-- Turns from before the columns existed group under empty strings, which the API reports as
-- unrecorded rather than as a model named "".
-- name: UsageByBackend :many
select coalesce(provider, '')::text                as provider,
       coalesce(agent, '')::text                   as agent,
       coalesce(model, '')::text                   as model,
       coalesce(effort, '')::text                  as effort,
       coalesce(sum(cost_usd), 0)::float8          as cost_usd,
       count(*)::int                               as turns,
       coalesce(sum(num_turns), 0)::int            as model_turns,
       count(*) filter (where cost_usd is null)::int as unpriced
from turns
where started_at >= @from_time and started_at < @to_time
group by provider, agent, model, effort
order by cost_usd desc, turns desc;
