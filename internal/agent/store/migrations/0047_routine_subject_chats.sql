-- A routine on Linear events may run in a chat per ticket (docs/agent.md, "On a Linear
-- event"): the events are grouped by what they are about — an issue, a project, a cycle, a
-- label — and each such subject has a chat of its own, which keeps that ticket's history. The
-- routine's own chat stays, for runs started by hand and for events about nothing in
-- particular.

-- chat_mode is where a routine's runs happen: 'routine', every run in its one chat, which is
-- what every routine before this one does; or 'subject', a chat per ticket. Only a routine on
-- Linear events may be 'subject'.
alter table routines add column if not exists chat_mode text not null default 'routine';
alter table routines drop constraint if exists routines_chat_mode_check;
alter table routines add constraint routines_chat_mode_check
  check (chat_mode = 'routine' or (chat_mode = 'subject' and trigger_kind = 'linear'));

-- subject_key is what an event is about, worked out when it is stored: "Issue:<id>" for an
-- issue and a comment on one, "Project:<id>" for a project and its updates, "Cycle:<id>",
-- "IssueLabel:<id>". subject_title is that thing in words ("ENG-12 Fix login"). Both are null
-- for an event about nothing that can be told apart, and for every event stored before this.
alter table routine_events add column if not exists subject_key text;
alter table routine_events add column if not exists subject_title text;
create index if not exists routine_events_pending_subject_idx
  on routine_events (routine_id, subject_key, received_at) where run_id is null;

-- routine_subject_chats is the chat each subject has, one per (routine, subject) and one
-- subject per chat. It is written when the first run about the subject is about to start.
-- Deleting the chat deletes the row, and the next event about that subject opens a fresh chat;
-- deleting the routine deletes every row, and its chats stay as ordinary chats.
create table if not exists routine_subject_chats (
  routine_id    text        not null references routines (id) on delete cascade,
  subject_key   text        not null,
  subject_title text        not null default '',
  chat_id       text        not null unique references chats (id) on delete cascade,
  created_at    timestamptz not null,
  primary key (routine_id, subject_key)
);
create index if not exists routine_subject_chats_created_idx on routine_subject_chats (routine_id, created_at);

-- chat_id is the chat a run happened in when it was not the routine's own: one subject's.
-- Null is the routine's own chat, which is every run before this one.
alter table routine_runs add column if not exists chat_id text references chats (id) on delete set null;
