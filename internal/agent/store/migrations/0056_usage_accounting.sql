-- What a unit of spend was paid with, how many tokens it used, and the spend no row held.
--
-- Credential. 0009 and 0013 recorded the provider a turn was billed by and nothing about the
-- credential: a Claude subscription, the company's Anthropic key and somebody's own key all
-- read "anthropic". The conductor decides whose credential a turn spends and whether it is a
-- subscription sign-in or an API key (conductor.credentialOwner, chathost.claudeAgentTurn,
-- openaiEndpoint), and until now it threw that answer away once the turn had started.
--
--   * auth_kind is 'api_key' or 'subscription'. A subscription turn still reports a cost: it
--     is the harness's list-price figure, which the usage screen shows apart from billed spend.
--     Null is a row from before this migration.
--   * credential_owner is the login whose own credential was spent, '' for the company's.
--   * credential_account is who the provider said signed in, or the key's hint: shown, never
--     authorised on. It is a copy, because the settings row it comes from is replaced by the
--     next sign-in and a bill has to keep saying who paid.
--
-- Tokens are summed over the turn's model steps as the harness reported them. Null where the
-- runtime reported none, which is every row from before this migration.
alter table turns       add column if not exists auth_kind          text;
alter table turns       add column if not exists credential_owner   text;
alter table turns       add column if not exists credential_account text;
alter table turns       add column if not exists input_tokens       bigint;
alter table turns       add column if not exists output_tokens      bigint;
alter table turns       add column if not exists reasoning_tokens   bigint;
alter table turns       add column if not exists cache_read_tokens  bigint;
alter table turns       add column if not exists cache_write_tokens bigint;

alter table delegations add column if not exists auth_kind          text;
alter table delegations add column if not exists credential_owner   text;
alter table delegations add column if not exists credential_account text;
alter table delegations add column if not exists input_tokens       bigint;
alter table delegations add column if not exists output_tokens      bigint;
alter table delegations add column if not exists reasoning_tokens   bigint;
alter table delegations add column if not exists cache_read_tokens  bigint;
alter table delegations add column if not exists cache_write_tokens bigint;

-- housekeeping_spend is model spend the conductor makes for itself rather than for a turn: a
-- conversation's rolling summary (conductor/summary.go). It ran on the assistant's model every
-- few minutes per idle conversation and no row recorded it, so the bill was short by exactly
-- that. It is not a turn: a turn is something somebody asked for, and the chat's history,
-- the runner list and the queue all read turns.
create table if not exists housekeeping_spend (
  id                 text        primary key,
  session_id         text        not null references sessions (id) on delete cascade,
  purpose            text        not null,
  status             text        not null,
  started_at         timestamptz not null,
  finished_at        timestamptz,
  num_turns          integer,
  cost_usd           numeric(12,6),
  agent              text,
  model              text,
  effort             text,
  provider           text,
  auth_kind          text,
  credential_owner   text,
  credential_account text,
  input_tokens       bigint,
  output_tokens      bigint,
  reasoning_tokens   bigint,
  cache_read_tokens  bigint,
  cache_write_tokens bigint
);
create index if not exists housekeeping_spend_started_idx on housekeeping_spend (started_at desc);

-- usage_rows is every unit of spend, one row each, with everything the usage screen breaks it
-- down by. Every usage query reads it, so a turn, a delegated task and a summary are counted by
-- one definition rather than three unions that drift apart.
--
-- A turn that ran no model is left out: an automatic review or a /playbook run records a turn
-- only so the delegation has one to hang off, with no backend, no task and no cost. Counting
-- it made every review two units of spend, one of them a $0 "unrecorded" model. Every turn
-- that did run a model has a backend (0009) or, from before 0009, a task of its own.
--
-- started_by is whose turn it was, and for a delegation the turn that delegated it. origin is
-- the source the screen groups by: a routine's run is 'routine', whichever conversation it ran
-- in, and anything else is its session's kind.
create or replace view usage_rows as
select t.id,
       'turn'::text                               as kind,
       coalesce(t.task_id, '')::text              as task_id,
       t.session_id,
       s.source_kind,
       s.source_key,
       s.playbook,
       s.profile,
       t.status,
       t.started_at,
       t.finished_at,
       t.num_turns,
       t.cost_usd,
       t.agent, t.model, t.effort, t.provider,
       t.started_by,
       t.auth_kind, t.credential_owner, t.credential_account,
       t.input_tokens, t.output_tokens, t.reasoning_tokens, t.cache_read_tokens, t.cache_write_tokens,
       coalesce(c.id, '')::text                   as chat_id,
       coalesce(c.title, '')::text                as chat_title,
       coalesce(r.id, '')::text                   as routine_id,
       coalesce(r.name, '')::text                 as routine_name,
       (case when r.id is not null then 'routine' else s.source_kind end)::text as origin
  from turns t
  join sessions s on s.id = t.session_id
  left join chats c on c.source_key = s.source_key
  left join routine_runs rr on rr.id = t.routine_run_id
  left join routines r on r.id = rr.routine_id
 where not (t.provider is null and t.task_id is null and coalesce(t.cost_usd, 0) = 0)
union all
select d.id,
       'delegation'::text,
       coalesce(d.task_id, '')::text,
       d.session_id,
       s.source_kind,
       s.source_key,
       d.playbook,
       s.profile,
       d.status,
       d.created_at,
       d.finished_at,
       d.num_turns,
       d.cost_usd,
       d.agent, d.model, d.effort, d.provider,
       coalesce(pt.started_by, '')::text,
       d.auth_kind, d.credential_owner, d.credential_account,
       d.input_tokens, d.output_tokens, d.reasoning_tokens, d.cache_read_tokens, d.cache_write_tokens,
       coalesce(c.id, '')::text,
       coalesce(c.title, '')::text,
       coalesce(r.id, '')::text,
       coalesce(r.name, '')::text,
       (case when r.id is not null then 'routine' else s.source_kind end)::text
  from delegations d
  join sessions s on s.id = d.session_id
  left join turns pt on pt.id = d.turn_id
  left join chats c on c.source_key = s.source_key
  left join routine_runs rr on rr.id = pt.routine_run_id
  left join routines r on r.id = rr.routine_id
union all
select h.id,
       h.purpose,
       ''::text,
       h.session_id,
       s.source_kind,
       s.source_key,
       ''::text,
       s.profile,
       h.status,
       h.started_at,
       h.finished_at,
       h.num_turns,
       h.cost_usd,
       h.agent, h.model, h.effort, h.provider,
       ''::text,
       h.auth_kind, h.credential_owner, h.credential_account,
       h.input_tokens, h.output_tokens, h.reasoning_tokens, h.cache_read_tokens, h.cache_write_tokens,
       coalesce(c.id, '')::text,
       coalesce(c.title, '')::text,
       ''::text,
       ''::text,
       'housekeeping'::text
  from housekeeping_spend h
  join sessions s on s.id = h.session_id
  left join chats c on c.source_key = s.source_key;
