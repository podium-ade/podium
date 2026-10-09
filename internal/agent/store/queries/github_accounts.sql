-- name: GetGitHubAccount :one
select login, github_id, github_login, name, connected_at
from github_accounts
where login = @login;

-- name: UpsertGitHubAccount :exec
insert into github_accounts (login, github_id, github_login, name, connected_at)
values (@login, @github_id, @github_login, @name, @connected_at)
on conflict (login) do update
  set github_id = excluded.github_id,
      github_login = excluded.github_login,
      name = excluded.name,
      connected_at = excluded.connected_at;

-- name: DeleteGitHubAccount :exec
delete from github_accounts where login = @login;
