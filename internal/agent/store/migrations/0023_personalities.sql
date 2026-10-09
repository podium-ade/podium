-- A personality is a voice one person added on top of Podium. It is not a second
-- assistant: it has no model, no skills, and no computer. login is the only person
-- who can see the row. name is unique per login, and "podium" is refused in the
-- store so a personal voice cannot take the built-in assistant's name.
create table personalities (
  id text primary key,
  login text not null,
  name text not null,
  display_name text not null,
  instructions text not null,
  updated_at timestamptz not null,
  unique (login, name)
);

-- A chat with a null personality_id talks to Podium. There is no foreign key:
-- deleting the personality leaves the chat readable, labeled by the display name
-- snapshotted at create. Slack mirrors stay null and keep the empty default.
alter table chats add column personality_id text;
alter table chats add column personality_display text not null default '';
