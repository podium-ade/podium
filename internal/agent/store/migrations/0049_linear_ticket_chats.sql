-- A Linear ticket's conversation in a person's chat rail, and written from Podium
-- (docs/linear-agent-plan.md, "Ticket chats").
--
-- chat_role (0048) gave somebody who touched a Linear issue — through a linked Linear account,
-- or as the owner of a routine that ran in it — `viewer` on its conversation. They are the
-- issue's participants, the people its conversation is in the chat rail of, and from here they
-- write it: what they send is posted to the issue as a comment under their name, and the turn
-- it starts runs as them, exactly as an @mention of the app on the issue in Linear would. That
-- is no more than Linear already lets them do. An admin or owner who never touched it still
-- only reads it (the API's Infra grant), and so does the dev token's operator view.
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
        select 'editor' where linear_touched(c.source_key, viewer)
      ) grants
      order by case r when 'editor' then 0 else 1 end
      limit 1
    ), '')
  end
  from chats c where c.id = chat
$$;

-- chat_rail_hidden is a ticket's conversation somebody took out of their chat rail. It stays
-- out until something new is said in it after hidden_at, and is in the Linear view throughout.
-- Nothing else about the chat changes: hiding is not leaving, and their access is the issue's.
create table if not exists chat_rail_hidden (
  chat_id   text        not null references chats (id) on delete cascade,
  login     text        not null,
  hidden_at timestamptz not null,
  primary key (chat_id, login)
);
