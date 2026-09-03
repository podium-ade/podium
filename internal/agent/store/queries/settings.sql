-- name: PutSetting :exec
insert into settings (key, value, updated_at) values (@key, @value, @updated_at)
on conflict (key) do update set value = excluded.value, updated_at = excluded.updated_at;

-- name: GetSetting :one
select value from settings where key = @key;

-- name: DeleteSetting :exec
delete from settings where key = @key;
