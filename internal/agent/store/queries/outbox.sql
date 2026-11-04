-- The delivery outbox (0033). Every query here is keyed so two conductors sharing the
-- database never deliver the same row at once: a row is only ever worked by whoever claimed
-- it, and a claim moves next_at past the claim lease so a second claimer skips it.

-- name: InsertOutbox :one
insert into chat_outbox (id, chat_id, message_seq, surface_kind, surface_ref, payload,
                         status, attempts, next_at, created_at, updated_at)
values (@id, @chat_id, @message_seq, @surface_kind, @surface_ref, @payload,
        'pending', 0, @next_at, @now, @now)
returning *;

-- ClaimOutbox takes up to page_limit due rows for this caller. The database's clock decides
-- what is due, so conductors with skewed clocks agree. FOR UPDATE SKIP LOCKED is what
-- lets two conductors run this at the same instant and take disjoint rows; pushing next_at
-- out by the lease is what stops the SAME row being claimed again while its attempt is still
-- in flight, after the claiming transaction (this one statement) has committed.
-- name: ClaimOutbox :many
update chat_outbox o
   set attempts = o.attempts + 1,
       next_at = now() + make_interval(secs => @lease_seconds::double precision),
       updated_at = now()
 where o.id in (
   select id from chat_outbox
    where status = 'pending' and next_at <= now()
    order by next_at
    limit @page_limit::int
    for update skip locked
 )
returning o.*;

-- BeginOutboxAttempt is the inline first attempt's claim: the row was just inserted by this
-- caller, and counting the attempt here keeps attempts honest for the backoff that follows.
-- name: BeginOutboxAttempt :execrows
update chat_outbox
   set attempts = attempts + 1, updated_at = @now
 where id = @id and status = 'pending';

-- name: MarkOutboxSent :exec
update chat_outbox
   set status = 'sent', external_id = nullif(@external_id::text, ''), last_error = '',
       updated_at = @now
 where id = @id;

-- MarkOutboxRetry records a failed attempt. give_up is decided by the caller from attempts,
-- so the schedule lives in Go where it is tested, and the row only stores the outcome.
-- name: MarkOutboxRetry :exec
update chat_outbox
   set status = case when @give_up::bool then 'failed' else 'pending' end,
       next_at = @next_at, last_error = @last_error, updated_at = @now
 where id = @id;

-- RetryOutbox puts a message's failed deliveries back in the queue, due now, with a fresh
-- attempt budget. A row that is pending or sent is left alone: retrying something on its way
-- or already there would post it twice.
-- name: RetryOutbox :execrows
update chat_outbox
   set status = 'pending', attempts = 0, next_at = @now, last_error = '', updated_at = @now
 where chat_id = @chat_id and message_seq = @message_seq and status = 'failed';

-- ListChatOutbox is every delivery a chat has, for the message list to say which answers did
-- not reach their surface. A chat with no external surface has no rows, and this is one
-- indexed read per page of messages, not one per message.
-- name: ListChatOutbox :many
select * from chat_outbox where chat_id = @chat_id order by message_seq, created_at;

-- name: GetOutbox :one
select * from chat_outbox where id = @id;

-- SetChatMessageExternalID records where the bot's own answer landed on its first surface,
-- once. A pull request that also fans out to Slack threads has one external id per surface;
-- the message keeps the first (the surface the turn started on) and the outbox rows keep all
-- of them. ON CONFLICT is impossible here because the row already exists, so the guard is
-- the null check, and a duplicate id on another bot row in the chat is skipped by the
-- partial unique index refusing it — which the caller logs and ignores.
-- name: SetChatMessageExternalID :execrows
update chat_messages set external_id = @external_id
 where chat_id = @chat_id and seq = @seq and external_id is null;
