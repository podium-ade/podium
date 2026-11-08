-- Routines (0043). A personal routine is its owner's alone: every read and write a person
-- makes is filtered by owner_login, so knowing an id is not access to somebody else's.

-- name: InsertRoutine :one
insert into routines (
  id, scope, owner_login, name, schedule, timezone, instruction, instruction_updated_at,
  notes, personality_id, model, effort, enabled, chat_id, next_run_at, created_at, updated_at,
  repos, daily_cap_usd, created_by
) values (
  @id, @scope, sqlc.narg('owner_login'), @name, @schedule, @timezone, @instruction, @created_at,
  @notes, sqlc.narg('personality_id'), @model, @effort, true, @chat_id, @next_run_at, @created_at, @created_at,
  @repos::text[], sqlc.narg('daily_cap_usd'), @created_by
)
returning *;

-- name: GetRoutine :one
select * from routines where id = @id;

-- name: GetRoutineByChat :one
select * from routines where chat_id = @chat_id;

-- name: ListRoutinesByOwner :many
select * from routines where owner_login = @owner_login order by name, id;

-- ListVisibleRoutines is a person's own routines and, for an admin, every system routine.
-- name: ListVisibleRoutines :many
select * from routines
where owner_login = @owner_login or (@with_system::bool and scope = 'system')
order by scope desc, name, id;

-- ListPersonalRoutines is every person's routines, for the admin's metadata view.
-- name: ListPersonalRoutines :many
select * from routines where scope = 'personal' order by owner_login, name, id;

-- CountRoutinesByOwner is how many routines a person has, against their limit.
-- name: CountRoutinesByOwner :one
select count(*)::int from routines where owner_login = @owner_login;

-- name: ListAllRoutines :many
select * from routines order by scope desc, owner_login, name, id;

-- UpdateRoutine replaces what a person edits. instruction_updated_at moves only when the
-- instruction itself changed, so the next brief can say so.
-- name: UpdateRoutine :one
update routines
set name = @name,
    schedule = @schedule,
    timezone = @timezone,
    instruction_updated_at = case when instruction = @instruction then instruction_updated_at else @updated_at end,
    instruction = @instruction,
    notes = @notes,
    personality_id = sqlc.narg('personality_id'),
    model = @model,
    effort = @effort,
    repos = @repos::text[],
    daily_cap_usd = sqlc.narg('daily_cap_usd'),
    next_run_at = @next_run_at,
    updated_at = @updated_at
where id = @id
returning *;

-- name: DeleteRoutine :execrows
delete from routines where id = @id;

-- SetRoutineEnabled pauses or resumes. A resume clears the reason and the failures behind it
-- by moving next_run_at to the next time the schedule names.
-- name: SetRoutineEnabled :one
update routines
set enabled = @enabled,
    paused_reason = @paused_reason,
    paused_until = sqlc.narg('paused_until'),
    next_run_at = @next_run_at,
    updated_at = @updated_at
where id = @id
returning *;

-- name: SetRoutineState :one
update routines
set state = @state, state_updated_at = @updated_at
where id = @id
returning *;

-- name: SetRoutineNotes :one
update routines
set notes = @notes, proposed_note = @proposed_note, updated_at = @updated_at
where id = @id
returning *;

-- name: SetRoutineProposedNote :exec
update routines set proposed_note = @proposed_note where id = @id;

-- DueRoutines is every enabled routine whose time has come. FOR UPDATE SKIP LOCKED keeps two
-- ticks from taking the same row, though only the leading conductor ticks.
-- name: DueRoutines :many
select * from routines
where enabled and next_run_at <= @now
order by next_run_at
limit @page_limit::int
for update skip locked;

-- name: AdvanceRoutine :exec
update routines
set next_run_at = @next_run_at, last_run_at = @last_run_at
where id = @id;

-- InsertRoutineRun claims one firing. No row back means another conductor already claimed it.
-- name: InsertRoutineRun :one
insert into routine_runs (id, routine_id, scheduled_for, trigger, status, created_at, finished_at)
values (@id, @routine_id, @scheduled_for, @trigger, @status, @created_at, sqlc.narg('finished_at'))
on conflict (routine_id, scheduled_for, trigger) do nothing
returning *;

-- name: GetRoutineRun :one
select * from routine_runs where id = @id;

-- name: ListRoutineRuns :many
select * from routine_runs
where routine_id = @routine_id
  and (@include_idle::bool or status not in ('idle', 'skipped_overlap', 'skipped_paused'))
  and (@before_id::text = '' or id < @before_id::text)
order by id desc
limit @page_limit::int;

-- name: RunningRoutineRuns :many
select * from routine_runs where status = 'running' order by created_at;

-- SettleRoutineRun records how a run ended. Only a run still running is settled, so a run
-- the tool already reported idle is not overwritten by the turn's own ending.
-- name: SettleRoutineRun :one
update routine_runs
set status = @status,
    summary = case when @summary::text = '' then summary else @summary::text end,
    cost_usd = sqlc.narg('cost_usd'),
    finished_at = @finished_at
where id = @id and status = 'running'
returning *;

-- name: SetRoutineRunTurn :exec
update routine_runs set turn_id = @turn_id where id = @id;

-- name: ReportRoutineRun :exec
update routine_runs set reported = @reported, summary = @summary where id = @id;

-- name: SetTurnRoutineRun :exec
update turns set routine_run_id = @routine_run_id where id = @id;

-- name: TurnForRoutineRun :one
select * from turns where routine_run_id = @routine_run_id order by started_at desc limit 1;

-- name: SetRoutineIdleCount :exec
update routines
set idle_count = case when @idle::bool then idle_count + 1 else 0 end
where id = @id;

-- RoutineFailuresSince counts the runs that failed in a window, for auto-pause.
-- name: RoutineFailuresSince :one
select count(*)::int from routine_runs
where routine_id = @routine_id and status = 'failed' and created_at >= @since;

-- SessionBusy is whether a session has a turn running or a turn waiting in its queue: a
-- scheduled run is never put behind either.
-- name: SessionBusy :one
select (
  exists (select 1 from turns t where t.session_id = @session_id::text and t.status = 'running')
  or exists (select 1 from turn_queue q where q.session_id = @session_id::text)
)::bool;

-- CountChatsStartedByRoutineSince is how many chats a routine's turns opened in a window.
-- name: CountChatsStartedByRoutineSince :one
select count(*)::int from chats c
  join turns t on t.id = c.started_by_turn
  join routine_runs r on r.id = t.routine_run_id
where r.routine_id = @routine_id and c.created_at >= @since;

-- name: SetChatRoutine :exec
update chats set routine_id = @routine_id where id = @id;

-- RoutineRunCostToday is what a routine's runs spent since a moment: their turns, plus the
-- tasks those turns delegated, which a turn's cost does not include.
-- name: RoutineSpendSince :one
select coalesce(sum(t.cost_usd), 0)::float8 from turns t
  join routine_runs r on r.id = t.routine_run_id
where r.routine_id = @routine_id and t.started_at >= @since;

-- name: InsertRoutineRevision :exec
insert into routine_revisions (routine_id, instruction, notes, schedule, timezone, changed_by, changed_at)
values (@routine_id, @instruction, @notes, @schedule, @timezone, @changed_by, @changed_at);

-- name: ListRoutineRevisions :many
select * from routine_revisions where routine_id = @routine_id order by id desc limit @page_limit::int;

-- LastRoutineRuns is each routine's latest run that was not skipped, for the run status a row
-- shows.
-- name: LastRoutineRuns :many
select distinct on (routine_id) routine_id, status, created_at, finished_at from routine_runs
where routine_id = any(@routine_ids::text[]) and status not in ('skipped_overlap', 'skipped_paused')
order by routine_id, created_at desc;

-- DueToResume is every routine Podium paused until a moment that has passed: a spend cap's
-- day is over.
-- name: DueToResume :many
select * from routines
where not enabled and paused_until is not null and paused_until <= @now
order by paused_until
limit @page_limit::int;

-- PersonSpendSince is what a person's routines spent since a moment.
-- name: PersonSpendSince :one
select coalesce(sum(t.cost_usd), 0)::float8 from turns t
  join routine_runs r on r.id = t.routine_run_id
  join routines o on o.id = r.routine_id
where o.owner_login = @owner_login and t.started_at >= @since;

-- RunningRoutineRunOf is a routine's run still going, if any.
-- name: RunningRoutineRunOf :one
select * from routine_runs where routine_id = @routine_id and status = 'running' order by created_at desc limit 1;
