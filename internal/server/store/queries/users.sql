-- name: UpsertUser :one
insert into users (login, display_name)
values (@login, sqlc.narg(display_name)::text)
on conflict (login) do update
  set display_name = coalesce(sqlc.narg(display_name)::text, users.display_name)
returning *;

-- name: GetUser :one
select * from users where login = @login;
