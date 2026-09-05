-- Skills an operator created in the web UI.
--
-- The profile directory on the conductor's host stays authoritative for the names it holds:
-- this table only ever contributes names skills/*.yaml does not define, and a row whose name
-- a file later claims is shadowed rather than merged. See internal/agent/profiles.Merge.
--
-- definition is the same document a skills/<name>.yaml holds, as JSON, validated by exactly
-- the code that validates the file. It NAMES secrets and never carries one — a secret's
-- value lives in the control plane's encrypted store and cannot be read back at all.
create table if not exists skills (
  name       text        primary key,
  definition jsonb       not null,
  updated_at timestamptz not null,
  updated_by text        not null
);
