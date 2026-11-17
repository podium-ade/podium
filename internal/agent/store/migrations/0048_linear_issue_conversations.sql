-- A Linear issue is one conversation, however it was started (docs/agent.md, "Linear"): every
-- agent session on it — the delegation, each @mention — and every routine run about it is a turn
-- of one session and one chat, keyed linear:<issue id> as a pull request is github:owner/repo#N.
-- A project is one too, linear:project:<project id>. Before this a Linear agent session was a
-- conversation of its own (linear:session:<agent session id>), and a routine with a chat per
-- ticket opened a chat of its own for each ticket (0047).
--
-- What this leaves of the old rows, none of which existed outside development: a chat keyed
-- linear:session:<id> stays as it was, readable by the people in it, and is no longer written to
-- or listed in the Linear view; the next prompt on its issue opens the issue's conversation. A
-- routine's chat per ticket (routine_subject_chats, dropped here) stays an ordinary chat of its
-- routine's; the next event about that ticket runs in the ticket's conversation instead.

-- A routine on Linear events has no chat of its own until it needs one: one that runs in each
-- ticket's conversation never does, and one that batches events into its own chat gets it on its
-- first run. A routine on a schedule still gets its chat when it is made.
alter table routines alter column chat_id drop not null;

drop table if exists routine_subject_chats;

-- linear_issues is what the webhooks said about the issue or project a conversation is about:
-- its identifier, title, link, team and Linear state. It is written from every delivery that
-- names it — an agent session's issue, an Issue or Comment data change — so the Linear view
-- lists hundreds of conversations without asking Linear about any of them. A field a delivery
-- does not carry is left as the last one that did.
--
-- asked_at is set while the conversation waits on a person: a question was asked in it and
-- nobody has answered. asked_of is whose turn asked it, '' when it was nobody's.
create table if not exists linear_issues (
  source_key  text        primary key,
  kind        text        not null check (kind in ('issue', 'project')),
  linear_id   text        not null,
  identifier  text        not null default '',
  title       text        not null default '',
  url         text        not null default '',
  team_key    text        not null default '',
  state_name  text        not null default '',
  state_type  text        not null default '',
  asked_at    timestamptz,
  asked_of    text        not null default '',
  updated_at  timestamptz not null
);
create index if not exists linear_issues_team_idx on linear_issues (team_key);
create index if not exists linear_issues_asked_idx on linear_issues (asked_of) where asked_at is not null;

-- linear_people is who touched an issue or project, which is what lets them see its
-- conversation: who created it, was assigned it, delegated it to the app, @mentioned the app on
-- it, wrote in its agent sessions, commented on it, changed it, or owns a routine that ran in it.
--
-- A Linear account is kept as Linear named it (linear_user, "<organization>/<user>", the
-- identities key) and resolved to a Podium login when somebody reads, through identities: an
-- account linked after it touched the issue sees the issue from then on, and one never linked
-- sees nothing. A routine's owner is a login already (login). Exactly one of the two is set.
create table if not exists linear_people (
  source_key  text        not null,
  linear_user text        not null default '',
  login       text        not null default '',
  via         text        not null,
  first_seen  timestamptz not null,
  last_seen   timestamptz not null,
  primary key (source_key, linear_user, login, via),
  check ((linear_user = '') <> (login = ''))
);
create index if not exists linear_people_user_idx on linear_people (linear_user) where linear_user <> '';
create index if not exists linear_people_login_idx on linear_people (login) where login <> '';

-- linear_touched is whether viewer touched the issue or project a chat is the conversation of,
-- through a linked Linear account or as a routine's owner.
create or replace function linear_touched(chat_key text, viewer text) returns boolean
language sql stable as $$
  select chat_key like 'linear:%' and exists (
    select 1 from linear_people lp
     where lp.source_key = chat_key
       and (
         (lp.login <> '' and lp.login = viewer)
         or (lp.linear_user <> '' and exists (
               select 1 from identities i
                where i.provider = 'linear' and i.external_id = lp.linear_user and i.login = viewer))
       )
  )
$$;

-- chat_role (0036) gains one grant: somebody who touched a Linear issue reads its conversation.
-- Only reads: the people who spoke in it from Linear are its participants, and editors, as
-- before.
create or replace function chat_role(chat text, viewer text) returns text
language sql stable as $$
  select case
    when c.deleted_at is not null then ''
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
        union all
        select 'viewer' where linear_touched(c.source_key, viewer)
      ) grants
      order by case r when 'editor' then 0 else 1 end
      limit 1
    ), '')
  end
  from chats c where c.id = chat
$$;

-- chat_visible_to (0036) likewise: a Linear issue's conversation is in the lists of those who
-- touched it.
create or replace function chat_visible_to(chat text, owner text, viewer text) returns boolean
language sql stable as $$
  select not exists (select 1 from chats d where d.id = chat and d.deleted_at is not null)
     and (
       (owner is not null and owner = viewer)
       or exists (
         select 1 from chat_participants p
          where p.chat_id = chat and p.login = viewer
       )
       or exists (
         select 1 from chat_access a
          where a.chat_id = chat and a.login = viewer
       )
       or exists (
         select 1 from chats k where k.id = chat and linear_touched(k.source_key, viewer)
       )
     )
$$;
