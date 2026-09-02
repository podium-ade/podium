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
- [ ] Server starts with `PODIUM_TRANSPORT=dev`, refuses `PODIUM_DEV_LISTEN=0.0.0.0:8080` with a clear error.
- [ ] `curl -H 'Authorization: Bearer $T' -d '{"spec":{...}}' localhost:8080/podium.v1.TaskService/CreateTask` returns a queued task
      (Connect JSON works without the CLI).
- [ ] A **fake node** test client (in `internal/server/nodes/nodes_test.go`) enrolls, opens a stream, sends Hello, receives
      an `Assign` for a created task, sends provisioning/started/log/exited/finished events, receives `Ack`s;
      the task ends `succeeded`/`failed` per exit code and `StreamTaskEvents` replays every event in order.
- [ ] Re-sending an already-acked event batch does not duplicate rows.
- [ ] Two fake nodes: a task with `labels:[gpu]` goes only to the node advertising `gpu`; a task with no eligible node stays `queued`.
- [ ] Node stream disconnect marks the node `unreachable`; reconnect with `Hello` marks it `online` again.
- [ ] `/readyz` returns 503 when Postgres is down, 200 when up.
- [ ] No `Assign` is ever logged unredacted (grep test for the `RedactForLog` helper usage or a lint rule).

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
_(fill in when done)_
