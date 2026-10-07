-- name: UpsertUser :one
insert into users (login, display_name, hosted_domain, picture_url, last_seen_at)
values (@login, sqlc.narg(display_name)::text, sqlc.narg(hosted_domain)::text, sqlc.narg(picture_url)::text, now())
on conflict (login) do update
  set display_name = coalesce(sqlc.narg(display_name)::text, users.display_name),
      hosted_domain = coalesce(users.hosted_domain, sqlc.narg(hosted_domain)::text),
      picture_url = coalesce(sqlc.narg(picture_url)::text, users.picture_url),
      last_seen_at = now()
returning *;

-- name: TouchUserLastSeen :exec
update users set last_seen_at = now()
where login = @login and last_seen_at < now() - interval '1 minute';

-- name: GetUser :one
select * from users where login = @login;

-- name: ListUsers :many
select * from users order by last_seen_at desc, login;

-- name: ListOwnerLoginsForUpdate :many
select login from users where 'owner' = any (roles) for update;

-- name: SetUserRoles :one
update users set roles = @roles where login = @login
returning *;
