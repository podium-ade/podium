-- task_log_index is what a task's log chunks were, apart from their bytes, written by the
-- roll-up next to the per-stream objects: each chunk's seq, stream, length, timestamp and
-- source offset, compressed (internal/server/logs/index.go). It is what lets a pruned task
-- replay with the times, the stream interleaving and the sequence numbers it had live.
-- A task rolled up before it existed has no row, and replays the old way.
--
-- It is a table of its own rather than a column of tasks so that every other read of a
-- task — the list page among them — does not carry it.
create table if not exists task_log_index (
  task_id text  primary key references tasks (id) on delete cascade,
  data    bytea not null
);
