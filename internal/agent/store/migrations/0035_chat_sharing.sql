-- Phase 5 of docs/agent.md: a chat is shared the way a document is.
--
-- Three things decide what a login may do with a chat, in this order:
--
--   1. chats.login is the OWNER. There is exactly one, it is never a chat_access row, and
--      ownership is not transferable in this phase. Keeping it on the chat row (where every
--      query since 0001 already reads it) means there is no second copy to disagree with.
--   2. chat_access is everybody the owner shared it with by name, everybody who joined
--      through the link, and everybody who opened a members-shared chat — each with a role.
--   3. chat_participants (0031) is everybody who has spoken. Speaking makes you an EDITOR:
--      you are already in the conversation, so refusing you the composer would be absurd.
--      The one exception is a participant the 0031 backfill GUESSED from a display name
--      (joined_via = 'backfill'): a name match is not proof, so it only grants VIEWER until
--      that person speaks again and is resolved by id.
--
-- general_access widens the chat beyond those rows:
--   restricted  nobody else (the default, and what every chat before this migration is)
--   members     every Podium login may open it by id, listed under "Workspace"; opening it
--               writes a chat_access row with general_role, so it then stays in their lists
--   link        anybody signed in who presents link_token may open it, and is written a
--               chat_access row the same way
alter table chats add column if not exists general_access text not null default 'restricted'
  check (general_access in ('restricted', 'members', 'link'));
alter table chats add column if not exists general_role text not null default 'viewer'
  check (general_role in ('viewer', 'editor'));
-- link_token is 128 random bits, hex. Null until the owner first turns link sharing on, and
-- replaced (never reused) when they reset it. The comparison is the API's, in constant time:
-- a token is never a query predicate, so its value cannot leak through timing of an index.
alter table chats add column if not exists link_token text;

-- granted_by is the login who shared it, or 'link' / 'members' for a row written by opening
-- the chat that way. It is what the "Shared with me" section says next to the chat.
create table if not exists chat_access (
  chat_id    text        not null references chats (id) on delete cascade,
  login      text        not null,
  role       text        not null check (role in ('editor', 'viewer')),
  granted_by text        not null,
  granted_at timestamptz not null,
  primary key (chat_id, login)
);
create index if not exists chat_access_login_idx on chat_access (login);

-- chat_reads is how far each person has read each chat. Unread is every message after it that
-- somebody ELSE said (a person, or Podium) — never your own words, never chrome.
create table if not exists chat_reads (
  chat_id       text        not null references chats (id) on delete cascade,
  login         text        not null,
  last_read_seq bigint      not null,
  updated_at    timestamptz not null,
  primary key (chat_id, login)
);
create index if not exists chat_reads_login_idx on chat_reads (login);

-- notifications is the in-app inbox: "Dana shared X with you" and "Dana mentioned you in X".
-- A table rather than something derived, because both events are moments: a share that is
-- later revoked was still a share somebody was told about, and a mention is one message, not
-- a state. read_at is null until the UI marks it read.
create table if not exists notifications (
  id         bigserial   primary key,
  login      text        not null,
  kind       text        not null check (kind in ('share', 'mention')),
  chat_id    text        not null references chats (id) on delete cascade,
  seq        bigint      not null default 0,
  actor      text        not null default '',
  created_at timestamptz not null default now(),
  read_at    timestamptz
);
create index if not exists notifications_login_unread_idx on notifications (login, id) where read_at is null;
-- One share notification per (person, chat): sharing again, or changing a role, is not news.
create unique index if not exists notifications_share_once_idx on notifications (login, chat_id) where kind = 'share';

-- chat_role is THE permission rule: '' (none), 'viewer', 'editor' or 'owner'. Every write
-- check reads it, so "may this person do this" has one answer everywhere. The strongest of
-- the grants wins: an access row saying viewer does not demote somebody who has spoken.
--
-- members access only counts for a login with a web identity: somebody who has signed in to
-- Podium. Every proxied caller has one (api/webidentity.go records it on the first call), so
-- this is the same as "a Podium member" without asking the control plane.
create or replace function chat_role(chat text, viewer text) returns text
language sql stable as $$
  select case
    when c.login is not null and c.login = viewer then 'owner'
    else coalesce((
      select r from (
        select a.role as r from chat_access a where a.chat_id = c.id and a.login = viewer
        union all
        select case when p.joined_via = 'backfill' then 'viewer' else 'editor' end
          from chat_participants p where p.chat_id = c.id and p.login = viewer
        union all
        select c.general_role
         where c.general_access = 'members'
           and exists (select 1 from identities i where i.provider = 'web' and i.external_id = viewer)
      ) grants
      order by case r when 'editor' then 0 else 1 end
      limit 1
    ), '')
  end
  from chats c where c.id = chat
$$;

-- chat_visible_to (0031) is "in your own lists": owner, participant, and now an access row.
-- members access is deliberately NOT here — a chat shared with everybody would otherwise be
-- in everybody's list. It is listed under Workspace instead, until opened.
create or replace function chat_visible_to(chat text, owner text, viewer text) returns boolean
language sql stable as $$
  select (owner is not null and owner = viewer)
      or exists (
        select 1 from chat_participants p
         where p.chat_id = chat and p.login = viewer
      )
      or exists (
        select 1 from chat_access a
         where a.chat_id = chat and a.login = viewer
      )
$$;

-- chat_unread counts what viewer has not read in a chat: messages after their read mark that
-- somebody else said. A chat they have never opened counts from the beginning.
create or replace function chat_unread(chat text, viewer text) returns bigint
language sql stable as $$
  select count(*)
    from chat_messages m
   where m.chat_id = chat
     and m.role in ('user', 'assistant')
     and m.deleted_at is null
     and m.author_login is distinct from viewer
     and m.seq > coalesce(
       (select r.last_read_seq from chat_reads r where r.chat_id = chat and r.login = viewer), 0)
$$;

-- A mention notifies the person named, when they can open the chat. Done by a trigger so that
-- every writer of chat_messages — the web chat, a queued turn, a future surface — notifies the
-- same way, and nobody can forget to. A person cannot notify themselves, and a mention of
-- somebody who has no access grants nothing: it is not a back door into a chat.
create or replace function notify_mentions() returns trigger
language plpgsql as $$
begin
  if new.mentions is null or cardinality(new.mentions) = 0 then
    return new;
  end if;
  insert into notifications (login, kind, chat_id, seq, actor)
  select distinct m, 'mention', new.chat_id, new.seq, coalesce(new.author_login, new.author, '')
    from unnest(new.mentions) as m
   where m <> '' and m is distinct from new.author_login
     and chat_role(new.chat_id, m) <> '';
  return new;
end
$$;
drop trigger if exists chat_messages_notify_mentions on chat_messages;
create trigger chat_messages_notify_mentions
  after insert on chat_messages
  for each row execute function notify_mentions();

-- Everybody who can already see a chat starts with it read, so the first deploy does not light
-- up every conversation anybody ever had as unread.
insert into chat_reads (chat_id, login, last_read_seq, updated_at)
select c.id, c.login, c.last_seq, now()
  from chats c
 where c.login is not null and c.login <> ''
on conflict (chat_id, login) do nothing;
insert into chat_reads (chat_id, login, last_read_seq, updated_at)
select p.chat_id, p.login, c.last_seq, now()
  from chat_participants p join chats c on c.id = p.chat_id
on conflict (chat_id, login) do nothing;
