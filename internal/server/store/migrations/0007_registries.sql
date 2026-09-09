-- Registry credentials: one login per registry host, the password encrypted under the
-- master key exactly as a secret is (AES-256-GCM, the host as the additional data).
create table if not exists registries (
  host       text        primary key,
  username   text        not null,
  ciphertext bytea       not null,
  nonce      bytea       not null,
  key_id     text        not null,
  created_by text        not null,
  updated_at timestamptz not null default now()
);
