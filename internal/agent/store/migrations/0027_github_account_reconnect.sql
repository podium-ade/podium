-- A connected GitHub account whose refresh token GitHub refused: it expired after six months
-- unused, or the person revoked the App. Only connecting again clears it.
alter table github_accounts add column needs_reconnect boolean not null default false;
