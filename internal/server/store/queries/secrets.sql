-- name: UpsertSecret :one
insert into secrets (name, ciphertext, nonce, version, key_id, created_by, updated_at)
values (@name, @ciphertext, @nonce, 1, @key_id, @created_by, now())
on conflict (name) do update
  set ciphertext = excluded.ciphertext,
      nonce      = excluded.nonce,
      version    = secrets.version + 1,
      key_id     = excluded.key_id,
      updated_at = now()
returning *;

-- name: GetSecret :one
select * from secrets where name = @name;

-- name: GetSecrets :many
select * from secrets where name = any(@names::text[]) order by name;

-- name: ListSecrets :many
select * from secrets order by name;

-- name: DeleteSecret :execrows
delete from secrets where name = @name;

-- ListSecretsForUpdate is the rotation read: it locks every row so a concurrent SetSecret
-- waits rather than being re-encrypted under a key it did not use.
-- name: ListSecretsForUpdate :many
select * from secrets order by name for update;

-- name: ReEncryptSecret :execrows
update secrets
set ciphertext = @ciphertext,
    nonce      = @nonce,
    key_id     = @key_id
where name = @name;
