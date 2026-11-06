-- The link to the conversation a mirrored chat records, where it lives: a Slack thread's
-- permalink. The API cannot build it — it does not know the workspace's archive URL, which is
-- the Slack source's from auth.test — so the mirror records what the source says. A pull
-- request's link is built from its key, so a GitHub chat leaves this empty. '' is not known.
alter table chats add column if not exists source_url text not null default '';
