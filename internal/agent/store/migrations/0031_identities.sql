-- One person, many accounts. A Podium login (a Google Workspace email) is the identity; a
-- Slack user and a GitHub user are accounts that have been PROVEN to be that person. A row
-- here is what lets a Slack thread or a pull request be attributed to somebody who can sign
-- in to Podium, and it is the only thing that does: a display name, a GitHub username or a
-- public profile email is never matched on, because anybody can type any of them.
--
--   web     external_id is the login itself. Written the first time podium-server proxies a
--           call for that login, so a web row is what "this person is in Podium" means.
--   github  external_id is the numeric GitHub user id from the OAuth connect flow. A
--           username can be renamed and reused; the id cannot.
--   slack   external_id is "<team_id>/<user_id>". A Slack user id is only unique within a
--           workspace, so the team is part of the key and the same U… in a Slack Connect
--           partner's workspace can never collide with ours.
--
-- The provider set is closed in code and small, so it is checked here: a typo would
-- otherwise be an identity nothing ever resolves.
create table if not exists identities (
  login        text        not null,
  provider     text        not null check (provider in ('web', 'slack', 'github')),
  external_id  text        not null,
  handle       text        not null default '',
  display_name text        not null default '',
  avatar_url   text        not null default '',
  verified_at  timestamptz not null,
  primary key (provider, external_id)
);

create index if not exists identities_login_idx on identities (login);

-- The Podium people who have taken part in a conversation. Speaking makes you one, from any
-- surface; only resolved logins are stored, so somebody outside Podium is a message author
-- and never a participant. A participant can read the conversation: that is how a Slack
-- thread or a pull request becomes visible to the people who were in it.
--
-- joined_via is where they were first seen: web, slack, github, or backfill for a row this
-- migration guessed from a display name. A backfilled row is a guess, and phase 5's roles
-- treat it as read-only for that reason.
create table if not exists chat_participants (
  chat_id    text        not null references chats (id) on delete cascade,
  login      text        not null,
  joined_via text        not null,
  first_seen timestamptz not null,
  last_seen  timestamptz not null,
  primary key (chat_id, login)
);

create index if not exists chat_participants_login_idx on chat_participants (login);

-- The list's owner branch filters on login; it had no index while it was the only branch.
create index if not exists chats_login_idx on chats (login);

-- chat_visible_to is THE visibility rule, written once so that every read agrees: the chat
-- list, a chat's stream, the sessions list and a session's turns all call it. The owner,
-- or anybody who has taken part. The dev token's see-everything branch stays in each query,
-- because it is a property of the caller and not of the chat.
--
-- The chat's own login is passed in rather than re-read: every caller already has the row.
create or replace function chat_visible_to(chat text, owner text, viewer text) returns boolean
language sql stable as $$
  select (owner is not null and owner = viewer)
      or exists (
        select 1 from chat_participants p
         where p.chat_id = chat and p.login = viewer
      )
$$;

-- Backfill. Everything below is idempotent, and every guess is conservative: an identity is
-- written only from something that was already proof, and a mirrored chat gains a
-- participant only where a GitHub handle that was proven by OAuth matches its author.

-- A connected GitHub account is OAuth proof of the numeric id.
insert into identities (login, provider, external_id, handle, display_name, verified_at)
select login, 'github', github_id::text, github_login, name, connected_at
  from github_accounts
on conflict (provider, external_id) do nothing;

-- Whoever made a web chat signed in through podium-server to do it.
insert into identities (login, provider, external_id, verified_at)
select login, 'web', login, min(created_at)
  from chats
 where login is not null and login <> '' and origin = 'web'
 group by login
on conflict (provider, external_id) do nothing;

-- A web chat's owner is its first participant.
insert into chat_participants (chat_id, login, joined_via, first_seen, last_seen)
select id, login, 'web', created_at, created_at
  from chats
 where login is not null and login <> '' and origin = 'web'
on conflict (chat_id, login) do nothing;

-- A mirrored pull request whose authors include a connected GitHub handle. Slack threads
-- cannot be backfilled: no Slack account has been proven yet, and a display name is not
-- proof. They appear to their people the next time one of them speaks.
insert into chat_participants (chat_id, login, joined_via, first_seen, last_seen)
select c.id, i.login, 'backfill', c.created_at, c.created_at
  from chats c
  join identities i
    on i.provider = 'github'
   and i.handle <> ''
   and (
     lower(i.handle) = lower(c.started_by)
     or exists (
       select 1 from chat_messages m
        where m.chat_id = c.id and m.role = 'user' and lower(m.author) = lower(i.handle)
     )
   )
 where c.origin = 'github'
on conflict (chat_id, login) do nothing;
