-- The pull requests a chat's work produced, so a human reaches them without reading the
-- transcript back. A turn that opens one says so in its answer and the conductor links it;
-- a person can also attach one the turn never mentioned, and detach one.
--
-- What is stored is what the URL itself says — owner, repo, number — and nothing that would
-- need a GitHub credential to learn. The conductor has none: podium.agent.github_token is a
-- secret attached to TASKS, not something this process can read. A title or an open/merged
-- state would be a second system to keep in step and a vendor coupling for a convenience
-- feature, so the UI renders owner/repo#number from these columns alone.
--
-- (chat_id, url) is the primary key, so a turn naming its pull request three times links it
-- once. The url stored is canonical — https://github.com/<owner>/<repo>/pull/<number> —
-- which is what makes /pull/12/files and /pull/12#discussion the same link.
--
-- detached_at is a tombstone rather than a delete because a detach has to survive the next
-- turn. A human who removes a link and then asks a follow-up in the same chat would
-- otherwise get it back the moment the next answer mentions the same pull request again.
-- A row with detached_at set blocks the automatic re-link and is invisible to every read;
-- attaching the same URL by hand clears it, because a person undoing their own removal is
-- the one case where it should come back.
--
-- ON DELETE CASCADE follows 0005: deleting a chat takes everything that only made sense as
-- part of that conversation with it.

create table if not exists chat_pull_requests (
  chat_id     text        not null references chats(id) on delete cascade,
  url         text        not null,
  owner       text        not null,
  repo        text        not null,
  number      int         not null,
  -- source is 'turn' when a turn's own answer named it and 'human' when a person attached
  -- it. A reviewer looking at a chat's links wants to know which of the two they are.
  source      text        not null check (source in ('turn', 'human')),
  created_at  timestamptz not null,
  detached_at timestamptz,
  primary key (chat_id, url)
);
