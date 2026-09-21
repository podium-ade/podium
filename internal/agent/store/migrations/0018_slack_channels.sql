-- Slack channels this conductor has seen, and the operator-authored note a turn of one is
-- briefed with. The id is Slack's, which does not change when a channel is renamed; name is
-- the last resolved human name, without a leading #.
--
-- description is written only by the UI. A name refresh on a mention must not wipe it.
--
-- chats.channel is the denormalised name a mirrored thread shows in the Podium chat window.
-- It is display, not routing, and empty until Slack has answered conversations.info.

create table slack_channels (
  id          text        primary key,
  name        text        not null default '',
  description text        not null default '',
  updated_at  timestamptz not null
);

alter table chats add column if not exists channel text not null default '';
