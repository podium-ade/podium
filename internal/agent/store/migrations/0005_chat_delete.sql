-- Deleting a chat has to take its messages with it. The FK was added without CASCADE
-- because nothing deleted a chat yet; this is that delete.
alter table chat_messages
  drop constraint chat_messages_chat_id_fkey,
  add constraint chat_messages_chat_id_fkey
    foreign key (chat_id) references chats(id) on delete cascade;
