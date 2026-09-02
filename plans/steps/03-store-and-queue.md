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
MVP-0 trims applied (per `00-index.md`, unit C): the `artifacts`, `secrets`, `users` and `audit_log`
tables and the `PutSecret`/`GetSecretCiphertext`/`ListSecrets`/`DeleteSecret`,
`CreateArtifact`/`ListArtifacts`/`GetArtifact`, `UpsertUser` and `Audit` methods are **not** built
— trimmed for MVP-0. `tasks.requested_by` is kept. No checklist item below is affected by the trim.

- [x] `make test-integration` boots Postgres 16 via testcontainers, runs migrations from empty, and all store tests pass.
- [x] Transition tests cover every legal edge and at least three illegal ones.
- [x] `ClaimQueuedTasks` tested with two concurrent claimers: no task is returned twice.
- [x] `AppendEvents` is idempotent (re-appending the same seq is a no-op) and returns the high-water mark.
- [x] `ConsumeEnrollmentToken` rejects reuse and expiry; the plaintext never touches the DB.
- [x] `sqlc generate` is idempotent and committed.

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

Unit C of the MVP-0 track. Everything below is what step 06 (and later 12) code against.

### Read this first: three edges were added to the state graph

The step file draws the graph as
`queued→scheduled→provisioning→running→{succeeded,failed,cancelled}` plus
`{scheduled,provisioning,running}→{lost,cancelled}` plus `{scheduled,provisioning,running}→queued`.
Three edges are **added** on top of that, because MVP-0 cannot be built without them:

| added edge | why |
|---|---|
| `queued → cancelled` | Step 06's own In scope says `CancelTask` does "`queued→cancelled` directly". |
| `scheduled → failed` | Design §10: "Image pull fails / registry down → … task fails fast with `attempts+1`". A task can die before it ever runs. |
| `provisioning → failed` | Design §10: "Sidecar never becomes ready → **Task fails at `provisioning`**". |

Nothing else was widened. Terminal states (`succeeded`, `failed`, `cancelled`, `lost`) have no
outgoing edges at all, so a retry is a *requeue of a live task*, never a resurrection of a dead one.
The full graph lives in one place, `legalTransitions` in `internal/server/store/tasks.go`, and
`store.CanTransition(from, to Status) bool` exposes it. If the orchestrator wants the three edges
gone, delete them from that map: the test table in `TestTransitionEveryLegalEdge` asserts it has not
drifted, so it will fail loudly rather than silently.

### Package and import path

```go
import "github.com/alvaroibarguen/podium/internal/server/store"
```

`internal/server/store/db` is the sqlc output. **Do not import it outside the store package** — it is
an implementation detail and its types leak `[]byte` jsonb and `*string` nullables.

### Constructor and lifecycle

```go
func New(ctx context.Context, databaseURL string) (*Store, error)  // pgxpool; pings before returning
func (s *Store) Migrate(ctx context.Context) error                 // idempotent, concurrency-safe
func (s *Store) Ping(ctx context.Context) error                    // /readyz
func (s *Store) Close()                                            // no error to check
```

Start order for `podium-server`: `New` → `Migrate` → everything else. `Migrate` takes a session-level
`pg_advisory_lock` and applies each embedded file in its own transaction, recording it in its own
`schema_migrations(version text primary key, applied_at timestamptz)` table. Five servers starting at
once is tested (`TestMigrateIsConcurrencySafe`).

### Types

```go
type Status string   // queued scheduled provisioning running succeeded failed cancelled lost
                     // constants StatusQueued … StatusLost; AllStatuses; (Status).Valid(); (Status).Terminal()
type NodeStatus string // NodeOnline NodeUnreachable NodeOffline NodeDraining

type Task struct {
    ID             string
    Spec           spec.TaskSpec   // the exact tasks.spec jsonb, no exploded columns
    Status         Status
    Priority       int32
    RequestedBy    string
    NodeID         string          // "" when unassigned (the column is nullable)
    LeaseID        string          // "" when unassigned
    LeaseExpiresAt *time.Time
    Attempts       int32
    MaxAttempts    int32
    CreatedAt      time.Time
    ScheduledAt    *time.Time
    StartedAt      *time.Time
    FinishedAt     *time.Time
    ExitCode       *int32          // nil until the task exits; 0 is a real exit code
    Usage          *Usage
    FailureReason  string
}

type NewTask struct { ID string; Spec spec.TaskSpec; Priority int32; RequestedBy string; MaxAttempts int32 }
type Filter  struct { Status []Status; NodeID, RequestedBy string }   // zero value = no filter
type Page    struct { Limit int; Cursor string }                      // Limit 0 -> 50, capped at 500
type Patch   struct { StartedAt, FinishedAt *time.Time; ExitCode *int32; Usage *Usage
                      FailureReason *string; NodeID, LeaseID *string; LeaseExpiresAt *time.Time }

type Event    struct { Seq uint64; Kind string; TS time.Time; Payload json.RawMessage }
type LogChunk struct { Seq uint64; Stream, Sidecar string; TS time.Time; Bytes []byte }

type Usage        struct { CPUSeconds float64; PeakMemoryMB, WallMS int64 }  // mirrors podium.v1.Usage
type NodeCapacity struct { MaxTasks, CPUCores int32; MemoryMB int64 }        // mirrors podium.v1.NodeCapacity

type Node    struct { ID, Name string; Tags, Labels []string; Capacity NodeCapacity
                      NodeKeyHash []byte; Status NodeStatus; Version string
                      LastHeartbeatAt *time.Time; CreatedAt time.Time }
type NewNode struct { ID, Name string; Tags, Labels []string; Capacity NodeCapacity
                      NodeKeyHash []byte; Status NodeStatus; Version string }
```

`Usage` and `NodeCapacity` are store-owned Go structs marshalled into the `usage`/`capacity` jsonb
columns, **not** `protojson` of the proto messages. Their JSON field names match the proto field
names (`cpu_seconds`, `peak_memory_mb`, `wall_ms`, `max_tasks`, `cpu_cores`, `memory_mb`), so the two
are trivially convertible, but step 06 must do that conversion field by field. Every `time.Time` the
store returns is UTC.

### Sentinel errors — match with `errors.Is`

```go
store.ErrNotFound           // no such task / node / token
store.ErrInvalidTransition  // wrong `from`, illegal edge, or requeue past max_attempts
store.ErrTokenUsed          // enrollment token already redeemed (also what the loser of a race gets)
store.ErrTokenExpired       // enrollment token past expires_at
```

### Tasks

```go
func (s *Store) CreateTask(ctx context.Context, in NewTask) (Task, error)
func (s *Store) GetTask(ctx context.Context, taskID string) (Task, error)
func (s *Store) ListTasks(ctx context.Context, f Filter, p Page) (tasks []Task, nextCursor string, err error)
func (s *Store) ClaimQueuedTasks(ctx context.Context, limit int) ([]Task, error)
func (s *Store) AssignTask(ctx context.Context, taskID, nodeID, leaseID string, leaseExpires time.Time) error
func (s *Store) TransitionTask(ctx context.Context, taskID string, from []Status, to Status, patch Patch) (Task, error)
func CanTransition(from, to Status) bool
```

- `CreateTask` mints `task_01j…` via `internal/ids` when `NewTask.ID` is empty, and falls back
  `MaxAttempts → Spec.MaxAttempts → 1`. Status is always `queued`.
- `ListTasks` is **newest-first by ID** (ULIDs sort by mint time). `nextCursor` is the last row's ID
  and is `""` on the final page; feed it back as `Page.Cursor`.
- `ClaimQueuedTasks` runs `select … where status='queued' order by priority desc, created_at
  for update skip locked limit $1` inside a transaction and **does not transition anything**.
  It returns candidates. **`AssignTask` is the exclusivity gate**: it is
  `update … where id=$1 and status='queued'`, so exactly one racing scheduler wins and every loser
  gets `ErrInvalidTransition`. Write the naive scheduler as claim → for each candidate `AssignTask` →
  on `ErrInvalidTransition`, skip to the next task. That loop is what
  `TestClaimQueuedTasksTwoConcurrentClaimers` exercises with 60 tasks and two claimers.
- `TransitionTask` locks the row `FOR UPDATE`, checks `from` (an empty/nil `from` means "any current
  status, as long as the edge is legal"), checks the graph, checks the requeue attempt budget, then
  applies the patch. A nil patch field leaves that column alone; **transitioning to `queued` always
  clears `node_id`, `lease_id` and `lease_expires_at`** whatever the patch says. It never sets
  `attempts` — only `AssignTask` does (`attempts+1`), so "attempt" means "assignment".
- Step 06's `logs.Ingest` mapping, spelled in these calls:
  `provisioning` → `TransitionTask(id, []Status{StatusScheduled}, StatusProvisioning, Patch{})`;
  `started` → `…{StatusProvisioning}, StatusRunning, Patch{StartedAt: &t}`;
  `finished` exit 0 → `…{StatusRunning}, StatusSucceeded, Patch{FinishedAt: &t, ExitCode: &c, Usage: &u}`;
  non-zero → same with `StatusFailed`;
  `error` non-retryable → `…nil, StatusFailed, Patch{FailureReason: &msg, FinishedAt: &t}` (legal from
  `scheduled`, `provisioning` and `running`).

### Events and log chunks

```go
func (s *Store) AppendEvents(ctx context.Context, taskID string, events []Event) (highSeq uint64, err error)
func (s *Store) ListEvents(ctx context.Context, taskID string, fromSeq uint64, limit int) ([]Event, error)
func (s *Store) AppendLogChunks(ctx context.Context, taskID string, chunks []LogChunk) (highSeq uint64, err error)
func (s *Store) ListLogChunks(ctx context.Context, taskID string, fromSeq uint64, limit int) ([]LogChunk, error)
func (s *Store) PruneLogChunks(ctx context.Context, olderThan time.Time) (deleted int64, err error)
```

Both appends are `on conflict (task_id, seq) do nothing` inside one transaction and return the task's
high-water mark **after** the write — that is the number to put in `Ack.seq`. Re-appending an acked
batch is free and never overwrites the stored row (a replayed `seq` keeps its original payload; tested).
An empty batch is legal and just reads the current mark, so `AppendEvents(ctx, id, nil)` is how you ask
"where am I?". `fromSeq` is inclusive; `limit <= 0` means 50.

**The two sequences are separate.** `task_events.seq` and `task_log_chunks.seq` are independent
primary keys, so `AppendEvents` and `AppendLogChunks` return two different high-water marks. Step 06
splits one node batch across both tables and must decide which mark it acks — the safest is
`min(eventsHigh, logsHigh)` over the batch it just wrote, or a single node-side seq space written to
both tables (the node emits one monotonic `seq` per task, so the same value simply lands in whichever
table matches the kind, and the two marks then interleave without colliding).

### Nodes and enrollment tokens

```go
func (s *Store) CreateNode(ctx context.Context, in NewNode) (Node, error)
func (s *Store) GetNode(ctx context.Context, nodeID string) (Node, error)
func (s *Store) GetNodeByKeyHash(ctx context.Context, keyHash []byte) (Node, error)
func (s *Store) ListNodes(ctx context.Context) ([]Node, error)
func (s *Store) UpdateNodeHeartbeat(ctx context.Context, nodeID string, status NodeStatus, capacity *NodeCapacity, version string) error
func (s *Store) SetNodeStatus(ctx context.Context, nodeID string, status NodeStatus) error
func (s *Store) DeleteNode(ctx context.Context, nodeID string) error

func HashToken(plaintext string) []byte   // raw SHA-256; use it for node keys too
func (s *Store) CreateEnrollmentToken(ctx context.Context, labels []string, ttl time.Duration, createdBy string) (plaintext, id string, err error)
func (s *Store) ConsumeEnrollmentToken(ctx context.Context, plaintext, nodeID string) (labels []string, err error)
```

- `CreateNode` mints `node_01j…` when `ID` is empty and **requires** `NodeKeyHash`. Step 06 generates
  32 random bytes for the node key, returns them once in `EnrollResponse`, and stores
  `store.HashToken(key)` — the same hash function the tokens use, so there is one hashing rule in the
  codebase, not two.
- `UpdateNodeHeartbeat` always stamps `last_heartbeat_at`. A nil `capacity` or an empty `version`
  leaves that column at its last known value, so a bare keep-alive does not erase what `Hello` said.
- `CreateEnrollmentToken` returns **32 random bytes as `base64.RawURLEncoding`** and stores only the
  SHA-256. `ttl <= 0` falls back to `store.DefaultEnrollmentTokenTTL` (1h). ID prefix is `etok_`
  (`00-index.md` fixes `task_`/`node_`/`lease_` and leaves the rest to us).
- `ConsumeEnrollmentToken` is one `update … where token_hash=$1 and used_at is null and
  expires_at > now() returning labels`, so single-use is enforced by the database, not by a
  read-then-write. Eight concurrent redemptions of one token yield exactly one winner (tested).

### Live updates

```go
const TaskEventsChannel = "podium_task_events"
func (s *Store) NotifyTaskEvents(ctx context.Context, taskID string) error
func (s *Store) SubscribeTaskEvents(ctx context.Context) (<-chan string, error)
```

The payload is a **task ID wake-up, not the event**: on receipt, read the rows you are missing with
`ListEvents`/`ListLogChunks`. Call `NotifyTaskEvents` after the append has committed.
`SubscribeTaskEvents` **hijacks** a connection out of the pool (a connection that has run `LISTEN`
must never go back in); cancel the context to unsubscribe, which closes the channel. Open **one**
subscription per server process and fan out to individual `StreamTaskEvents` clients in memory —
one hijack per HTTP client would drain the pool.

### sqlc: what is generated and what is not

`sqlc.yaml` is at the repo root; queries live in `internal/server/store/queries/*.sql`; output is
`internal/server/store/db` (package `db`), committed. `sqlc generate` is byte-for-byte idempotent.

**Every SQL statement in this package is sqlc-generated. Nothing is hand-written pgx SQL.** The three
things the brief expected to fight the generator all fit, as follows:

- *Dynamic `ListTasks` filters* — expressed with sentinel parameters rather than string building:
  `(cardinality(@statuses::text[]) = 0 or status = any(@statuses::text[]))` and
  `(@node_id::text = '' or node_id = @node_id::text)`, same for `requested_by` and the `@after_id`
  cursor. Consequence: an empty string cannot be *filtered for*, only *ignored*. Nothing needs that.
- *`FOR UPDATE SKIP LOCKED`* — sqlc generates the query fine; the transaction is Go's job. The store's
  `inTx` helper begins a `pgx` transaction and hands `q.WithTx(tx)` to the closure.
- *`LISTEN`/`NOTIFY`* — `NotifyTaskEvents` is a generated `:exec` over `select pg_notify(...)`.
  Only the `LISTEN` side is raw pgx, and it has to be: `WaitForNotification` is a connection method
  with no SQL to generate.

Two more generator notes:

1. **Multi-array `unnest(...)` does not work.** sqlc's analyser rejects it
   (`function unnest(unknown, unknown, unknown, unknown) does not exist`). Batch inserts therefore use
   sqlc's pgx `:batchexec` (`AppendEvent`, `AppendLogChunk` in `db/batch.go`), which builds a
   `pgx.Batch` — still one round trip, and unlike `:copyfrom` it supports `on conflict do nothing`.
   The `AppendEvents`/`AppendLogChunks` store methods wrap the singular batch queries.
2. `sqlc.yaml` sets `overrides` mapping `timestamptz` to `time.Time` / `*time.Time`. Without them
   sqlc emits `pgtype.Timestamptz` and pgx leaks into every signature in the tree.

### Migrations

`internal/server/store/migrations/0001_init.sql`, `//go:embed migrations/*.sql`. Add
`0002_….sql` and it is picked up automatically (files are applied in lexical order). Every statement
is `create table if not exists` / `create index if not exists` inside a transaction, so the file is
safe even if `schema_migrations` is lost. Non-obvious addition beyond the step file's DDL: a unique
index `nodes_key_hash_idx` on `nodes(node_key_hash)`, because `GetNodeByKeyHash` is an authentication
lookup and must not be able to match two rows; plus `task_log_chunks_ts_idx` on `(ts)` so
`PruneLogChunks` is not a sequential scan.

### `deploy/docker-compose.dev.yml`

Postgres 16 only (`postgres:16-alpine`, named volume `podium-pgdata`, `pg_isready` healthcheck,
`POSTGRES_USER/PASSWORD/DB=podium`). The published port is **`127.0.0.1:5432:5432`**, not `5432:5432`
— loopback-only, since the credentials are dev throwaways. The DSN in the MVP-0 acceptance script,
`postgres://podium:podium@127.0.0.1:5432/podium`, is unchanged. Integration tests do not use this
file; they boot their own container via testcontainers.

### Verification adaptation

The step file's `make test-integration ./internal/server/store/...` is not valid make syntax (the path
would be read as a target). The equivalent is
`go test -tags integration ./internal/server/store/... -count=1`, or `make test-integration` for the
whole tree.

### Deviations, in one list

1. Three transition edges added (`queued→cancelled`, `scheduled→failed`, `provisioning→failed`) — see
   the table at the top.
2. `UpdateNodeHeartbeat` takes `capacity *NodeCapacity`, not a value, so a keep-alive can decline to
   overwrite it.
3. `AppendLogChunks` returns a high-water mark (the step file only specifies one for `AppendEvents`);
   symmetry, and step 06 needs it to ack log batches.
4. `PruneLogChunks` returns the delete count.
5. Compose publishes on `127.0.0.1:5432` rather than `0.0.0.0:5432`.
6. `go get github.com/jackc/pgx/v5/pgxpool@v5.10.0 github.com/jackc/pgx/v5/pgconn@v5.10.0` was needed:
   the pre-seeded `go.sum` was missing `github.com/jackc/puddle/v2` and `golang.org/x/text/secure/precis`.
   `go mod tidy` was **not** run. Purely additive: `puddle/v2 v2.2.2`, `golang.org/x/sync v0.22.0`,
   `github.com/pkg/errors v0.9.1`; no existing version moved.

### Open problems

- Port 5432 on this machine is already held by an unrelated `socat` tunnel, so
  `docker compose -f deploy/docker-compose.dev.yml up -d postgres` cannot bind as written here. The
  compose file itself is verified (brought up healthy on a remapped port, `psql` connected as
  `podium@podium`, then torn down). Whoever runs the MVP-0 acceptance script has to free 5432 first.
- `ClaimQueuedTasks` returning candidates without transitioning them means the SKIP LOCKED row locks
  are released when its transaction commits, i.e. before the caller acts on them. That is what the
  step file asks for, and `AssignTask` closes the hole. If step 12 wants the lock held across the
  assignment decision it needs a tx-scoped variant; that is a scheduler concern, deliberately not
  built here.

