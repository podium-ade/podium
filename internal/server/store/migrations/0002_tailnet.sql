-- Tailnet transport (step 11). Two additions:
--
--  1. users: the tailnet transport records a login the first time it sees one, so roles can be
--     attached to a person later. There is no password column and never will be — identity comes
--     from Tailscale's WhoIs, not from anything Podium stores.
--  2. nodes.ts_stable_id: the Tailscale device a node enrolled from. Once set, later Hellos must
--     come from the same device, so a stolen identity.json is useless on another machine.
--     NULL means unbound: enrolled over the dev transport, or rekeyed by an admin.

create table if not exists users (
  login         text        primary key,
  display_name  text,
  roles         text[]      not null default '{}',
  first_seen_at timestamptz not null default now()
);

alter table nodes add column if not exists ts_stable_id text;

create index if not exists nodes_ts_stable_id_idx on nodes (ts_stable_id) where ts_stable_id is not null;
