-- A routine may run on GitHub events as well as on Linear ones (docs/agent.md, "On a GitHub
-- event"): "when a workflow run fails on a pull request, look at why". The GitHub App's webhook
-- hands every delivery it receives to the routines that ask for it, and they wait in
-- routine_events and run exactly as a Linear routine's do.
--
-- A github routine names GitHub events in events ("workflow_run.failed") and keeps the
-- repositories it is limited to in teams: "owner/name", or "owner" for all of an owner's,
-- lower case; empty is every repository the App is installed on.
alter table routines drop constraint if exists routines_trigger_kind_check;
alter table routines add constraint routines_trigger_kind_check
  check (trigger_kind in ('schedule', 'linear', 'github'));

-- A github routine runs, by default, in each pull request's conversation: chat_mode 'subject',
-- where an event about a pull request is a turn of github:<owner>/<name>#<number>, the chat its
-- reviews and every web message about it are in. 'routine' keeps its runs in a chat of its own.
-- subject_key is "PullRequest:<owner>/<name>#<number>" for an event about one.
alter table routines drop constraint if exists routines_chat_mode_check;
alter table routines add constraint routines_chat_mode_check
  check (chat_mode = 'routine' or (chat_mode = 'subject' and trigger_kind in ('linear', 'github')));
