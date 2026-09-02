# Step 03 — Postgres store and task queue

**Milestone:** M1 · **Depends on:** 02 · **Design ref:** §6.2 Schema, §6.1 `server/store`

## Goal
Implement `internal/server/store`: embedded migrations, typed queries, the task state machine, and the
`SKIP LOCKED` queue. This is the only place SQL lives.

## In scope

### Migrations (`internal/server/store/migrations/*.sql`, embedded, run on server start)
```sql
-- 0001_init.sql
create table nodes (
  id text primary key, name text not null, tags text[] not null default '{}',
  labels jsonb not null default '[]', capacity jsonb not null default '{}',
  node_key_hash bytea not null, status text not null default 'offline',
  version text, last_heartbeat_at timestamptz, created_at timestamptz not null default now()
);
create table enrollment_tokens (
  id text primary key, token_hash bytea not null unique, labels jsonb not null default '[]',
  expires_at timestamptz not null, used_at timestamptz, used_by_node_id text, created_by text not null,
  created_at timestamptz not null default now()
);
create table tasks (
  id text primary key, spec jsonb not null, status text not null default 'queued', priority int not null default 0,
  requested_by text not null, node_id text references nodes(id), lease_id text, lease_expires_at timestamptz,
  attempts int not null default 0, max_attempts int not null default 1,
  created_at timestamptz not null default now(), scheduled_at timestamptz, started_at timestamptz, finished_at timestamptz,
  exit_code int, usage jsonb, failure_reason text
);
create index tasks_queue_idx on tasks (status, priority desc, created_at) where status = 'queued';
create index tasks_node_idx on tasks (node_id) where status in ('scheduled','provisioning','running');
create table task_events (
  task_id text not null references tasks(id) on delete cascade, seq bigint not null, kind text not null,
  ts timestamptz not null, payload jsonb not null, primary key (task_id, seq)
);
create table task_log_chunks (
  task_id text not null references tasks(id) on delete cascade, seq bigint not null, stream text not null,
  sidecar text, ts timestamptz not null, bytes bytea not null, primary key (task_id, seq)
);
create table artifacts (
  id text primary key, task_id text not null references tasks(id) on delete cascade, name text not null,
  content_type text, size bigint, object_key text not null, created_at timestamptz not null default now()
);
create table secrets (
  name text primary key, ciphertext bytea not null, nonce bytea not null, version int not null default 1,
  created_by text not null, updated_at timestamptz not null default now()
);
create table users (login text primary key, display_name text, roles text[] not null default '{}', first_seen_at timestamptz not null default now());
create table audit_log (id bigserial primary key, ts timestamptz not null default now(), actor text not null, action text not null, subject text, details jsonb);
```

### Store API (`internal/server/store`)
- `New(ctx, databaseURL) (*Store, error)` using `pgx/v5` pool; `Migrate(ctx)` with `golang-migrate` or a minimal
  embedded runner (own table `schema_migrations`). Prefer the minimal runner (fewer deps).
- Queries via `sqlc` (config `sqlc.yaml`, queries in `queries/*.sql`, generated into `internal/server/store/db`).
- Tasks: `CreateTask`, `GetTask`, `ListTasks(filter{status[], node_id, requested_by}, page{limit, cursor})`,
  `ClaimQueuedTasks(ctx, limit) []Task` — `select … from tasks where status='queued' order by priority desc, created_at
  for update skip locked limit $1` inside a tx, then `update … set status='scheduled'` is **not** done here; claiming
  returns candidates and the scheduler decides (step 12). For step 06 provide the simple path:
  `AssignTask(ctx, taskID, nodeID, leaseID, leaseExpires)` (`queued→scheduled`, sets `scheduled_at`, `attempts+1`).
- State transitions as one function `TransitionTask(ctx, taskID, from []Status, to Status, patch)` that fails with
  `ErrInvalidTransition` if the row is not in one of `from`. Valid graph:
  `queued→scheduled→provisioning→running→{succeeded,failed,cancelled}`; `{scheduled,provisioning,running}→{lost,cancelled}`;
  `{scheduled,provisioning,running}→queued` (requeue, only when attempts < max_attempts).
- Events: `AppendEvents(ctx, taskID, []Event)` idempotent on `(task_id, seq)` (`on conflict do nothing`), returns highest seq.
  `ListEvents(taskID, fromSeq, limit)`. Log chunks: `AppendLogChunks`, `ListLogChunks(taskID, fromSeq, limit)`,
  `PruneLogChunks(olderThan)`.
- Nodes: `CreateNode`, `GetNode`, `GetNodeByKeyHash`, `ListNodes`, `UpdateNodeHeartbeat(id, status, capacity, version)`,
  `SetNodeStatus`, `DeleteNode`.
- Enrollment tokens: `CreateEnrollmentToken(labels, ttl, createdBy) (plaintext, id)`; `ConsumeEnrollmentToken(plaintext, nodeID)`
  (single use, atomic, checks expiry). Tokens are 32 random bytes base64url; only the SHA-256 is stored.
- Secrets: `PutSecret(name, ciphertext, nonce, createdBy)` (version+1 on conflict), `GetSecretCiphertext`, `ListSecrets`, `DeleteSecret`.
- Artifacts: `CreateArtifact`, `ListArtifacts(taskID)`, `GetArtifact`.
- Users/audit: `UpsertUser(login, displayName)`, `Audit(actor, action, subject, details)`.
- Listen/notify helper: `NotifyTaskEvents(taskID)` / `SubscribeTaskEvents(ctx)` using `pg_notify('podium_task_events', task_id)`
  so the API can fan out live updates without polling (step 06).

## Out of scope
- Scheduler logic (12). Encryption (09 — this step stores ciphertext blindly). HTTP anything.

## Acceptance checklist
- [ ] `make test-integration` boots Postgres 16 via testcontainers, runs migrations from empty, and all store tests pass.
- [ ] Transition tests cover every legal edge and at least three illegal ones.
- [ ] `ClaimQueuedTasks` tested with two concurrent claimers: no task is returned twice.
- [ ] `AppendEvents` is idempotent (re-appending the same seq is a no-op) and returns the high-water mark.
- [ ] `ConsumeEnrollmentToken` rejects reuse and expiry; the plaintext never touches the DB.
- [ ] `sqlc generate` is idempotent and committed.

## Verification
```sh
make test-integration ./internal/server/store/...
sqlc generate && git status --porcelain internal/server/store/db | wc -l   # 0
```

## Notes
- All timestamps `timestamptz`, all times in Go are UTC.
- Use `pgx` native types; avoid `database/sql`.
- Keep `spec jsonb` as the exact `pkg/spec.TaskSpec` JSON — no separate columns for spec fields.

## Hand-off notes
_(fill in when done)_
