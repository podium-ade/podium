-- name: CreateSession :one
insert into sessions (id, token_hash, login, expires_at)
values (@id, @token_hash, @login, @expires_at)
returning *;

-- name: GetSessionByTokenHash :one
select * from sessions
where token_hash = @token_hash and expires_at > now();

-- name: DeleteSessionByTokenHash :exec
delete from sessions where token_hash = @token_hash;
