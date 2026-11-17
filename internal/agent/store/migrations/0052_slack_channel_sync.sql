-- When the slack_channels catalogue was last refreshed from Slack's own list. The catalogue is
-- a mirror that the Channels screen reads without calling Slack, so it needs to say how old it
-- is. One row, enforced by the check: this is a fact about the whole catalogue, not a channel.
-- No row means the catalogue has only ever been filled by mentions.

create table slack_channel_sync (
  id           boolean     primary key default true check (id),
  refreshed_at timestamptz not null
);
