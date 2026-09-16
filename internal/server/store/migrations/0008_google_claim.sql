-- Google Workspace sign-in and the instance claim. SAML is a later slice: this only stores
-- what a claim and a browser session need.
--
--  1. users.hosted_domain: the Google `hd` claim (or the email domain for a tailnet login).
--     Empty until something that knows a domain has seen this person.
--  2. instance: a singleton. Inserting the row is the claim; there is no unclaim in v1.
--  3. sessions: hashed bearer/cookie tokens for a Google login. Podium never stores the
--     plaintext, the same way enrollment tokens work.

alter table users add column if not exists hosted_domain text;

create table if not exists instance (
  id            int         primary key default 1 check (id = 1),
  hosted_domain text        not null,
  claimed_by    text        not null references users (login),
  claimed_at    timestamptz not null default now()
);

create table if not exists sessions (
  id         text        primary key,
  token_hash bytea       not null unique,
  login      text        not null references users (login),
  expires_at timestamptz not null,
  created_at timestamptz not null default now()
);

create index if not exists sessions_expires_at_idx on sessions (expires_at);
