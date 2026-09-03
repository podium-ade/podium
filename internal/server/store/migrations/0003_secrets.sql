-- Secrets (step 09). Two tables that 0001_init.sql trimmed for MVP-0:
--
--  1. secrets: one row per named secret, holding AES-256-GCM ciphertext and its nonce and
--     nothing else. The plaintext never reaches Postgres. key_id names the master key the
--     row is encrypted under, so a half-finished rotation is visible rather than silent,
--     and version increments on every set so an operator can see a value has moved.
--     There is no unique constraint beyond the name: a secret is its name.
--
--  2. audit_log: who did what to which subject. Step 09 writes secret.set, secret.delete
--     and secret.resolve rows; details is jsonb and must never carry a secret value.

create table if not exists secrets (
  name       text        primary key,
  ciphertext bytea       not null,
  nonce      bytea       not null,
  version    int         not null default 1,
  key_id     text        not null,
  created_by text        not null,
  updated_at timestamptz not null default now()
);

create table if not exists audit_log (
  id      bigserial   primary key,
  ts      timestamptz not null default now(),
  actor   text        not null,
  action  text        not null,
  subject text        not null,
  details jsonb       not null default '{}'
);

create index if not exists audit_log_ts_idx on audit_log (ts desc);
create index if not exists audit_log_action_subject_idx on audit_log (action, subject);
