-- name: UpsertUser :one
insert into users (login, display_name, hosted_domain, picture_url)
values (@login, sqlc.narg(display_name)::text, sqlc.narg(hosted_domain)::text, sqlc.narg(picture_url)::text)
on conflict (login) do update
  set display_name = coalesce(sqlc.narg(display_name)::text, users.display_name),
      hosted_domain = coalesce(users.hosted_domain, sqlc.narg(hosted_domain)::text),
      picture_url = coalesce(sqlc.narg(picture_url)::text, users.picture_url)
returning *;

-- name: GetUser :one
select * from users where login = @login;

-- name: SetUserRoles :one
update users set roles = @roles where login = @login
returning *;
