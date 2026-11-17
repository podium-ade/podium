-- A mirrored chat its owner deleted stays deleted.
--
-- Deleting a web chat removes it: Podium is the only place that conversation lives. A mirrored
-- chat is Podium's record of a conversation that lives in Slack or on a pull request, and the
-- next message there would simply open a new record of it — the owner deleted it and it came
-- back. So a mirrored chat is not removed but tombstoned: deleted_at is set, and from then on
-- nobody's role in it is anything (chat_role is '') and it is in nobody's lists
-- (chat_visible_to is false). The dev token, which sees every chat, still sees it.
--
-- The mirror keeps recording into the tombstone. Turns from the conversation's own surface
-- still run (a Slack thread does not stop being a thread because Podium's copy was deleted)
-- and their transcript is still read from the record, so it must stay whole. Nothing revives
-- it: a Podium person speaking in the thread again becomes a participant of a chat they
-- cannot see. Deleted for everyone means everyone, the person who deleted it included.

alter table chats add column if not exists deleted_at timestamptz;

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
      ) grants
      order by case r when 'editor' then 0 else 1 end
      limit 1
    ), '')
  end
  from chats c where c.id = chat
$$;

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
     )
$$;
