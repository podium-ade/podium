-- A message written by a bot account on its surface — a GitHub App or a [bot] login, say
-- dependabot commenting on a pull request — is in the record like anybody's, but its author is
-- not a human, and a chat is multiplayer only once more than one human has been in it. The
-- source says so (InboundEvent.AuthorBot); ChatAuthors leaves these rows out of who is in the
-- chat.
--
-- Rows that are already here stay false: nothing recorded then said who was a bot, and a
-- chat's multiplayer flag is sticky anyway.
alter table chat_messages add column if not exists author_bot boolean not null default false;
