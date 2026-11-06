-- Whether the Slack conversation a mirrored chat records is public, so the share dialog can
-- warn about sharing a private one with what Slack itself said, rather than guessing from the
-- channel id (a C… channel may be either). One of 'public', 'private' (a private channel),
-- 'im' (a direct message) or 'mpim' (a group DM); '' is not known yet — a chat recorded before
-- this, or a Slack that did not answer conversations.info — and the dialog falls back to the
-- id. Deliberately unconstrained, like origin: the values are the Slack source's own.
alter table chats add column if not exists channel_privacy text not null default '';
