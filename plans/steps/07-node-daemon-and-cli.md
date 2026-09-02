# Step 07 — `podium-node` daemon and `podium` CLI (M1 acceptance)

**Milestone:** M1 ✔ · **Depends on:** 05, 06 · **Design ref:** §7.1 Startup, §7.2, §2 binaries

## Goal
Wire the Docker executor to the server stream, add the CLI, and pass the M1 acceptance test:
`podium run --image alpine:3 -- sh -c 'echo hi; sleep 5; exit 3'` streams logs live from a real node
and reports exit code 3 — on one laptop, dev transport, no Tailscale.

## In scope

### `podium-node` (`cmd/podium-node`, `internal/node`)
- Config: load `/etc/podium/node.yaml` (or `--config`), overlay `PODIUM_NODE_*` env, validate. Defaults per index.
- Startup sequence (design §7.1): docker executor `New` (fails fast on cgroup v1/old API) → identity: if
  `data_dir/identity.json` missing, `Enroll` with `enroll_token` (dev transport: also send dev token) and write it `0600`;
  else load → open `Stream` → send `Hello{labels, capacity, running_task_ids: ListOwned()}` → heartbeat ticker (10s).
- Transport dialer (`internal/transport/dev`): `Dial(ctx, serverURL)` → `http.Client` that adds `Authorization: Bearer <dev_token>`;
  h2c for plaintext HTTP/2 bidi streaming.
- Stream loop (`internal/node/stream.go`):
  - Reconnect with exponential backoff (1s → 30s cap, jitter) on any error; re-send `Hello` each time.
  - `Assign` → if `freeSlots == 0` reply with `TaskEvent{kind:error, message:"node full", retryable:true}`; else spawn a task goroutine.
  - `Cancel` → `executor.Cancel`. `Drain` → set draining, stop accepting, exit when running == 0 (used in step 12).
- Task goroutine: `executor.Run` with an events channel → **batcher**: flush every 100ms or 64KB into one or more `TaskEvent`
  messages; append each to a per-task **replay buffer** (ring, 8 MB cap) until `Ack.seq` ≥ its seq. On reconnect, replay unacked
  events before anything else. If the buffer overflows, drop oldest `log` chunks first and emit an `error{retryable:false,
  message:"log buffer overflow"}` marker (never drop `exited`/`finished`).
- After `finished` is acked → `executor.Teardown(keepWorkspace)`.
- Heartbeat payload: running count, free slots, host CPU%/mem% (gopsutil), disk free of `data_dir`.
- `/healthz` `/readyz` `/metrics` on `127.0.0.1:9091` (configurable).

### `podium` CLI (`cmd/podium`, `internal/cli`)
- Config: `~/.config/podium/config.yaml` `{server, token}` + `PODIUM_SERVER`, `PODIUM_TOKEN` env + flags. Dev only for now.
- Commands:
  - `podium run [--image IMG] [--label L]... [--env K=V]... [--timeout 1h] [--spec file.yaml] [--detach] -- CMD...`
    Creates the task; unless `--detach`, follows `StreamTaskEvents`, prints stdout/stderr chunks to the matching local streams,
    prints lifecycle lines to stderr in dim text (`→ scheduled on node_…`, `→ running`, `→ finished exit 3 in 5.2s`),
    exits with the task's exit code (or 125 on infra failure, 130 on cancel).
  - `podium tasks [--status s] [--limit n]` table; `podium task get ID` (JSON with `--json`); `podium task cancel ID`.
  - `podium logs [-f] [--from-seq n] ID`.
  - `podium nodes` table (name, status, labels, running/max, last heartbeat).
  - `podium node enroll-token [--label L]... [--ttl 1h]` prints a token (admin).
  - `podium version`.
- Exit codes and streams are contractual (scripts will rely on them); document in `docs/cli.md`.

### End-to-end test (`test/e2e/m1_test.go`, `//go:build e2e`)
- Starts Postgres (testcontainers), `podium-server` (dev transport, random port), `podium-node` (dev transport, temp data dir,
  host Docker), then runs the CLI as a subprocess for the acceptance command. Asserts: output contains `hi`, exit code 3,
  `podium nodes` shows one online node, `podium task get` shows `failed` with `exit_code: 3`, and no `podium-*` containers/networks remain.
- Second scenario: kill the node's network path mid-run (stop the server for 5s, restart) → the CLI output has no gap and the task still finishes.

## Out of scope
- Sidecars/limits/secrets/artifacts (08–10), tailnet (11), leases/reconciliation beyond "reconnect and replay" (12), UI (13).

## Acceptance checklist
- [x] `make e2e` passes locally against Docker Desktop — 5/5 scenarios green on darwin/arm64. *CI on Linux is unverified: the repo has no CI workflow and no Linux runner yet.*
- [x] Killing `podium-node` with SIGTERM during a task: the task container keeps running; on restart the node re-adopts it via
      `ListOwned` + `Hello.running_task_ids` and continues streaming (the server side may still be naive; the node must at least not orphan it).
      *`TestNodeRestartAdoptsItsContainers`; needed a new `docker.Adopt` — see Hand-off notes, deviation 1.*
- [x] Replay buffer test: unacked events survive a reconnect and arrive exactly once server-side (store PK dedupes; assert no `error` markers).
      *`TestServerRestartMidTaskLeavesNoGap` + `requireEventsExactlyOnce`, which reads `task_events` ∪ `task_log_chunks` straight out of Postgres.*
- [x] CLI `run` returns the task's exit code; `--detach` prints the task ID and returns 0.
- [x] Node refuses to start if `data_dir` is not writable or `server` is empty, with actionable messages. *`TestValidateRefusesToStart`.*

## Verification
```sh
make lint test build      # 0 issues, all packages ok, 4 binaries
go test -tags integration ./... -count=1
make e2e                  # 5/5 scenarios
```
Plus a live stack on non-default ports (5432 and 8080 are taken on the build host by
unrelated tunnels): `PODIUM_PG_PORT=55432`, `PODIUM_DEV_LISTEN=127.0.0.1:18080`.

```sh
TOKEN=$(./bin/podium --server http://127.0.0.1:18080 --token devtoken node enroll-token --label demo)
./bin/podium --server http://127.0.0.1:18080 --token devtoken nodes
./bin/podium --server http://127.0.0.1:18080 --token devtoken run --image alpine:3 -- \
  sh -c 'for i in 1 2 3 4 5; do echo tick $i; sleep 1; done; exit 3'; echo "exit=$?"
```
The ticks arrive one per second, timestamped at the terminal, and the CLI exits 3.

## Notes
- The CLI must never require Docker; it only talks to the server.
- Keep `internal/node` free of server-side packages; the only shared code is `internal/proto`, `pkg/spec`, `internal/transport`.

## Hand-off notes

Unit F of the MVP-0 track: the node daemon, the CLI and the end-to-end suite. Everything in
step 07 is in scope for MVP-0 (both e2e scenarios, the replay buffer); sidecars/limits/
secrets/artifacts (08-10), tailnet (11), leases and reconciliation beyond reconnect-and-
replay (12) and the UI (13) are not. Everything below is what **unit G (the web UI)** and
any later step code against.

### What exists

```
internal/node/config.go        Config, DefaultConfig, LoadConfig, Validate
internal/node/identity.go      Identity, LoadIdentity, SaveIdentity, enroll
internal/node/node.go          Node, New, task registry, slot accounting, adoptOwned
internal/node/stream.go        Run (reconnect loop), session, Hello, heartbeat, flush, Assign/Ack/Cancel/Drain
internal/node/task.go          startTask, adoptTask, execute (the coalescer), push, rejectAssign
internal/node/buffer.go        buffer: one seq space + replay buffer + the on-disk bookmark
internal/node/convert.go       docker.Event -> podiumv1.TaskEvent
internal/node/host.go          HostFacts, sampleLoad (gopsutil)
internal/node/health.go        /healthz /readyz /metrics on 127.0.0.1:9091
internal/node/docker/adopt.go  Adopt (new; see deviation 1)
internal/transport/dev/client.go  NewClient (HTTP/1.1) and NewStreamClient (h2c)
internal/cli/{root,config,client,run,follow,tasks,logs,nodes}.go
cmd/podium-node/main.go, cmd/podium/main.go
docs/cli.md                    exit codes and stream contract
test/e2e/{harness_test.go,m1_test.go}   //go:build e2e, driven by `make e2e`
```

### Running the whole dev stack (this is the "how do I see it work" recipe)

`5432` and `8080` are held by unrelated tunnels on the build host, so the numbers below are
the alternates that were actually exercised. Substitute the defaults on a clean machine.

```sh
PODIUM_PG_PORT=55432 docker compose -f deploy/docker-compose.dev.yml up -d postgres

PODIUM_TRANSPORT=dev PODIUM_DEV_TOKEN=devtoken PODIUM_DEV_LISTEN=127.0.0.1:18080 \
  PODIUM_DATABASE_URL=postgres://podium:podium@127.0.0.1:55432/podium ./bin/podium-server &

TOKEN=$(./bin/podium --server http://127.0.0.1:18080 --token devtoken node enroll-token --label demo)

PODIUM_NODE_SERVER=http://127.0.0.1:18080 PODIUM_NODE_TRANSPORT=dev \
  PODIUM_NODE_DEV_TOKEN=devtoken PODIUM_NODE_ENROLL_TOKEN=$TOKEN \
  PODIUM_NODE_DATA_DIR=/tmp/podium-node PODIUM_NODE_METRICS_LISTEN=127.0.0.1:19091 \
  ./bin/podium-node &

./bin/podium --server http://127.0.0.1:18080 --token devtoken nodes
./bin/podium --server http://127.0.0.1:18080 --token devtoken run --image alpine:3 -- \
  sh -c 'for i in 1 2 3 4 5; do echo tick $i; sleep 1; done; exit 3'
```

Teardown: `pkill -f bin/podium-node; pkill -f bin/podium-server` then
`docker compose -f deploy/docker-compose.dev.yml down -v`. A clean run leaves no
`podium.task`-labelled container, network or volume behind — check with
`docker ps -a --filter label=podium.task`.

For the UI, `PODIUM_DEV_TOKEN` is the bearer token the browser must send, and the server is
plain HTTP/1.1 for everything unit G touches (see below).

### CLI surface — exact invocations

Global: `--server` and `--token` are persistent flags on the root command and beat
`PODIUM_SERVER`/`PODIUM_TOKEN`, which beat `~/.config/podium/config.yaml`
(`$XDG_CONFIG_HOME` is honoured).

| Command | Notes |
|---|---|
| `podium run [--image I] [--label L]... [--env K=V]... [--timeout D] [--working-dir P] [--spec F] [--detach] -- CMD...` | follows the task; exits with its exit code |
| `podium tasks [--status S]... [--limit N] [--node ID]` | table, newest first |
| `podium task get ID [--json]` | `--json` is protojson, i.e. `exitCode`, `nodeId`, `TASK_STATUS_FAILED` |
| `podium task cancel ID [--reason R]` | returns immediately |
| `podium logs [-f] [--from-seq N] ID` | `--from-seq` exclusive |
| `podium nodes` | name, id, status, labels, running/max, heartbeat age |
| `podium node enroll-token [--label L]... [--ttl D]` | token on stdout, nothing else |
| `podium version` | |

**Exit codes and streams are contractual and documented in `docs/cli.md`.** The short
version: `run` exits with the task's own code, `125` for any infra failure, `130` for a
cancelled task, `1` for a bad invocation. The task's stdout/stderr go to the CLI's
stdout/stderr; every Podium progress line goes to stderr, prefixed `→` and dimmed only when
stderr is a terminal. `podium node enroll-token` therefore works inside `$(...)`.

`podium run`'s first progress line is `→ task task_01j…`, which is how the e2e suite (and
any script) learns the task ID without `--detach`.

### `StreamTaskEvents` — what unit G needs to know, learned the hard way

- `from_seq` is **exclusive**. Remember the last seq you rendered and reconnect with it;
  that is exactly-once, no gap and no repeat. The CLI's `follower`
  (`internal/cli/follow.go`) is the reference implementation and is what makes e2e scenario
  2 (server restart mid-task) pass — reuse its shape in TypeScript.
- **A clean end of stream means the task is finished**, not that the connection dropped.
  The handler returns once the task is terminal *and* everything has been delivered. Treat
  EOF as "done", then `GetTask` for the exit code. Do not poll `GetTask` on a timer.
- An error from the stream is *not* terminal. Reconnect from `lastSeq` with a short
  backoff. The CLI gives up after 90s without progress (`followGrace`) and exits 125.
- A replayed event carries `lease_id: ""`, and `provisioning`/`pulling`/`started` come back
  with the payload oneof **unset**. Do not read `.log`/`.exited`/`.finished` without
  checking the kind first.
- Log bytes arrive as `LogChunk.bytes` — raw bytes, not lines, and a chunk may split a line
  in the middle. Append to a per-stream buffer and split on newlines yourself.
- Ordering is strictly ascending by seq across both kinds of row, so one seq counter per
  task is all the state a renderer needs.

### The node's wire behaviour (what the server sees)

- **h2c is mandatory for `NodeService.Stream`.** `dev.NewStreamClient` builds
  `&http.Transport{Protocols: …}` with `SetUnencryptedHTTP2(true)` and deliberately does
  **not** enable HTTP/1.1, so a misconfiguration fails loudly instead of silently
  downgrading and breaking bidi. `golang.org/x/net/http2/h2c` is still not imported
  anywhere. The CLI uses `dev.NewClient` (plain HTTP/1.1) — **unit G needs nothing
  special**, `fetch` works.
- Two auth layers, both required: `Authorization: Bearer <dev token>` on every HTTP request
  (`Enroll` and `Stream` alike) plus `node_id`+`node_key` inside `Hello`. `node_key` is
  written to `<data_dir>/identity.json` mode 0600 and there is no recovery path.
- `Hello.capacity.max_tasks` is `config.max_tasks` (default 4) and
  `Hello.running_task_ids` is every task the node currently holds a buffer for, which after
  a restart is everything `ListOwned` found. `Heartbeat.free_slots` is
  `max_tasks - len(running)` and drops to 0 while draining.
- One monotonic seq space per task starting at 1, across all kinds. Log chunks are
  **coalesced**: a run of container output becomes one `log` event after 100ms or 64KB,
  whichever comes first. So node seq numbers do not map 1:1 onto executor event numbers,
  and they are assigned once — a replay is byte-identical because it re-sends the very same
  message.
- `Ack{task_id, seq}` drops everything `<= seq` from the buffer. If a send goes out and no
  ack follows within 15s the events are sent again on the same connection
  (`retransmitAfter`), because a failed `Ingest` is never acked and the connection it failed
  on may stay up forever. On reconnect everything unacked is re-sent before anything new.
- Replay buffer: 8 MB per task (`node.ReplayBufferBytes`). On overflow the oldest `log`
  events are dropped — never `exited`, `finished` or `error` — and one
  `error{retryable:false, message:"log buffer overflow"}` is emitted per task. Note that a
  non-retryable error moves the task to **failed** server side, which is the intended
  signal that the record is incomplete.
- Assignment refusals are `TaskEvent{kind: error, retryable: true}` with seq 1 and no
  buffer: `"node full"` when `free_slots == 0`, `"node draining"` after a `Drain`. A
  retryable error causes no status transition, so in MVP-0 the task simply stays
  `scheduled` — nothing reschedules it (step 12).
- Reconnect backoff is 1s → 30s, doubling, jittered down by up to 25%, reset once a session
  has lasted 30s. `Hello` is re-sent on every connection.
- After `finished` is acked the node calls `executor.Teardown(ctx, taskID, false)`. The slot
  is held until then, so a server that never acks holds a slot — that is deliberate: the
  node is holding undelivered evidence.

### Node configuration

`/etc/podium/node.yaml` (or `--config`), overlaid by `PODIUM_NODE_*`. **The file is
optional**: a missing default path is not an error, so the MVP-0 acceptance script's
environment-only invocation works as written. An explicitly named `--config` that does not
exist *is* an error.

| Variable | Default | |
|---|---|---|
| `PODIUM_NODE_SERVER` | `http://127.0.0.1:8080` | must be an http(s) URL |
| `PODIUM_NODE_TRANSPORT` | `dev` | `tailnet`/`host` error with "step 11" |
| `PODIUM_NODE_DEV_TOKEN` | — | required for `dev` |
| `PODIUM_NODE_ENROLL_TOKEN` | — | first run only |
| `PODIUM_NODE_DATA_DIR` | `/var/lib/podium-node` | created and write-probed at startup |
| `PODIUM_NODE_LABELS` | — | comma separated |
| `PODIUM_NODE_MAX_TASKS` | `4` | 0 is refused, not silently accepted |
| `PODIUM_NODE_METRICS_LISTEN` | `127.0.0.1:9091` | |
| `PODIUM_NODE_IMAGE_CACHE_HIGH_WATERMARK` | `0.80` | recorded, unused until step 12 |
| `PODIUM_NODE_DOCKER_HOST` | — | overrides the engine endpoint |

`/healthz` is process liveness, `/readyz` is 200 only while the control plane stream is up,
`/metrics` carries the Go and process collectors plus `podium_node_running_tasks`,
`podium_node_free_slots` and `podium_node_stream_connected`.

### Deviations, and why

1. **`docker.Adopt` was added to unit D's package** (`internal/node/docker/adopt.go`, plus
   `newEmitterAt`, `streamLogsFrom` with per-stream byte offsets, and the accessors
   `ServerVersion()` / `TaskDir()`). The checklist wants a restarted node to re-adopt its
   containers "and continue streaming", and `Run` cannot: it creates the container. `Adopt`
   re-attaches to `podium-<task_id>`, replays the container's output from the byte offset
   the server last acked, and emits `exited` + `finished`. Without it a SIGTERM'd node
   leaves the task `running` forever, which is precisely orphaning it.
2. **The node keeps an on-disk bookmark per task**, `<data_dir>/tasks/<task_id>/
   node-state.json`, holding `{task_id, lease_id, high_seq, acked_stdout, acked_stderr}`.
   An adopted run continues the seq space at `high_seq+1` — reusing a seq would be silently
   swallowed by the server's `on conflict do nothing` and the task would never leave
   `running` — and skips `acked_stdout`/`acked_stderr` bytes of the container's log, because
   Docker replays the whole log on every attach and offers no cursor. Result: an adopted
   task's output is complete and unrepeated (asserted in `TestNodeRestartAdoptsItsContainers`).
   If the bookmark is unreadable the seq space restarts at `1<<20` (`adoptSeqFloor`) — a gap
   is recoverable, a collision is not. `Teardown` removes the bookmark with the task dir.
3. **SIGTERM to `podium-node` never cancels a run's context.** Cancelling `executor.Run`'s
   context makes it tear its own container down (unit D's error path), which is the opposite
   of what the checklist wants. Task runs use `context.Background()` and the process simply
   exits; the containers are adopted by the next incarnation. `Drain` is the graceful path:
   stop accepting, exit when the last task finishes.
4. **Log chunks are coalesced before they get a seq** (100ms / 64KB), so the node's seq
   numbers are its own, not the executor's. This is what the step file's "flush every 100ms
   or 64KB into one or more TaskEvent messages" means in practice, and it keeps a chatty
   task from costing one Postgres row per `write(2)`.
5. **`podium logs` without `-f` stops after 1s of silence** when the task is still running.
   The server has no non-following read: `StreamTaskEvents` follows until the task is
   terminal. A terminal task needs no timeout, because the stream ends by itself.
6. **`ExitInfra`=125 and `ExitCancelled`=130 shadow those task exit codes.** A task that
   genuinely exits 125 is indistinguishable from an infra failure at the shell. Documented
   in `docs/cli.md`; use `task get --json` when it matters.
7. **`make e2e` builds its own binaries** into a temp dir rather than depending on `make
   build`, so a stale `bin/` can never make a green run meaningless. Ports are all
   ephemeral (`127.0.0.1:0` probed then released), so nothing assumes 8080 or 5432 is free.
   The server runs **in process** (the tests import `internal/server`) because scenario 2
   has to stop and restart it on the same address; the node and the CLI are real
   subprocesses because the tests SIGTERM one and read the other's exit status.
8. **`go get github.com/shirou/gopsutil/v4@v4.26.8`** promoted gopsutil from indirect to
   direct and pulled `ebitengine/purego 0.10.1 → 0.10.2`. `go mod tidy` was run (single unit
   in the tree); nothing else moved.

### Gotchas worth remembering

- **One node per Docker engine.** `ListOwned` filters by the `podium.task` label across the
  whole engine, so two `podium-node` processes on one machine adopt each other's
  containers. The e2e suite therefore runs strictly sequentially and cleans the engine
  between tests. Enforcing this is nobody's job yet.
- `connect.BidiStreamForClient.Send` is not safe for concurrent use: exactly one goroutine
  (the session loop) sends Hello, heartbeats, events and refusals. Task goroutines only
  append to their buffer and nudge a 1-deep `wake` channel.
- The server's own free-slot bookkeeping decrements on `Assign` (unit E's `Session.reserve`)
  and is overwritten by the next `Heartbeat`, so `Heartbeat.free_slots` has to stay honest
  or the scheduler will over- or under-assign for up to 10s.
- Docker's `ContainerLogs` with `Follow: true` on an **already stopped** container returns
  what it has and ends, which is why `Adopt` attaches unconditionally rather than only when
  the container is running — that is how a container that exited while no daemon was
  attached still delivers its tail.
- `podium task cancel` of a command that does not trap SIGTERM costs the full 30s grace and
  ends in exit 137, because with no runner the task command is PID 1 and the kernel drops a
  default-disposition SIGTERM. The e2e's cancel test traps TERM to stay fast; a UI must not
  block on the cancel response.
- `podium run`'s `→ scheduled on node_…` line costs one extra `GetTask`; it is cosmetic and
  degrades to "a node" if that call fails.

### Open problems

- **`Spec.Timeout` is still enforced nowhere.** The executor ignores it (unit D), the server
  has no timer (unit E) and the node does not either. A task that hangs runs until someone
  cancels it.
- **Nothing reschedules a refused assignment.** `error{retryable:true}` causes no status
  transition, so a task refused by a full node stays `scheduled` forever. Step 12.
- **A node that loses the server forever holds its slots**, because a finished task's slot
  is only freed once its last event is acked. Bounded by the 8 MB replay buffer, not by
  time.
- **Linux CI is unverified.** `make e2e` has only been run against Docker Desktop 29.4.3 on
  darwin/arm64; there is no CI workflow in the repo to run it anywhere else.
- **`/readyz` on the node does not probe Docker.** The engine is checked once, at startup;
  after that readiness only tracks the control plane stream.
