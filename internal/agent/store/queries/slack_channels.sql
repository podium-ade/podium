-- name: ListSlackChannels :many
select id, name, description, updated_at
from slack_channels
order by name, id;

-- name: GetSlackChannel :one
select id, name, description, updated_at
from slack_channels
where id = @id;

-- UpsertSlackChannelName records the name Slack last gave this channel. The description is
-- not in the set list: a mention refreshing a name must not wipe an operator's note.
-- An empty name on conflict is ignored so a failed conversations.info cannot blank a name
-- we already had.
-- name: UpsertSlackChannelName :exec
insert into slack_channels (id, name, description, updated_at)
values (@id, @name, '', @updated_at)
on conflict (id) do update
  set name = excluded.name,
      updated_at = excluded.updated_at
 where excluded.name <> ''
   and slack_channels.name is distinct from excluded.name;

-- name: SetSlackChannelDescription :execrows
update slack_channels
set description = @description,
    updated_at = @updated_at
where id = @id;

-- name: SetChatChannel :exec
update chats set channel = @channel where id = @id;
