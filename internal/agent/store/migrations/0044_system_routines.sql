-- System routines and the safety that comes with them (docs/agent.md, "Routines").
--
-- A system routine is configured by an admin and runs as nobody: the company's model key, the
-- global MCP servers, and GitHub through the App, read-only and only on the repositories the
-- routine names. Its chat has no owner; admins read it and nobody types in it.

-- repos is a system routine's GitHub reach: owner/name, read through the App. A personal
-- routine reads as its owner and leaves it empty.
alter table routines add column if not exists repos text[] not null default '{}';
-- daily_cap_usd is what the routine's runs may spend in a day, in its timezone. Null is no cap
-- of its own. Reaching it pauses the routine until paused_until, the next midnight there.
alter table routines add column if not exists daily_cap_usd numeric(10, 2);
alter table routines add column if not exists paused_until timestamptz;
-- created_by is who set the routine up: its owner for a personal one, the admin for a system one.
alter table routines add column if not exists created_by text not null default '';
update routines set created_by = owner_login where created_by = '' and owner_login is not null;
create index if not exists routines_paused_until_idx on routines (paused_until) where paused_until is not null;

-- routine_revisions is the instruction's history: every change to what a routine is told to do,
-- who made it, and when. The first row is the routine as created.
create table if not exists routine_revisions (
  id          bigserial   primary key,
  routine_id  text        not null references routines (id) on delete cascade,
  instruction text        not null,
  notes       text        not null default '',
  schedule    text        not null,
  timezone    text        not null,
  changed_by  text        not null,
  changed_at  timestamptz not null default now()
);
create index if not exists routine_revisions_routine_idx on routine_revisions (routine_id, id desc);

insert into routine_revisions (routine_id, instruction, notes, schedule, timezone, changed_by, changed_at)
select r.id, r.instruction, r.notes, r.schedule, r.timezone, coalesce(r.owner_login, ''), r.created_at
  from routines r
 where not exists (select 1 from routine_revisions v where v.routine_id = r.id);

-- The inbox learns one more kind: a routine of yours was paused by Podium (it kept failing or
-- reached its spend cap) or by an admin.
alter table notifications drop constraint if exists notifications_kind_check;
alter table notifications add constraint notifications_kind_check check (kind in ('share', 'mention', 'routine'));
-- detail is what a routine notification says happened ("5 runs failed within an hour").
alter table notifications add column if not exists detail text not null default '';
