-- Slack threads that asked for a GitHub pull-request review. The conversation identity is
-- always github:owner/repo#N (sessions.source_key). A Slack thread is not naturally a PR,
-- so the bind is stored here: one thread is at most one review ((kind, ref) unique), and
-- one PR may be asked from several threads (index on source_key).
--
-- kind is 'slack' today. The column is named rather than implied so a later surface is a
-- row, not a migration. ref for slack is channel/thread — the two parts of a Slack Ref
-- that name the conversation, never the triggering message.
--
-- The github source does not open this table itself: the conductor injects bind/lookup
-- the way Linear injects its session lookup, so a source still never imports the store.

create table if not exists review_surfaces (
  source_key  text        not null,
  kind        text        not null,
  ref         text        not null,
  created_at  timestamptz not null,
  primary key (kind, ref)
);

create index if not exists review_surfaces_source_key_idx on review_surfaces (source_key);
