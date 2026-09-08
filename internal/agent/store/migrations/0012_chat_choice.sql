-- What a chat was last told to answer on, so a person picks a model once rather than on
-- every message.
--
-- These are the OVERRIDE and not the resolution: empty means "whatever the assistant's own
-- model is", which is what profile.yaml says and may change under a conversation that never
-- asked for anything specific. `turns` already records the resolved triple per turn, and
-- seeding the picker from that would have pinned every chat to the model its first turn
-- happened to run — turning a default into a choice nobody made.
--
-- This is the second thing a chat row has remembered about how to answer. The first was
-- `playbook`, dropped in 0010: it recorded whichever container the conversation last happened
-- to start, which was never a decision anybody expressed. A model is — "answer me on Grok" is
-- a statement about this conversation — which is why this one is worth storing and that one
-- was not.
alter table chats add column if not exists agent  text not null default '';
alter table chats add column if not exists model  text not null default '';
alter table chats add column if not exists effort text not null default '';
