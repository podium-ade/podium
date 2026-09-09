-- name: UpsertRegistry :one
insert into registries (host, username, ciphertext, nonce, key_id, created_by, updated_at)
values (@host, @username, @ciphertext, @nonce, @key_id, @created_by, now())
on conflict (host) do update
  set username   = excluded.username,
      ciphertext = excluded.ciphertext,
      nonce      = excluded.nonce,
      key_id     = excluded.key_id,
      updated_at = now()
returning *;

-- name: GetRegistries :many
select * from registries where host = any(@hosts::text[]) order by host;

-- name: ListRegistries :many
select * from registries order by host;

-- name: DeleteRegistry :execrows
delete from registries where host = @host;

-- name: ListRegistriesForUpdate :many
select * from registries order by host for update;

-- name: ReEncryptRegistry :execrows
update registries
set ciphertext = @ciphertext,
    nonce      = @nonce,
    key_id     = @key_id
where host = @host;
