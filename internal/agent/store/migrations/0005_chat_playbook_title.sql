-- A web chat remembers the playbook it started with — the same one-session-one-playbook
-- rule a Slack thread follows — and may have its title rewritten from the first query.
--
-- playbook is empty until the first message; then it is fixed. auto_title is true for a
-- chat created untitled ("New chat"), which Podium may rename. A title the caller supplied
-- at create is theirs and is never overwritten.

alter table chats add column if not exists playbook text not null default '';
alter table chats add column if not exists auto_title boolean not null default true;
