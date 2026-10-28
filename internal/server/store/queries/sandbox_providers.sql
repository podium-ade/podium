-- name: UpsertSandboxProvider :one
insert into sandbox_providers (name, settings, max_concurrent, ciphertext, nonce, key_id, created_by, updated_at)
values (@name, @settings, @max_concurrent, @ciphertext, @nonce, @key_id, @created_by, now())
on conflict (name) do update
  set settings       = excluded.settings,
      max_concurrent = excluded.max_concurrent,
      ciphertext     = excluded.ciphertext,
      nonce          = excluded.nonce,
      key_id         = excluded.key_id,
      created_by     = excluded.created_by,
      updated_at     = now()
returning *;

-- name: GetSandboxProvider :one
select * from sandbox_providers where name = @name;

-- name: ListSandboxProviders :many
select * from sandbox_providers order by name;

-- name: DeleteSandboxProvider :execrows
delete from sandbox_providers where name = @name;

-- name: ListSandboxProvidersForUpdate :many
select * from sandbox_providers order by name for update;

-- name: ReEncryptSandboxProvider :execrows
update sandbox_providers
set ciphertext = @ciphertext,
    nonce      = @nonce,
    key_id     = @key_id
where name = @name;
