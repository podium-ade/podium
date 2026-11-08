-- Phase 5 of docs/agent.md: sharing a chat, reading it, and being told.
-- The rules are 0035_chat_sharing.sql's chat_role and chat_visible_to; these are the writes.

-- name: ChatRole :one
select chat_role(c.id, @viewer::text)::text as role from chats c where c.id = @id;

-- ShareChatWith is the owner sharing by name. It overwrites a previous grant's role: sharing
-- again as editor is how a viewer is promoted, and as viewer how an editor is demoted.
-- inserted is true for a first grant, which is what decides whether to notify.
-- name: ShareChatWith :one
insert into chat_access (chat_id, login, role, granted_by, granted_at)
values (@chat_id, @login, @role, @granted_by, @granted_at)
on conflict (chat_id, login) do update
  set role = excluded.role, granted_by = excluded.granted_by, granted_at = excluded.granted_at
returning (xmax = 0)::bool as inserted;

-- JoinChatAccess is opening a chat by link or through members access: the row that keeps it
-- in the opener's lists afterwards. It never overwrites — an explicit share stays what the
-- owner made it — so a link set to viewer cannot demote an editor who opens it.
-- name: JoinChatAccess :execrows
insert into chat_access (chat_id, login, role, granted_by, granted_at)
values (@chat_id, @login, @role, @granted_by, @granted_at)
on conflict (chat_id, login) do nothing;

-- name: RevokeChatAccess :execrows
delete from chat_access where chat_id = @chat_id and login = @login;

-- LeaveChat takes somebody out of a chat that is not theirs: their access row and their
-- participant row. Speaking in it again (from any surface) makes them a participant again,
-- which is the same rule that put them there in the first place.
-- name: LeaveChat :one
with a as (
  delete from chat_access ca where ca.chat_id = @chat_id::text and ca.login = @login::text returning 1
), p as (
  delete from chat_participants cp where cp.chat_id = @chat_id::text and cp.login = @login::text returning 1
)
select ((select count(*) from a) + (select count(*) from p))::bigint as removed;

-- ListChatAccess is the share dialog's "people with access": every named grant, with what
-- the person's own accounts say about their name and picture.
-- name: ListChatAccess :many
select a.chat_id, a.login, a.role, a.granted_by, a.granted_at,
  coalesce((select i.display_name from identities i
             where i.login = a.login and i.display_name <> ''
             order by (i.provider = 'web') desc, i.verified_at desc limit 1), '')::text as name,
  coalesce((select i.avatar_url from identities i
             where i.login = a.login and i.avatar_url <> ''
             order by i.verified_at desc limit 1), '')::text as avatar
  from chat_access a
 where a.chat_id = @chat_id
 order by a.granted_at, a.login;

-- name: SetChatGeneralAccess :one
update chats set general_access = @general_access, general_role = @general_role
 where id = @id
returning *;

-- name: SetChatLinkToken :one
update chats set link_token = @link_token where id = @id
returning *;

-- MarkChatRead moves a read mark forward and never back: two tabs reporting out of order
-- leave the furthest one.
-- name: MarkChatRead :exec
insert into chat_reads (chat_id, login, last_read_seq, updated_at)
values (@chat_id, @login, @seq, now())
on conflict (chat_id, login) do update
  set last_read_seq = greatest(chat_reads.last_read_seq, excluded.last_read_seq),
      updated_at = excluded.updated_at;

-- name: NotifyShare :exec
insert into notifications (login, kind, chat_id, actor)
values (@login, 'share', @chat_id, @actor)
on conflict (login, chat_id) where kind = 'share' do nothing;

-- NotifyRoutine tells a routine's owner that Podium or an admin paused it, and why.
-- name: NotifyRoutine :exec
insert into notifications (login, kind, chat_id, actor, detail)
values (@login, 'routine', @chat_id, @actor, @detail);

-- ListNotifications is a person's inbox, newest first, with the chat's title for the toast.
-- A notification for a chat they can no longer open is left out rather than deleted: access
-- can come back, and the row is harmless.
-- name: ListNotifications :many
select n.id, n.login, n.kind, n.chat_id, n.seq, n.actor, n.created_at, n.read_at,
       n.detail, c.title as chat_title
  from notifications n
  join chats c on c.id = n.chat_id
 where n.login = @login
   and (not @unread_only::bool or n.read_at is null)
   and chat_role(c.id, @login::text) <> ''
 order by n.id desc
 limit @page_limit::int;

-- MarkNotificationsRead marks some, or with all set every one, of a person's notifications.
-- name: MarkNotificationsRead :execrows
update notifications set read_at = now()
 where login = @login and read_at is null
   and (@mark_all::bool or id = any(@ids::bigint[]));

-- MemberProfile is one Podium person's name and picture, from whichever of their accounts
-- says so — the share dialog's owner row.
-- name: MemberProfile :one
select
  coalesce((select i.display_name from identities i
             where i.login = @login::text and i.display_name <> ''
             order by (i.provider = 'web') desc, i.verified_at desc limit 1), '')::text as name,
  coalesce((select i.avatar_url from identities i
             where i.login = @login::text and i.avatar_url <> ''
             order by i.verified_at desc limit 1), '')::text as avatar;
