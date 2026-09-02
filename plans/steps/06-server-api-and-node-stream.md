# Step 06 — Server: Task API, node stream handling, dev transport

**Milestone:** M1 · **Depends on:** 03 · **Design ref:** §5 protocol, §6.1 packages, §4.3 dev mode

## Goal
A running `podium-server` that accepts tasks over Connect RPC, holds node streams, hands tasks to a
connected node with a naive assignment, ingests events/logs, and streams them back to clients — all over
the `dev` transport on localhost. This is the server half of the M1 vertical slice.

## In scope

### Transport interface (`internal/transport`)
```go
type Identity struct { Kind IdentityKind /* User | Node | DevToken */; Login string; NodeTags []string; RemoteAddr string }
type Listener interface { Listen(ctx) (net.Listener, error); Identify(r *http.Request) (Identity, error) }
```
- `internal/transport/dev`: listens on `PODIUM_DEV_LISTEN` (must be loopback or the server refuses to start);
  `Identify` reads `Authorization: Bearer <PODIUM_DEV_TOKEN>` → `Identity{Kind: DevToken, Login: "dev"}`.
  Nodes in dev mode also present the dev token; their `Kind` becomes `Node` once they send `Hello` with a valid key.
- HTTP middleware `WithIdentity` puts the identity in context; handlers read it with `identity.From(ctx)`.

### Server wiring (`cmd/podium-server`, `internal/server`)
- Config from env (canonical names). Start order: store + migrate → transport → mux → HTTP server (h2c so Connect
  bidi streams work over plaintext in dev).
- Mux: Connect handlers for `TaskService`, `NodeService`, `NodeAdminService` (only `CreateEnrollmentToken`, `ListNodes`
  now); `/healthz`, `/readyz` (DB ping), `/metrics`. `SecretService`/`ArtifactService` return `unimplemented` until 09/10.
- Graceful shutdown on SIGTERM: stop accepting, close node streams with a `Drain`-less goodbye (nodes reconnect), 10s deadline.

### `internal/server/nodes` — stream sessions
- `Enroll`: requires `Identity.Kind ∈ {Node, DevToken}`; consumes token via store; creates node with random 32-byte
  `node_key` (returned once; only SHA-256 stored). Labels = token labels ∪ request labels.
- `Stream`: first message must be `Hello` within 5s; validate `node_id` + `node_key` hash; mark node `online`;
  register a `Session{nodeID, send chan ServerMessage, lastHeartbeat, capacity, freeSlots, running set}` in an in-memory
  registry (one session per node; a new stream replaces the old one, which is closed).
- Handle `Heartbeat` → update session + `store.UpdateNodeHeartbeat`. Handle `TaskEvent` → `logs.Ingest` (below) then `Ack`
  every batch (ack the highest seq persisted). Handle stream end → mark session gone (status changes are step 12's job;
  for now set `unreachable` immediately).
- `Assign(nodeID, task, resolvedSecrets)` sends on the session channel; `Cancel(nodeID, taskID)` likewise.

### Naive assignment (temporary, replaced in step 12)
- A goroutine every 1s: `ClaimQueuedTasks(limit 10)`; for each task pick **any** online session with `freeSlots > 0`
  whose labels ⊇ `spec.Labels`; `store.AssignTask` (`queued→scheduled`, lease 2 min) then `Assign`. No node → leave queued.
- Keep it in `internal/server/scheduler/naive.go` behind `type Scheduler interface { Run(ctx) }` so step 12 swaps it.

### `internal/server/logs` — ingest and fan-out
- `Ingest(taskID, events)`: split `log` kinds into `task_log_chunks`, everything else into `task_events`, in one tx;
  apply status transitions from kinds: `provisioning→provisioning`, `started→running` (set `started_at`),
  `finished→succeeded|failed` by exit code (set `finished_at`, `exit_code`, `usage`), `error` with `retryable=false` → `failed`.
  Then `pg_notify`. Idempotent thanks to `(task_id, seq)` PK.
- `Subscribe(ctx, taskID, fromSeq) <-chan Event`: replay from store then live via LISTEN; used by `StreamTaskEvents`.

### `internal/server/api` — TaskService
- `CreateTask`: validate spec (`pkg/spec`), `requested_by = identity.Login`, insert `queued`, audit.
- `GetTask`, `ListTasks` (filters + cursor pagination), `CancelTask` (`queued→cancelled` directly; otherwise send `Cancel`
  to the node and mark `cancelled` on `exited`), `StreamTaskEvents` (server-streaming; merges `task_events` and
  `task_log_chunks` ordered by seq).

## Out of scope
- Real scheduler, leases expiry, reconciliation (12). Tailnet identity (11). Secrets resolution (09) — pass empty. UI (13).

## Acceptance checklist
- [x] Server starts with `PODIUM_TRANSPORT=dev`, refuses `PODIUM_DEV_LISTEN=0.0.0.0:8080` with a clear error.
      (`TestServerRefusesNonLoopbackDevListen`, `dev.TestNewRefusesNonLoopbackListenAddress`, and live.)
- [x] `curl -H 'Authorization: Bearer $T' -d '{"spec":{...}}' localhost:8080/podium.v1.TaskService/CreateTask` returns a queued task
      (Connect JSON works without the CLI). (`TestConnectJSONWithoutTheCLI`, and live against the compose Postgres.)
- [x] A **fake node** test client (in `internal/server/nodes/nodes_test.go`) enrolls, opens a stream, sends Hello, receives
      an `Assign` for a created task, sends provisioning/started/log/exited/finished events, receives `Ack`s;
      the task ends `succeeded`/`failed` per exit code and `StreamTaskEvents` replays every event in order.
      (`TestFakeNodeRunsATaskEndToEnd`, `TestNonZeroExitFailsTheTask`.)
- [x] Re-sending an already-acked event batch does not duplicate rows. (`TestReplayedBatchDoesNotDuplicateRows`.)
- [x] Two fake nodes: a task with `labels:[gpu]` goes only to the node advertising `gpu`; a task with no eligible node stays `queued`.
      (`TestLabelRoutingAndUnschedulableTasks`.)
- [x] Node stream disconnect marks the node `unreachable`; reconnect with `Hello` marks it `online` again.
      (`TestDisconnectMarksUnreachableAndReconnectMarksOnline`.)
- [x] `/readyz` returns 503 when Postgres is down, 200 when up. (`TestReadyzTracksPostgres`, and live.)
- [x] No `Assign` is ever logged unredacted (grep test for the `RedactForLog` helper usage or a lint rule).
      (`internal/server/redaction_test.go`, `TestNoAssignIsLoggedUnredacted` — a plain `go test` unit test, no tag.)

## Verification
```sh
make test-integration ./internal/server/...
PODIUM_TRANSPORT=dev PODIUM_DEV_TOKEN=t PODIUM_DATABASE_URL=… ./bin/podium-server &
curl -sf localhost:8080/healthz && curl -sf localhost:8080/readyz
```

## Notes
- Use `connectrpc.com/connect` + `golang.org/x/net/http2/h2c`. Enable `connect.WithCompressMinBytes(1024)`.
- Session registry is in-memory and single-process by design (multi-server is an open question).
- Log chunk fan-out to live subscribers should not block ingest — use per-subscriber buffered channels and drop-to-replay on overflow
  (subscriber re-reads from store when it detects a gap).

## Hand-off notes

Unit E of the MVP-0 track: the whole server half, built to the trims in `00-index.md` (no secrets
resolution, no `SecretService`/`ArtifactService` — they are not in the proto at all, so there is nothing
to register as `unimplemented`; no `audit_log`, so `CreateTask` logs with `slog` instead of auditing).
Everything below is what **unit F** (node daemon + CLI) and **unit G** (web UI) code against.

### What exists

```
internal/transport/transport.go          Identity, IdentityKind, Listener, WithIdentity, From
internal/transport/dev/dev.go            loopback-only bearer-token listener
internal/server/config.go                Config, ConfigFromEnv, Validate
internal/server/server.go                Server: store -> transport -> mux -> http, scheduler, fan-out
internal/server/api/{tasks,admin,convert}.go   TaskService + NodeAdminService handlers
internal/server/nodes/{service,stream,registry}.go   Enroll, Stream, Session, Registry
internal/server/logs/{logs,convert}.go   Ingest, Subscribe, MarkCancelling, status transitions
internal/server/scheduler/{scheduler,naive}.go       Scheduler interface + the 1s naive loop
internal/server/store/batch.go           AppendBatch (new store method, see Deviations)
cmd/podium-server/main.go                cobra root + `serve`

internal/server/nodes/nodes_test.go      //go:build integration — the whole-server suite (see below)
internal/transport/{transport_test,dev/dev_test}.go  plain unit tests
internal/server/{config_test,redaction_test}.go      plain unit tests
internal/server/scheduler/naive_test.go              plain unit tests
```

### Running the server

Environment only, canonical names, no flags and no config file:

| var | required | default | meaning |
|---|---|---|---|
| `PODIUM_DATABASE_URL` | yes | — | pgx DSN; the server migrates on start |
| `PODIUM_TRANSPORT` | no | `dev` | `dev` only; `tailnet` errors with "not implemented yet (step 11)" |
| `PODIUM_DEV_LISTEN` | no | `127.0.0.1:8080` | must resolve to loopback or the process refuses to start |
| `PODIUM_DEV_TOKEN` | yes for `dev` | — | the shared bearer token |

```sh
PODIUM_TRANSPORT=dev PODIUM_DEV_TOKEN=devtoken \
  PODIUM_DATABASE_URL=postgres://podium:podium@127.0.0.1:5432/podium ./bin/podium-server
```

`podium-server` and `podium-server serve` do the same thing. SIGTERM/SIGINT closes every node session
(so node handlers return), then `http.Shutdown` with a 10s budget (`server.ShutdownTimeout`).

Open endpoints, no token: `/healthz` (always 200 while the process is up), `/readyz` (200, or 503 with
body `postgres unreachable` when `store.Ping` fails within 2s), `/metrics` (Go + process collectors only;
no Podium metrics yet). Everything else is behind `transport.WithIdentity` and answers **401 with
`WWW-Authenticate: Bearer`** without a valid token.

### HTTP/2 is mandatory for the node stream — read this before writing the node client

`internal/server/server.go` sets `http.Server.Protocols` to HTTP/1.1 **plus** `UnencryptedHTTP2`. It does
**not** use `golang.org/x/net/http2/h2c`: that package is deprecated in x/net v0.58 and `staticcheck`
(SA1019) fails `make lint` on it. The stdlib field is the replacement and needs Go ≥ 1.24, which the
module already requires (`go 1.25.0`).

Consequences:
- Unary RPCs and `StreamTaskEvents` (server-streaming) work over plain HTTP/1.1 — that is why `curl` and
  the browser's `fetch` work with no special setup. **Unit G needs nothing.**
- `NodeService.Stream` is **bidirectional** and Connect refuses it on HTTP/1.1. Unit F must build its
  client on an h2c transport:

```go
tr := &http.Transport{Protocols: new(http.Protocols)}
tr.Protocols.SetUnencryptedHTTP2(true)          // add SetHTTP1(true) if the same client also does unary
client := podiumv1connect.NewNodeServiceClient(&http.Client{Transport: tr}, "http://127.0.0.1:8080")
```

`internal/server/nodes/nodes_test.go` (`h2cClient`) is a working reference for exactly this.

### How a node authenticates under the dev transport

Two layers, and unit F needs both:

1. **Transport**: every HTTP request — `Enroll` and `Stream` alike — carries
   `Authorization: Bearer <PODIUM_DEV_TOKEN>`. That is compared in constant time and yields
   `Identity{Kind: KindDevToken, Login: "dev"}`. There is no per-node HTTP credential in dev mode.
2. **Protocol**: `Hello` carries `node_id` + `node_key`. The server hashes the key with
   `store.HashToken` (raw SHA-256) and looks the node up by hash, then checks the row's ID matches the
   claimed `node_id`. A mismatch is `CodeUnauthenticated`.

Enrollment flow, end to end:

```
podium node enroll-token       -> NodeAdminService.CreateEnrollmentToken{labels, ttl} -> {token, expires_at}
podium-node first run          -> NodeService.Enroll{token, hostname, arch, os, cpu_cores, memory_mb,
                                                     docker_version, labels}          -> {node_id, node_key}
```

- The enrollment token is 32 random bytes, `base64.RawURLEncoding`, single-use, default TTL 1h
  (`store.DefaultEnrollmentTokenTTL`). Redeeming it twice is `CodePermissionDenied` ("token already used").
- `node_key` is 32 fresh random bytes, `base64.RawURLEncoding`, **returned exactly once**; only its
  SHA-256 reaches Postgres. Persist it in the node's data dir. There is no recovery path — a lost key
  means a new enrollment token.
- The node's labels are `token.labels ∪ EnrollRequest.labels`, deduped and sorted. `Hello.labels` is
  **ignored** for scheduling: the registry uses the labels stored at enrollment. If unit F wants a label
  added later, that needs a new RPC (nothing in MVP-0 provides one).
- A freshly enrolled node is `offline`. It becomes `online` when `Hello` is accepted, and `unreachable`
  the moment its stream ends.

### The stream, precisely

- The first message **must** be `Hello`, within 5s (`CodeDeadlineExceeded` otherwise); anything else is
  `CodeInvalidArgument`. A second `Hello` on an open stream is logged and ignored.
- One session per node. A new stream **replaces** the old one and closes it; the replaced handler
  notices it is no longer the registered session and does **not** mark the node unreachable. So a node
  may reconnect without draining first.
- `Hello.capacity.max_tasks` is what the scheduler budgets against: `free_slots = max_tasks -
  len(running_task_ids)`. **A node that sends `max_tasks: 0` never gets work.**
- `Heartbeat` (every 10s) updates `last_heartbeat_at` and overwrites the session's `free_slots` with what
  the node reports, so unit F must keep `Heartbeat.free_slots` honest. `Heartbeat.load.running_tasks`,
  `cpu_pct`, `mem_pct`, `disk_free_bytes` are recorded but nothing reads them yet.
- The server also decrements `free_slots` locally the instant it pushes an `Assign` (`Session.reserve`),
  because the scheduler ticks faster than the node heartbeats; without it one tick could hand a
  four-slot node ten tasks.
- Server→node messages are queued on a 64-deep buffered channel. `Drain` exists in the proto but nothing
  sends it in MVP-0.
- Stream end (clean EOF or a broken connection) → `nodes.unreachable`. Nothing ever sets `offline` or
  expires a lease yet; that is step 12.

### `Ack` semantics — the exact contract

`Ack{task_id, seq}` where **`seq` is the highest seq in the batch the server just committed**, not a
per-table high-water mark. The node may drop everything `<= seq` from its replay buffer.

Why it is spelled that way: the node numbers **all** of a task's events from one monotonic seq space
starting at 1, but server-side a batch is split — `kind == log` rows go to `task_log_chunks`, everything
else to `task_events`. Those two tables have independent `(task_id, seq)` primary keys and therefore
independent high-water marks. Acking `min(eventsHigh, logsHigh)` would stall a log-only stretch of a task
forever, and acking `max` would ack rows that were never written. `store.AppendBatch` writes both halves
in **one transaction**, so the batch is all-or-nothing and the batch maximum is the honest ack.

Practical rules for unit F:
- Keep one seq counter per task, starting at 1, incremented for every event of any kind.
- Never reuse a seq for different content: the insert is `on conflict (task_id, seq) do nothing`, so the
  **first** write of a seq is the one that survives. A replay must be byte-identical.
- Re-sending an acked batch is free and idempotent — rows, status transitions and the ack all no-op.
- An `Ingest` that fails is **not** acked; the node must keep replaying until it sees the ack.
- The server coalesces incoming events into batches of **100ms or 64KB**, whichever comes first, so one
  ack usually covers several events. Acks for a task are monotonic.

### Status transitions driven by events (`internal/server/logs`)

| event kind | from | to | patch |
|---|---|---|---|
| `provisioning` | `scheduled` | `provisioning` | — |
| `started` | `provisioning` | `running` | `started_at` |
| `finished`, exit 0 | `running` | `succeeded` | `finished_at`, `exit_code`, `usage` |
| `finished`, exit ≠ 0 | `running` | `failed` | `finished_at`, `exit_code`, `usage` |
| `error`, `retryable=false` | any legal | `failed` | `failure_reason`, `finished_at` |
| `pulling`, `log`, `exited`, `step`, `artifact` | — | — | stored only, no transition |

`exited` is recorded but deliberately does **not** move the task: `finished` is the terminal signal and
carries the usage numbers. Unit F must emit `finished` — a task that only emits `exited` stays `running`.
A rejected transition (a replayed batch, or a terminal task) is logged at debug and never fails the
ingest.

### TaskService

Procedure constants live in `podiumv1connect` (`TaskServiceCreateTaskProcedure`, …); the paths are
`/podium.v1.TaskService/<Method>`.

- **CreateTask** — `spec.FromProto` → `ApplyDefaults` → `Validate`; a bad spec is `CodeInvalidArgument`
  with the joined validation errors. `requested_by` is `Identity.Login` (`"dev"` under dev transport).
  Always returns status `queued`. `working_dir` defaults to `/workspace`, `timeout` to `3600s`,
  `max_attempts` to 1 — the response echoes the defaulted spec, so the CLI can print what will run.
- **GetTask** / **ListTasks** — `ListTasks` is newest-first by ULID; `filter.status` (repeated),
  `filter.node_id`, `filter.requested_by`; `page.limit` (0 → 50, capped at 500) and `page.cursor`. Feed
  `next_cursor` back verbatim; it is `""` on the last page.
- **CancelTask** — `queued` is cancelled synchronously and the response already says `cancelled`.
  Anything non-terminal further along records an in-memory cancel intent and pushes `Cancel` to the node,
  then returns the task **unchanged** (still `running`): the terminal status lands later, when the node's
  `finished` event arrives, and `logs.applyStatus` rewrites that terminal state to `cancelled`.
  A terminal task is `CodeFailedPrecondition`. **Cancel is slow in MVP-0** — with no runner the task
  command is PID 1, so a default-disposition SIGTERM is discarded and the container dies at the node's
  30s SIGKILL (exit 137); a command that traps TERM exits 143 in a couple of seconds. Any CLI/UI
  interaction and any test must allow ~35s and must not block on the response.
- **StreamTaskEvents** — see below.

Store sentinels map to Connect codes in one place (`api.storeError`): `ErrNotFound` → `CodeNotFound`,
`ErrInvalidTransition` → `CodeFailedPrecondition`, everything else → `CodeInternal`.

### `StreamTaskEvents`: ordering, replay, termination

`StreamTaskEventsRequest{task_id, from_seq}` → a server stream of `TaskEvent`.

- `from_seq` is **exclusive**: the stream starts at `from_seq + 1`, so `0` replays everything. This is
  what makes `logs -f` resumable across a server restart (unit F scenario 2): remember the last seq you
  rendered and reconnect with it.
- Ordering is **strictly ascending by seq**, merging `task_events` and `task_log_chunks`. That merge is
  total precisely because the node uses one seq space per task.
- Pagination is internal: 256 rows per table per round trip. When either page comes back full the merged
  run is cut at the lower of the two last seqs, so a client never sees seq N+1 before seq N.
- Live follow is a Postgres `LISTEN`/`NOTIFY` wake-up (`podium_task_events`) with a 1s poll as insurance.
  **One** LISTEN connection is hijacked per server process and fanned out in memory; subscribers get a
  coalescing one-slot signal, never the event, so a slow client can never block ingest.
- **The stream ends by itself** once the task is terminal and every event has been delivered — the
  handler returns and the client sees a clean EOF. Treat "stream closed" as "task finished", then
  `GetTask` for the exit code. It does not idle forever.
- A replayed event carries `lease_id: ""` (the lease is not a column) and `ts` from the stored row.
  Kinds with no payload (`provisioning`, `pulling`, `started`) store `{}` and come back with the oneof
  unset — do not type-assert their payload.
- Unknown task → `CodeNotFound`.

### NodeAdminService

- **CreateEnrollmentToken**`{labels, ttl}` → `{token, expires_at}`. `ttl` ≤ 0 → 1h. The plaintext is
  returned once and is never logged (only the `etok_…` id is).
- **ListNodes**`{}` → every enrolled node, with `status`, `labels`, `capacity`, `version`,
  `last_heartbeat_at`, `created_at`. `running_tasks` and `free_slots` are filled in **only** for nodes
  holding a live stream on this process; for anything else they are 0. Unit G should read "connected" as
  `status == ONLINE`, not as `free_slots > 0`.

### Naive scheduler

`internal/server/scheduler`. `Scheduler` is `interface{ Run(ctx) error }`; step 12 swaps `Naive` for the
real one without touching `server.go`. Every second: `ClaimQueuedTasks(10)`, then for each task pick the
connected node with the **most free slots** whose labels ⊇ `spec.labels` (ties break on node ID, so the
choice is deterministic), `AssignTask` (`queued→scheduled`, `attempts+1`, lease 2 min), then push
`Assign{task_id, lease_id, spec, deadline = now+15s}`. No eligible node ⇒ the task simply stays `queued`
forever, which is the documented MVP-0 behaviour for an unsatisfiable label set.

`Assign.deadline` is the 15s provisioning deadline from the design. **Nothing enforces it in MVP-0** —
there is no lease expiry and no reconciliation until step 12 — but unit F should still emit
`provisioning` promptly, because that is what moves the task out of `scheduled`.

### The integration suite

`internal/server/nodes/nodes_test.go`, build tag `integration`, package `nodes_test` (external, because
the harness starts the real `internal/server`, which imports `nodes`). It boots one `postgres:16-alpine`
via testcontainers for the package and gives every test its own database, then starts a real
`podium-server` on `127.0.0.1:0`.

```sh
go test -tags integration ./internal/server/... ./internal/transport/... -count=1 -v
go test -tags integration -race ./internal/server/nodes/... -count=1     # also clean
```

The `fakeNode` type in it is the smallest possible node daemon: enroll, `Hello`, heartbeat, emit the
`provisioning → started → log* → exited → finished` sequence with one seq space, read `Assign`/`Ack`.
**Unit F should read it before writing `internal/node/heartbeat` and the stream loop** — it is the
executable spec for the wire.

The step file's `make test-integration ./internal/server/...` is not valid make syntax (the path parses
as a target); the equivalent is the `go test` line above, or `make test-integration` for the whole tree.

### Deviations, and why

1. **`h2c.NewHandler` → `http.Server.Protocols`** (see above). Not optional: `staticcheck` SA1019 fails
   `make lint` on x/net v0.58's deprecated `h2c`. `golang.org/x/net` is no longer imported anywhere in
   the tree.
2. **`store.AppendBatch` was added to unit C's package** (`internal/server/store/batch.go`, 72 lines).
   `AppendEvents` and `AppendLogChunks` each own a transaction, so using both would let a reader see the
   second half of a node batch without the first. `AppendBatch` writes the events and the log chunks of
   one batch in a single transaction, reusing the same sqlc `:batchexec` queries. It does not return a
   high-water mark, because the ack is the batch maximum (see `Ack` semantics).
3. **`Ack.seq` is the batch maximum**, not `min(eventsHigh, logsHigh)` as unit C's hand-off suggested as
   one option. Unit C's other option — one node-side seq space written to both tables — is what the node
   actually does, and the minimum would stall a log-only task's replay buffer.
4. **`CreateTask` does not audit.** The `audit_log` table and `store.Audit` were trimmed in step 03; it
   logs `task created` with `slog` instead.
5. **No `SecretService`/`ArtifactService` registration.** The step file says they should return
   `unimplemented`; they do not exist in the proto at all (trim B), so there is nothing to register.
6. **The scheduler unwinds a failed push.** If `AssignTask` succeeds but the node's session died before
   the `Assign` reached it, the task would be stranded in `scheduled` with no reconciliation to rescue
   it, so `Naive.unwind` puts it back to `queued`, or fails it when the attempt budget is spent. This is
   step 12 work done early, in the smallest form that keeps MVP-0 from wedging.
7. **Cancel intent is in-memory** (`logs.MarkCancelling`), like the session registry. A server restart
   between `CancelTask` and the node's `finished` event loses the intent and the task lands
   `succeeded`/`failed` instead of `cancelled`. Durable cancellation is step 12's.
8. **`Session.reserve`** (local free-slot decrement on assign) is not in the step file; without it the 1s
   tick over-assigns, because the node's next heartbeat is up to 10s away.
9. **`go mod tidy` was run** (single unit in the tree). `github.com/prometheus/client_golang v1.24.1` is
   promoted to a direct require; MVS pulled `klauspost/compress 1.18.6 → 1.19.1` and
   `golang.org/x/crypto 0.54.0 → 0.55.0` with it. Nothing else moved.
10. **The live smoke test ran on non-default ports.** This machine has unrelated tunnels holding
    **5432** and **8080** (both accept connections and are not Podium's). Postgres came up with
    `PODIUM_PG_PORT=55432` and the server with `PODIUM_DEV_LISTEN=127.0.0.1:18080`. The documented
    defaults are unchanged and untested on this host; whoever runs the MVP-0 acceptance script must free
    both ports first.

### Open problems

- **The session registry is per process.** A node's stream terminates on exactly one server, so only that
  server can `Assign` or `Cancel` it. Two `podium-server` processes against one database would both run
  a scheduler; `AssignTask` keeps them from double-assigning, but the loser's push would go to a node it
  cannot see. Single-server is a design assumption in MVP-0 (the step file says so) — it is not enforced.
- **Nothing expires a lease or notices a `lost` task.** A node that dies mid-task leaves the task
  `running` forever, and the node `unreachable` forever (never `offline`). Both are step 12.
- **`Spec.Timeout` is not enforced anywhere.** The executor ignores it (unit D, deviation 7) and the
  server has no timer that turns it into a `Cancel`. Whoever needs it should add it to the scheduler
  tick, where the lease already lives.
- **`/metrics` carries no Podium metrics**, only the Go and process collectors. The registry is local to
  `mux()`; add collectors there.

### Post-acceptance fix: a node's slot is released when its task ends

Found during the MVP-0 final acceptance run, after this step was signed off. `ListNodes` reported a
`running_tasks` that only ever grew: after five short tasks on a `max_tasks: 4` node the API answered
`{"capacity":{"maxTasks":4},"runningTasks":5,"freeSlots":3}` while ground truth was one running task
(one `podium.task` container, `podium_node_running_tasks 1` on the node's `/metrics`). The web UI
rendered it faithfully as "Running 5 / 4".

`Session.running` was populated by `newSession` from `Hello.running_task_ids` and added to by
`Session.reserve` on every `Assign`, and **nothing ever removed an entry**, so
`snapshot().RunningTasks` grew monotonically for the life of a stream. Scheduling was never affected:
`free_slots` is overwritten by every 10s `Heartbeat`, which is the honest number.

The fix:

- `Session.release(taskID) bool` and `Registry.Release(taskID)` (`nodes/registry.go`). `Release` frees
  the slot on whichever session holds the task and is a no-op when none does — replayed terminal
  batches and post-reconnect `Hello` rebuilds both rely on that.
- `logs.Service` gained a `Slots` collaborator (`interface{ Release(taskID string) }`) and calls it
  from `applyStatus` whenever an event implies a **terminal** status, *before* `TransitionTask`: a
  replayed batch has its transition rejected as already applied, and the slot must not be stranded by
  that. `logs` does not import `nodes`.
- `server.New` wires the two together with `logSvc.SetSlots(nodeSvc.Registry())`. It is a setter
  because `nodes.NewService` takes the `Ingestor`, so neither can be a constructor argument of the
  other.

`free_slots` is deliberately untouched by a release: the heartbeat stays authoritative and
`reserve`'s local decrement still prevents over-assignment between heartbeats. So immediately after a
task ends the session reports `running 0, free 3` on a 4-slot node, and the next heartbeat restores
`free 4` — under-assignment for at most 10s, exactly as before the fix.

Covered by `TestFinishedTasksReleaseTheirSlots` in `internal/server/nodes/nodes_test.go`: five tasks
run to completion on one node, `running_tasks` is 1 while each runs and back to 0 after, and
`running_tasks + free_slots <= capacity.max_tasks` holds throughout.

Still open (step 12, not fixed here): a **refused** assignment (`error{retryable:true}`, "node full" /
"node draining") implies no status transition, so the slot `reserve` booked for it is only given back
when the node reconnects and `Hello` rebuilds the set.
