-- A delegation is a task a HOST turn asked for.
--
-- A host turn runs in the conductor's own process with no container around it, so the work
-- that needs a repository, a Docker daemon or a browser is done by an ordinary Podium task
-- it starts through the delegation tools. That task outlives the turn that asked for it:
-- a turn is one exchange and a task can run for hours, so the row below is what keeps the
-- work owned after its turn is over — the conductor resumes following every delegation that
-- is still running when it starts, and posts the outcome into the conversation whether or
-- not the turn that asked is still there to summarise it.
--
-- status shares the turns table's vocabulary on purpose: the same classify() maps a terminal
-- task onto both, and two spellings of "lost" would be two things to reason about.

create table if not exists delegations (
  id          text        primary key,
  session_id  text        not null references sessions(id),
  turn_id     text        not null references turns(id),
  -- trigger_ref is where the answer goes: the chat id, as on turns.
  trigger_ref text        not null,
  playbook    text        not null,
  instruction text        not null,
  -- task_id is null between the row being written and the control plane accepting the task.
  task_id     text,
  status      text        not null
                check (status in ('running', 'succeeded', 'failed', 'lost', 'cancelled', 'timeout')),
  final_text  text,
  created_at  timestamptz not null,
  finished_at timestamptz
);

create index if not exists delegations_turn_idx on delegations (turn_id, created_at);
create index if not exists delegations_ref_idx on delegations (trigger_ref, created_at desc);
-- The recovery pass on start reads exactly this: every delegated task still in flight.
create index if not exists delegations_running_idx on delegations (status) where status = 'running';
