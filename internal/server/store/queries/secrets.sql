-- name: UpsertSecret :one
insert into secrets (scope, owner, name, ciphertext, nonce, version, key_id, created_by, updated_at)
values (@scope, @owner, @name, @ciphertext, @nonce, 1, @key_id, @created_by, now())
on conflict (scope, owner, name) do update
  set ciphertext = excluded.ciphertext,
      nonce      = excluded.nonce,
      version    = secrets.version + 1,
      key_id     = excluded.key_id,
      updated_at = now()
returning *;

-- name: GetSecret :one
select * from secrets where scope = @scope and owner = @owner and name = @name;

-- name: GetSecrets :many
select * from secrets
where scope = 'global' and owner = '' and name = any(@names::text[])
order by name;

-- name: ListSecrets :many
select * from secrets order by scope, owner, name;

-- name: ListVisibleSecrets :many
select * from secrets
where scope = 'global' or (scope = 'personal' and owner = @owner)
order by scope, name;

-- name: SecretNameInOtherScope :one
select exists (
  select 1 from secrets where name = @name and scope <> @scope
) as taken;

-- name: DeleteSecret :execrows
delete from secrets where scope = @scope and owner = @owner and name = @name;

-- ListSecretsForUpdate is the rotation read: it locks every row so a concurrent SetSecret
-- waits rather than being re-encrypted under a key it did not use.
-- name: ListSecretsForUpdate :many
select * from secrets order by scope, owner, name for update;

-- name: ReEncryptSecret :execrows
update secrets
set ciphertext = @ciphertext,
    nonce      = @nonce,
    key_id     = @key_id
where scope = @scope and owner = @owner and name = @name;
