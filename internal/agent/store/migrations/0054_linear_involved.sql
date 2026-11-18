-- A person's Issues list is the issues they are involved in (docs/agent.md, "The Issues view"),
-- not every issue they may read. Who may read a conversation is unchanged: chat_role,
-- chat_visible_to and linear_touched are as 0048 and 0049 left them.
--
-- Involved is any of:
--   * assigned it, or created it, in Linear;
--   * asked Podium about it: delegated it to the app, @mentioned the app on it, or prompted it in
--     a Linear agent session (linear_people via delegated, mentioned, prompted);
--   * spoke in its conversation (a chat_participants row; a backfilled one is a guess, and does
--     not count);
--   * was @mentioned in its description or a comment (via person_mentioned, recorded from this
--     migration on).
-- Commenting on it, changing it, owning a routine that ran in it, or being shared it still lets a
-- person open the conversation, and does not put it in their list.
--
-- A person_mentioned row is a linear_people row like the others, so linear_touched counts it: a
-- person @mentioned on an issue reads its conversation, as one who commented on it does.
--
-- Accounts resolve to logins through identities when somebody reads, as linear_touched does: an
-- account linked later is involved from then on, and one never linked involves nobody.
create or replace function linear_involved(chat_key text, chat text, viewer text) returns boolean
language sql stable as $$
  select chat_key like 'linear:%' and (
    exists (
      select 1 from linear_people lp
       where lp.source_key = chat_key
         and lp.via in ('assigned', 'created', 'delegated', 'mentioned', 'prompted', 'person_mentioned')
         and (
           (lp.login <> '' and lp.login = viewer)
           or (lp.linear_user <> '' and exists (
                 select 1 from identities i
                  where i.provider = 'linear' and i.external_id = lp.linear_user and i.login = viewer))
         )
    )
    or exists (
      select 1 from chat_participants p
       where p.chat_id = chat and p.login = viewer and p.joined_via <> 'backfill'
    )
  )
$$;
