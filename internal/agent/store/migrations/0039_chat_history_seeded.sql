-- Whether a mirrored chat's record was seeded with what its conversation said before Podium
-- was in it. A Slack thread has been since phase 2; a pull request is from this migration on.
-- A pull request chat opened before it (false) still has a record that starts where Podium
-- came in, so its turns keep reading GitHub's earlier history in front of the record; one
-- opened after it (true) has that history in the record and reads nothing again.
alter table chats add column if not exists history_seeded boolean not null default false;
