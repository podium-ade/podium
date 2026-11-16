-- A question the assistant asks in a Linear ticket's conversation lands in the inbox of the
-- person it is asked of (docs/linear-agent-plan.md, "Questions in the inbox").
--
-- The person is whose turn asked it: the person who prompted it, or a routine's owner for the
-- routine's run. detail is the question's text, and the ticket's identifier and title are read
-- from linear_issues when the inbox is listed. A question row is open while read_at is null,
-- and it is closed by the conversation hearing from somebody (the question answered in Linear,
-- in the chat or from the inbox), not by opening the chat or "mark all read".
alter table notifications drop constraint if exists notifications_kind_check;
alter table notifications add constraint notifications_kind_check
  check (kind in ('share', 'mention', 'routine', 'question'));

-- One open question per conversation and person: a second question asked before the first is
-- answered replaces its text rather than adding a row. chat_id leads, because an answer closes
-- a conversation's questions.
create unique index if not exists notifications_question_open_idx
  on notifications (chat_id, login) where kind = 'question' and read_at is null;
