-- One signed-in person's connected GitHub account. The token is not here: it is that
-- person's personal secret github.token in the control plane. This row is what the UI shows
-- and the identity a turn commits as.

create table github_accounts (
  login        text primary key,
  github_id    bigint not null,
  github_login text not null,
  name         text not null default '',
  connected_at timestamptz not null
);
