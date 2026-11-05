-- UpsertIdentity records that an account is a login. A later proof of the same account for a
-- different login (a GitHub account disconnected by one person and connected by another)
-- moves it: the newest proof wins, because it is the one somebody just completed.
-- name: UpsertIdentity :exec
insert into identities (login, provider, external_id, handle, display_name, avatar_url, verified_at)
values (@login, @provider, @external_id, @handle, @display_name, @avatar_url, @verified_at)
on conflict (provider, external_id) do update
  set login = excluded.login,
      handle = excluded.handle,
      display_name = excluded.display_name,
      avatar_url = excluded.avatar_url,
      verified_at = excluded.verified_at;

-- name: IdentityLogin :one
select login from identities where provider = @provider and external_id = @external_id;

-- HasWebIdentity is "this login has signed in to Podium". Only a web row says so.
-- name: HasWebIdentity :one
select exists (
  select 1 from identities where provider = 'web' and external_id = @login
)::bool as present;

-- DeleteIdentitiesOf removes every account of one provider that is this login's — the
-- GitHub disconnect, which does not know the numeric id any more.
-- name: DeleteIdentitiesOf :exec
delete from identities where provider = @provider and login = @login;

-- name: IdentitiesOfLogins :many
select * from identities where login = any(@logins::text[]) order by login, provider;

-- AddChatParticipant records that login took part. A repeat only moves last_seen: where
-- somebody was FIRST seen is the useful fact, and a backfilled guess becomes a real row the
-- moment the person is actually seen.
-- name: AddChatParticipant :one
insert into chat_participants (chat_id, login, joined_via, first_seen, last_seen)
values (@chat_id, @login, @joined_via, @seen_at, @seen_at)
on conflict (chat_id, login) do update
  set last_seen = greatest(chat_participants.last_seen, excluded.last_seen),
      joined_via = case when chat_participants.joined_via = 'backfill'
                        then excluded.joined_via else chat_participants.joined_via end
returning (xmax = 0)::bool as inserted;

-- ClaimChatOwner gives an unowned (mirrored) chat its first Podium participant as owner. The
-- `login is null` guard is the whole race: two conductors resolving two speakers at once
-- both try, and exactly one row update wins.
-- name: ClaimChatOwner :execrows
update chats set login = @login where id = @id and login is null;

-- name: ListChatParticipants :many
select * from chat_participants where chat_id = @chat_id order by first_seen, login;

-- name: ChatVisibleTo :one
select chat_visible_to(c.id, c.login, @viewer::text)::bool as visible
  from chats c where c.id = @id;

-- SearchMembers is the composer's @-mention list: every login that has signed in to Podium
-- (a web row), named and pictured by whichever of its accounts says so. query is matched
-- against the login and every display name, case-insensitively.
-- name: SearchMembers :many
select w.login,
  coalesce((select i.display_name from identities i
             where i.login = w.login and i.display_name <> ''
             order by (i.provider = 'web') desc, i.verified_at desc limit 1), '')::text as name,
  coalesce((select i.avatar_url from identities i
             where i.login = w.login and i.avatar_url <> ''
             order by i.verified_at desc limit 1), '')::text as avatar
  from identities w
 where w.provider = 'web'
   and (
     @query::text = ''
     or w.login ilike '%' || @query::text || '%'
     or exists (select 1 from identities n
                 where n.login = w.login and n.display_name ilike '%' || @query::text || '%')
   )
 order by w.login
 limit @page_limit::int;
