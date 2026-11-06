-- Logins are compared as podium-server spells them: that is who the proxy says is calling,
-- what chats.login and personal secrets are keyed on, and what a web identity row records.
-- A login somebody TYPES — a share target, a person @-mentioned — may not match that case
-- ("Dana@acme.com" for dana@acme.com), so it is resolved to the canonical spelling through
-- the web identities, case-insensitively (CanonicalWebLogin), before it is written or
-- compared. This index is that lookup's.
create index if not exists identities_web_login_ci_idx
  on identities (lower(external_id)) where provider = 'web';

-- A mention is recorded as the composer wrote it. The notification goes to the person it
-- names, in their canonical spelling, so chat_role and the inbox both find them.
create or replace function notify_mentions() returns trigger
language plpgsql as $$
begin
  if new.mentions is null or cardinality(new.mentions) = 0 then
    return new;
  end if;
  insert into notifications (login, kind, chat_id, seq, actor)
  select distinct who, 'mention', new.chat_id, new.seq, coalesce(new.author_login, new.author, '')
    from (
      select coalesce(
               (select i.login from identities i
                 where i.provider = 'web' and lower(i.external_id) = lower(m)
                 order by (i.external_id = m) desc
                 limit 1),
               m) as who
        from unnest(new.mentions) as m
       where m <> ''
    ) named
   where who is distinct from new.author_login
     and chat_role(new.chat_id, who) <> '';
  return new;
end
$$;
