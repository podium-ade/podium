-- name: UpsertUser :one
insert into users (login, display_name, hosted_domain)
values (@login, sqlc.narg(display_name)::text, sqlc.narg(hosted_domain)::text)
on conflict (login) do update
  set display_name = coalesce(sqlc.narg(display_name)::text, users.display_name),
      hosted_domain = coalesce(users.hosted_domain, sqlc.narg(hosted_domain)::text)
returning *;

-- name: GetUser :one
select * from users where login = @login;

-- name: SetUserRoles :one
update users set roles = @roles where login = @login
returning *;
