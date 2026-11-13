-- Routines (0043). A personal routine is its owner's alone: every read and write a person
-- makes is filtered by owner_login, so knowing an id is not access to somebody else's.
--
-- A routine that runs on Linear events (0046) is handed each data change it asks for as a row
-- of routine_events, and the tick runs it with the rows still waiting.

-- name: InsertRoutine :one
insert into routines (
  id, scope, owner_login, name, schedule, timezone, instruction, instruction_updated_at,
  notes, personality_id, model, effort, enabled, chat_id, next_run_at, created_at, updated_at,
  repos, daily_cap_usd, created_by, trigger_kind, events, teams, chat_mode
) values (
  @id, @scope, sqlc.narg('owner_login'), @name, @schedule, @timezone, @instruction, @created_at,
  @notes, sqlc.narg('personality_id'), @model, @effort, true, @chat_id, @next_run_at, @created_at, @created_at,
  @repos::text[], sqlc.narg('daily_cap_usd'), @created_by, @trigger_kind, @events::text[], @teams::text[], @chat_mode
)
returning *;

-- name: GetRoutine :one
select * from routines where id = @id;

-- GetRoutineByChat is the routine a chat belongs to: its own chat, or one of its chats per
-- ticket (0047), which carry chats.routine_id as its own chat does.
-- name: GetRoutineByChat :one
select r.* from routines r
where r.chat_id = @chat_id
   or r.id = (select c.routine_id from chats c where c.id = @chat_id)
limit 1;

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
    trigger_kind = @trigger_kind,
    events = @events::text[],
    teams = @teams::text[],
    chat_mode = @chat_mode,
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
-- ticks from taking the same row, though only the leading conductor ticks. A routine that runs
-- on Linear events has no schedule, so it is never due.
-- name: DueRoutines :many
select * from routines
where enabled and trigger_kind = 'schedule' and next_run_at <= @now
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

-- InsertRoutineEvents stores one Linear delivery for every enabled routine that asks for its
-- event, in its team when the routine names teams. It is one statement, so the webhook that
-- calls it does one round trip whatever the number of routines. A delivery stored before is
-- not stored again.
-- name: InsertRoutineEvents :execrows
insert into routine_events (routine_id, delivery_id, event, payload, received_at, subject_key, subject_title)
select r.id, @delivery_id::text, @event::text, @payload::jsonb, @received_at,
       sqlc.narg('subject_key')::text, sqlc.narg('subject_title')::text
  from routines r
 where r.enabled and r.trigger_kind = 'linear' and @event::text = any(r.events)
   and (cardinality(r.teams) = 0 or @team_key::text = any(r.teams))
on conflict (routine_id, delivery_id) do nothing;

-- RoutinesWithPendingEvents is every enabled linear routine with an event waiting.
-- name: RoutinesWithPendingEvents :many
select r.* from routines r
where r.enabled and r.trigger_kind = 'linear'
  and exists (select 1 from routine_events e where e.routine_id = r.id and e.run_id is null)
order by r.id
limit @page_limit::int;

-- CapPendingRoutineEvents drops a routine's waiting events beyond the newest @keep.
-- name: CapPendingRoutineEvents :execrows
delete from routine_events d
where d.routine_id = @routine_id::text and d.run_id is null
  and d.id not in (
    select k.id from routine_events k
    where k.routine_id = @routine_id::text and k.run_id is null
    order by k.received_at desc, k.id desc
    limit @keep::int
  );

-- AttachRoutineEvents gives a run the oldest of its routine's waiting events, at most @max.
-- name: AttachRoutineEvents :execrows
update routine_events set run_id = @run_id::text
where id in (
  select p.id from routine_events p
  where p.routine_id = @routine_id::text and p.run_id is null
  order by p.received_at, p.id
  limit @max::int
  for update skip locked
);

-- ReleaseRoutineEvents puts a run's events back to waiting: the run never started.
-- name: ReleaseRoutineEvents :execrows
update routine_events set run_id = null where run_id = @run_id;

-- name: RoutineEventsForRun :many
select * from routine_events where run_id = @run_id order by received_at, id;

-- PruneRoutineEvents deletes the events nobody will run: waiting since before
-- @pending_before, and given to a run since before @kept_before.
-- name: PruneRoutineEvents :execrows
delete from routine_events
where received_at < @pending_before and (run_id is null or received_at < @kept_before);

-- PendingRoutineEventCounts is how many events wait for each named routine.
-- name: PendingRoutineEventCounts :many
select routine_id, count(*)::int as pending from routine_events
where routine_id = any(@routine_ids::text[]) and run_id is null
group by routine_id;

-- CountRoutineRunsSince counts a routine's runs of one trigger in a window, for the flood guard.
-- name: CountRoutineRunsSince :one
select count(*)::int from routine_runs
where routine_id = @routine_id and trigger = @trigger and created_at >= @since;

-- DeleteUnstartedRoutineRun removes a run that was claimed and then had nothing to do.
-- name: DeleteUnstartedRoutineRun :exec
delete from routine_runs where id = @id and status = 'running' and turn_id is null;

-- SetRoutineRunChat records that a run happened in one subject's chat rather than the
-- routine's own (0047).
-- name: SetRoutineRunChat :exec
update routine_runs set chat_id = @chat_id where id = @id;

-- PendingRoutineSubjects is what a routine's waiting events are about, the subject waiting
-- longest first, with the chat each already has. An event about nothing in particular is the
-- subject ''.
-- name: PendingRoutineSubjects :many
select coalesce(e.subject_key, '')::text as subject_key,
       coalesce(max(e.subject_title), '')::text as subject_title,
       min(e.received_at)::timestamptz as oldest,
       count(*)::int as pending,
       coalesce(max(sc.chat_id), '')::text as chat_id
  from routine_events e
  left join routine_subject_chats sc on sc.routine_id = e.routine_id and sc.subject_key = e.subject_key
 where e.routine_id = @routine_id::text and e.run_id is null
 group by coalesce(e.subject_key, '')
 order by min(e.received_at), coalesce(e.subject_key, '')
 limit @page_limit::int;

-- AttachRoutineSubjectEvents is AttachRoutineEvents for one subject's waiting events alone.
-- name: AttachRoutineSubjectEvents :execrows
update routine_events set run_id = @run_id::text
where id in (
  select p.id from routine_events p
  where p.routine_id = @routine_id::text and p.run_id is null
    and coalesce(p.subject_key, '') = @subject_key::text
  order by p.received_at, p.id
  limit @max::int
  for update skip locked
);

-- name: GetRoutineSubjectChat :one
select * from routine_subject_chats where routine_id = @routine_id and subject_key = @subject_key;

-- name: GetRoutineSubjectChatByChat :one
select * from routine_subject_chats where chat_id = @chat_id;

-- InsertRoutineSubjectChat gives a subject its chat. No row back means another tick gave it
-- one first.
-- name: InsertRoutineSubjectChat :one
insert into routine_subject_chats (routine_id, subject_key, subject_title, chat_id, created_at)
values (@routine_id, @subject_key, @subject_title, @chat_id, @created_at)
on conflict do nothing
returning *;

-- CountRoutineSubjectChatsSince is how many chats per ticket a routine opened in a window.
-- name: CountRoutineSubjectChatsSince :one
select count(*)::int from routine_subject_chats where routine_id = @routine_id and created_at >= @since;
