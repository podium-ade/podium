-- The per-session turn queue (0034). See conductor/queue.go for the protocol.

-- name: EnqueueTurn :one
insert into turn_queue (session_id, source_kind, login, author, chat_id, message_seq, event)
values (@session_id, @source_kind, @login, @author, @chat_id, @message_seq, @event)
returning *;

-- PopQueuedTurn takes the oldest queued turn of a session. SKIP LOCKED because two conductors
-- may briefly both believe they serve a session (one is about to lose its lease); the row is
-- still taken exactly once.
-- name: PopQueuedTurn :one
delete from turn_queue
where id = (
  select q.id from turn_queue q
   where q.session_id = @session_id
   order by q.id
   limit 1
   for update skip locked
)
returning *;

-- PopQueuedTurnFrom takes the oldest queued turn only when the same person asked it on the
-- same surface. A run of messages one person sent while their turn was running is one turn,
-- as it always was: the latest is the instruction and the rest are in the transcript.
-- name: PopQueuedTurnFrom :one
delete from turn_queue t
where t.id = (
  select q.id from turn_queue q
   where q.session_id = @session_id
   order by q.id
   limit 1
   for update skip locked
)
  and t.login = @login and t.source_kind = @source_kind
returning *;

-- name: CountQueuedTurns :one
select count(*)::int from turn_queue where session_id = @session_id;

-- ListQueuedTurnsForChat is what the chat's queue frame and the API show: the turns waiting
-- in the session a chat belongs to, oldest first.
-- name: ListQueuedTurnsForChat :many
select q.* from turn_queue q
  join sessions s on s.id = q.session_id
  join chats c on c.source_key = s.source_key
 where c.id = @chat_id
 order by q.id;

-- RemoveQueuedTurn takes back one's own queued turn. Zero rows: not queued any more (it
-- started, or was never queued), or not the caller's.
-- name: RemoveQueuedTurn :execrows
delete from turn_queue q
 using sessions s, chats c
 where q.session_id = s.id and c.source_key = s.source_key
   and c.id = @chat_id and q.message_seq = @message_seq and q.login = @login;

-- RemoveQueuedTurnByID takes back one's own queued turn by its row, which is how a message
-- that waits unrecorded (it has no seq yet) is named. Zero rows: not queued any more, not in
-- this chat, or not the caller's.
-- name: RemoveQueuedTurnByID :execrows
delete from turn_queue q
 where q.id = @id and q.chat_id = @chat_id and q.login = @login;

-- OrphanedQueues is every session with queued turns and no live conductor but the caller
-- serving it: the one that held it died between taking a turn and serving the rest, or it is
-- the caller itself, restarted. adopt takes them; a session the caller is already serving is
-- a no-op for it.
-- name: OrphanedQueues :many
select distinct q.session_id from turn_queue q
 where not exists (
   select 1 from work_leases l
    where l.key = 'session:' || q.session_id and l.expires_at >= now() and l.owner <> @owner
 );

-- OwnerWarm is the live owner's warm task for a session, and whose it is, without leaving
-- anything on the lease. No row: nobody else serves it.
-- name: OwnerWarm :one
select warm_task_id, warm_login, warm_kind from work_leases
where key = @key and owner <> @caller and expires_at >= now();

-- QueuePosition is how many turns run before a queued one: the rows queued ahead of it in its
-- session, and the turn running there, if one is — whichever conductor runs it.
-- name: QueuePosition :one
select (
  (select count(*) from turn_queue q where q.session_id = @session_id and q.id < @id)
  + (case when exists (
      select 1 from turns t where t.session_id = @session_id and t.status = 'running'
    ) then 1 else 0 end)
)::int as ahead;
