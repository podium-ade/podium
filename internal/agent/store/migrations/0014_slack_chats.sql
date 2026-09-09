-- A Slack thread is mirrored into chats/chat_messages so it can be READ in the Podium UI
-- beside the web chats. The mirror is display-only and never read back into a brief: Slack
-- still holds the conversation, and FetchTranscript is still what a turn is handed, so an
-- edited or deleted message cannot leave the two disagreeing about what the model saw.

-- author is who said it. Empty for a web chat, where the chat's login is the only human;
-- a Slack display name for a mirrored thread, which has as many people in it as care to
-- join. The UI reads the distinct authors to list a conversation's participants.
alter table chat_messages add column if not exists author text not null default '';

-- source_key links a chat to the conductor session that owns it, which is how the list
-- knows whether a turn is running. A web chat's is 'chat:'||id — written for every row so
-- the column is the link for both kinds and ListChats needs no string concatenation.
alter table chats add column if not exists source_key text;
update chats set source_key = 'chat:' || id where source_key is null;
create unique index if not exists chats_source_key_idx on chats (source_key);

-- started_by is the person who asked first. It is a mirrored thread's attribution, because
-- login cannot be: login is a Podium identity and nobody in Slack has one.
alter table chats add column if not exists started_by text not null default '';

-- origin says where the conversation actually lives, and therefore whether the UI may write
-- to it. A mirrored thread is read-only here; you reply to it in Slack.
-- Deliberately unconstrained: it is written from a source's own Kind(), which is a closed
-- set in code, and a CHECK here would have to be migrated every time a source is added.
-- CreateMirrorChat is the guard that matters — it refuses 'web' and it refuses empty.
alter table chats add column if not exists origin text not null default 'web';

-- login stops being mandatory. A Slack thread belongs to the workspace rather than to a
-- login, so its login is null: visible to every login, and — because RenameChat and
-- DeleteChat both filter on `login = @login`, which no null ever matches — writable by
-- none of them without either query changing.
alter table chats alter column login drop not null;
