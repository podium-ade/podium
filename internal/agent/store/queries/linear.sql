-- The Linear source's poll watermark. One row, one key: the table exists so a conductor
-- that is restarted does not replay a day of tickets, and it is written only AFTER a
-- page's events have been handed over, so a crash between the two replays rather than
-- loses.
-- name: PutLinearCursor :exec
insert into linear_cursor (key, updated_at) values (@key, @updated_at)
on conflict (key) do update set updated_at = excluded.updated_at;

-- name: GetLinearCursor :one
select updated_at from linear_cursor where key = @key;
