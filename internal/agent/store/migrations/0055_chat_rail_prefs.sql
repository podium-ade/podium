-- chat_rail_prefs is one person's say over whether a chat is in their chat rail, apart from
-- whether it would be anyway (docs/agent.md, "The chat rail"). One row per chat and person, one
-- of two preferences, the latest choice replacing the one before:
--
--   * watched: in their rail even though they never spoke in it, own it or were shared it — a
--     routine's chat an admin reads, a Linear issue's conversation, somebody's chat they may read.
--     Watching grants nothing: the rail lists a watched chat only while they may still read it,
--     and the API refuses a watch on a chat they cannot.
--   * hidden: a Linear ticket's conversation they took out of their rail (0049's
--     chat_rail_hidden). It stays out until something new is said in it after set_at.
--
-- No row is the default: in the rail when they'd see it there anyway. Watching a hidden chat
-- un-hides it and hiding a watched one unwatches it, because a person has one answer to "do I
-- want this in my rail", not two that can disagree.
create table if not exists chat_rail_prefs (
  chat_id text        not null references chats (id) on delete cascade,
  login   text        not null,
  pref    text        not null check (pref in ('watched', 'hidden')),
  set_at  timestamptz not null,
  primary key (chat_id, login)
);
create index if not exists chat_rail_prefs_login_idx on chat_rail_prefs (login) where pref = 'watched';

insert into chat_rail_prefs (chat_id, login, pref, set_at)
select chat_id, login, 'hidden', hidden_at from chat_rail_hidden
on conflict (chat_id, login) do nothing;

drop table if exists chat_rail_hidden;
