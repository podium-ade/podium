-- A chat the assistant opened from another conversation: "starting a triage on this session,
-- click here to join". started_from_chat is the conversation it was opened from, so the new
-- chat can say where it came from and link back; it is cleared, not cascaded, when that
-- chat's row goes. started_by_turn is the turn that opened it, which is what bounds how many
-- chats one turn may open (conductor/startchat.go) on every conductor at once.
alter table chats add column if not exists started_from_chat text references chats(id) on delete set null;
alter table chats add column if not exists started_by_turn text;

create index if not exists chats_started_by_turn_idx on chats (started_by_turn)
  where started_by_turn is not null;
