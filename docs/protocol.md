# Podium node ↔ server protocol

The wire contract lives in `proto/podium/v1/`. Generated Go lands in
`internal/proto/podium/v1` (package `podiumv1`) with Connect clients and handlers in
`internal/proto/podium/v1/podiumv1connect`. Everything below describes `podium.v1`.

Transport is Connect over HTTP/2 (h2c in dev). Identity is **transport-derived** — a bearer
token today, Tailscale `WhoIs` later — and never travels inside a message.

## Services

| Service | RPCs |
|---|---|
| `NodeService` | `Enroll`, `Stream`, `UploadArtifact` |
| `TaskService` | `CreateTask`, `GetTask`, `ListTasks`, `CancelTask`, `StreamTaskEvents` |
| `NodeAdminService` | `CreateEnrollmentToken`, `ListNodes`, `RekeyNode`, `DrainNode`, `UndrainNode`, `SetNodeSlots`, `DeleteNode` |
| `SecretService` | `SetSecret`, `ListSecrets`, `DeleteSecret` |
| `ArtifactService` | `ListArtifacts`, `GetArtifactURL` |
| `IdentityService` | `WhoAmI` |

`NodeService.UploadArtifact` is **client-streaming and not part of the node stream**: it is
its own HTTP request, so its first message re-presents the node's `node_id` and `node_key`
exactly as `Hello` does, and every message after it is a chunk of the body (1 MB, 512 MB
per artifact). The bytes go node → server → object store; a node never talks to S3, which is
the same "nodes only ever talk to the server" invariant the transport design rests on.

`ListTasks` filters on `status`, `node_id`, `requested_by` and `search`. `search` matches a
task whose ID starts with it or whose image contains it, case-insensitively — a substring
match, not a query language, and `%` and `_` are characters rather than wildcards.

`IdentityService.WhoAmI` answers "who does the server think I am", and it sits behind the
**same identity middleware as everything else** — which is the whole trick. A client that gets
an answer without presenting a credential knows it is on a tailnet and needs no login step; a
client that gets 401 knows it must supply the dev token. The web UI decides whether to prompt
on exactly that.

`WhoAmIResponse` also carries `server_version` and `server_commit`, the build identity of the
process answering. `podium version` prints both and warns when the client and the control plane
are different builds. Fields 5 and 6 — a control plane older than they are answers with them
empty, and the CLI says so rather than printing a blank.

There is one route outside Connect: `GET /artifacts/{artifact_id}` streams an artifact's
bytes through the server, behind the same identity middleware. It is a plain HTTP handler
because a 512 MB artifact has to stream, and a unary Connect response would have to be
buffered whole at both ends.

## Stream lifecycle

```
operator      podium node                       podium server
   |               |                                  |
   |  CreateEnrollmentToken ------------------------> |   (NodeAdminService)
   |  token -------------------------------->|        |
   |               |  Enroll{token, hostname, arch,   |
   |               |         os, cpu, mem, docker}--> |   single-use, atomic
   |               | <-- EnrollResponse{node_id,      |
   |               |         node_key}                |   node_key stored 0600
   |               |                                  |
   |               |======== Stream (bidi) ==========>|   reopened with backoff
   |               |  Hello{node_id, node_key, labels,|
   |               |        capacity, running_task_ids, version}
   |               | <-- HelloAck{tasks: [TaskCheckpoint]}  always the first reply
   |               |                                  |   -> reconcile, mark online
   |               |  Heartbeat{load, free_slots, ...}|   every 10s
   |               | <-- Assign{task_id, lease_id, spec, deadline,
   |               |            resolved_secrets}      |   SENSITIVE: plaintext values
   |               |  TaskEvent{seq: 1..n} ---------> |   batched ~100ms / 64KB
   |               | <-- Ack{task_id, seq}            |   high-water mark
   |               | <-- Cancel{task_id, reason}      |   idempotent
   |               | <-- Drain{}                      |   stop accepting new work
   |               | <-- Slots{max_tasks}             |   one on every stream; 0 = the node's own
```

1. **Enroll** happens once. The token is single-use and time-limited; the server stores only
   its SHA-256, and only the SHA-256 of the returned `node_key`. Both values are `SENSITIVE:
   never log`.
2. **Stream** is one bidirectional stream per node. The first `NodeMessage` **must** be
   `Hello`, within 5s, or the server closes the stream. `Hello` carries the task IDs the node
   still has containers for, which is the server's reconciliation input; `HelloAck` is the
   answer, and is always the first `ServerMessage` on the stream. See **Reconciliation** below.
3. **Heartbeat** every 10s. Missing 3 (≈30s) marks the node `unreachable`; missing 12 (≈120s)
   marks it `offline` and expires its leases.
4. **Assign** hands over a task under a lease. The node must emit a `provisioning` `TaskEvent`
   within 15s or the server revokes the lease and reschedules. `resolved_secrets` carries the
   plaintext of every secret the task's spec referenced, resolved immediately before the push
   and sent to nobody else. The transport is what protects them in flight — WireGuard under
   `tailnet`, and nothing at all under `dev`, which is why `dev` refuses to bind anything but
   loopback and warns at startup when any secret exists.
5. **Cancel** is idempotent and is also how server-computed timeouts arrive: SIGTERM, 30s
   grace, SIGKILL, teardown.
6. **Drain** tells the node to stop accepting work and finish what is running. `Drain{undo:
   true}` lifts it.
7. **Slots** is how many tasks the node may run at once, overriding the `max_tasks` in its own
   configuration. The node enforces its own budget — it rejects an assignment it has no slot
   for — so the number has to reach it and not only the scheduler. Exactly one `Slots` is sent
   on every stream, straight after the `HelloAck`, and `max_tasks: 0` means "your own
   configuration": the control plane holds the override in `nodes.max_tasks_override` and the
   daemon keeps none of it, so an override cleared while a node was disconnected is undone on
   its next connection rather than left in force.
8. A new stream from the same node replaces the old session, which is closed.

## Reconciliation: `Hello` → `HelloAck`

A node that restarts finds its containers still running and has to work out where to resume
each one's output. It cannot work that out by itself, and the reason is worth stating
plainly: **the server commits an event batch and then sends the `Ack`**. A daemon killed in
between has no record of an acknowledgement that did in fact happen. If it resumes from its
own bookmark it re-reads those bytes and re-emits them under fresh sequence numbers, which
`(task_id, seq)` cannot deduplicate, and an operator sees a duplicated line. Resuming from
*sent* rather than *acked* bytes trades the duplicate for a gap, which is worse.

So the control plane answers the question, because it is the only party that knows:

```proto
message HelloAck { repeated TaskCheckpoint tasks = 1; }

message TaskCheckpoint {
  string task_id      = 1;
  bool   adopt        = 2;  // false: the control plane has moved on; tear the container down
  uint64 high_seq     = 3;  // highest seq stored, across events and log chunks
  int64  stdout_offset = 4; // bytes of the container's own stdout the store has committed
  int64  stderr_offset = 5;
}
```

There is one checkpoint per task id the `Hello` reported, in the same order. A node adopting a
container numbers its next event `high_seq + 1` and discards `stdout_offset` / `stderr_offset`
bytes of the log Docker replays to it. A `Cancel` follows for every checkpoint with
`adopt: false`, after the `HelloAck`, so a node reading its stream in order always learns the
verdict before it is told to act on one.

The offsets are **the container's own byte counts, not the length of what was stored**:
redaction rewrites the bytes on their way out, so `sum(length(bytes))` is a different number.
Each `LogChunk` therefore carries `source_offset`, the position in the container's stream that
that chunk ends at, and the server hands back the maximum it holds.

Tasks the store still has on a node that the node did *not* report have lost their containers:
they are requeued (`retry_on_node_loss`) or marked `lost`. Tasks in `scheduled` are the one
exception — an `Assign` may still be in flight — and are left to the 15s provisioning deadline.

## `TaskEvent.seq` — ordering, replay and acks

`seq` is a `uint64` that is **monotonically increasing per `task_id`, starting at 1**. It is
scoped to the task, not to the node, the stream or the lease.

- The node assigns `seq` as it produces events and holds every unacked event in an in-memory
  **replay buffer**.
- The server persists a batch, then sends `Ack{task_id, seq}` where `seq` is the highest
  sequence it has durably stored for that task. The ack is a high-water mark, not a
  per-message receipt.
- On `Ack`, the node drops every buffered event with `seq <= ack.seq`.
- On disconnect the node reopens the stream and **re-sends its whole replay buffer**, in order,
  with the original `seq` values. It never renumbers.
- The server deduplicates on the primary key `(task_id, seq)` (`insert … on conflict do
  nothing`), so replaying an already-stored batch is a no-op. This is what keeps a server
  restart mid-task from leaving a hole in the logs.
- `StreamTaskEvents{task_id, from_seq}` replays stored events with `seq > from_seq` and then
  follows live ones, so a reconnecting CLI or UI resumes exactly where it stopped.

Events for different tasks are independent; there is no global ordering.

## `TaskEvent.kind`

`TaskEventKind` carries all ten canonical values. `kind` and the `payload` oneof are
correlated but not redundant: `provisioning`, `pulling` and `started` have no payload.

| kind | payload | emitted in MVP-0 |
|---|---|---|
| `TASK_EVENT_KIND_PROVISIONING` | — | yes |
| `TASK_EVENT_KIND_PULLING` | — | yes |
| `TASK_EVENT_KIND_STARTED` | — | yes |
| `TASK_EVENT_KIND_LOG` | `LogChunk` | yes |
| `TASK_EVENT_KIND_STEP` | `Step` | yes — a sidecar's lifecycle, `name: "sidecar/<name>"`, `status: started \| ready \| failed` |
| `TASK_EVENT_KIND_ARTIFACT` | `ArtifactRef{artifact_id, name, object_key, size_bytes, content_type}` | yes — after the upload, see below |
| `TASK_EVENT_KIND_EXITED` | `Exited{exit_code, oom_killed}` | yes |
| `TASK_EVENT_KIND_FINISHED` | `Finished{exit_code, usage}` | yes |
| `TASK_EVENT_KIND_ERROR` | `Error{message, retryable, aborts_run}` | yes |
| `TASK_EVENT_KIND_MESSAGE` | `Message{type, text, attachments}` | yes — `podium-runner message`, see below |

An **`artifact` event is only ever emitted after `UploadArtifact` has returned**, so it
always names bytes that are already durable — never an upload in flight. Artifact events
come before `exited`: the run waits for its uploads, and for the sweep of
`/workspace/.podium/artifacts/`, before it emits the exit. An upload that fails becomes an
`error` event with `aborts_run: false`, which implies no status transition, so a task that
did its job still succeeds.

`Step` is also what the runner's event socket forwards for any kind a node does not
otherwise understand (see [runner-events.md](runner-events.md)).

**`Message` is the first payload whose content is authored by the task itself.** Everything
else on this wire is something Podium observed; a message is something a task said. It is
emitted by `podium-runner message`, it moves no status and the server stores it and nothing
else. `type` is an open string, canonically `progress` (a note that the next one may
supersede) or `final` (the answer); the agent runtime adds a third, `accounting`, whose text is
JSON the conductor reads for a turn's cost and turn count and never relays. `attachments` are
artifact **names**, not paths, and
nobody checks that they exist — when the message arrives the artifact may still be uploading.
`text` is capped at 32 KiB by the runner, which refuses a longer one rather than truncating.

Because a task container is untrusted and its event socket is world-writable inside the
container, a task can forge or flood `message` events. **Every reader must treat the text as
untrusted content**, exactly as it treats a log line — see
[security.md](security.md#3-a-task-container--untrusted).

`LogChunk.stream` keeps `STREAM_STDOUT`, `STREAM_STDERR` and `STREAM_SIDECAR`;
`sidecar_name` is set only for the third and names the sidecar the output came from.

A task with sidecars produces `step` and sidecar `log` events **before** `started`: the
sidecars are brought up and waited for during provisioning, and the task container is not
created until they are ready. `started` still means "the task command has been forked", and
`exited`/`finished` are still the last two events of a successful run — a sidecar's log
stream is stopped before them.

## Status mapping

The server derives `tasks.status` from event kinds:

| event | transition |
|---|---|
| `provisioning` | `scheduled → provisioning` |
| `started` | `provisioning → running`, sets `started_at` |
| `exited` with `oom_killed` | no transition; the following terminal transition sets `failure_reason = "oom"` |
| `finished` | `running → succeeded` (exit 0) or `failed`, sets `finished_at`, `exit_code`, `usage` |
| `error` with `aborts_run = false` | no transition — the run survived it and still owes an exit code |
| `error` with `aborts_run`, `retryable = false` | `→ failed`, sets `failure_reason` and `finished_at` |
| `error` with `aborts_run`, `retryable` | `→ queued` when `attempts < max_attempts`, else `→ failed` with the message as `failure_reason` |

`aborts_run` is the node saying it has stopped working on the task: no `exited` or
`finished` follows. **An error that aborts a run always resolves the task** — another
attempt, or a terminal status. It is never left in a status no node is working on, which is
what the control plane guarantees and what a `retryable` error used to break.

`log`, `pulling`, `exited`, `step`, `artifact` and `message` are stored and imply no
transition.

`cancelled` comes from `CancelTask` plus the node's `exited`; `lost` is set by the server when
a node vanishes mid-run and the retry policy says not to requeue.

## Redaction

Two separate things, both required.

**`Assign` must never be logged directly.** It carries `resolved_secrets`, so every log
statement that touches one goes through `podiumv1.RedactForLog(*Assign) *Assign`
(`internal/proto/podium/v1/redact.go`), which clones it and clears that field.
`internal/server`'s `TestNoAssignIsLoggedUnredacted` walks the tree and fails the build if a
log statement mentions an `Assign` without it.

**Log chunks are redacted on the node.** Before a `log` `TaskEvent` is buffered or sent, the
node replaces every occurrence of every secret value — and its base64 and URL-encoded forms
— with `[redacted:NAME]`, across `stdout`, `stderr` and every sidecar stream, holding back
up to 256 trailing bytes so a value straddling a chunk boundary is still caught. A redacted
value therefore never crosses the wire, which also means `podium run` shows the marker
rather than the value: there is one log path and the value does not travel it. This is
best-effort defence in depth, not a guarantee — see
[task-spec.md](task-spec.md#redaction).
