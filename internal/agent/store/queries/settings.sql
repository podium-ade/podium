-- name: PutSetting :exec
insert into settings (key, value, updated_at) values (@key, @value, @updated_at)
on conflict (key) do update set value = excluded.value, updated_at = excluded.updated_at;

-- name: GetSetting :one
select value from settings where key = @key;

-- name: DeleteSetting :exec
delete from settings where key = @key;

-- name: ListSettingsByPrefix :many
select key, value from settings where starts_with(key, @prefix::text) order by key;

-- ClaimSetting writes a setting only when it was never written: whoever writes it first has it.
-- name: ClaimSetting :execrows
insert into settings (key, value, updated_at) values (@key, @value, @updated_at)
on conflict (key) do nothing;
