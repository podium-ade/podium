-- Sandbox providers: the cloud sandbox services sandbox nodes run tasks on, one row per
-- provider. settings holds the provider's options, none of them secret; the API key or
-- token is encrypted under the master key exactly as a secret is (AES-256-GCM, with
-- "sandbox-provider:<name>" as the additional data).
create table if not exists sandbox_providers (
  name       text        primary key,
  settings   jsonb       not null default '{}'::jsonb,
  ciphertext bytea       not null,
  nonce      bytea       not null,
  key_id     text        not null,
  created_by text        not null,
  updated_at timestamptz not null default now()
);
