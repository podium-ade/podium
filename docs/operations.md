# Operations

Running Podium once it is running: what to watch, what to back up, how to upgrade, and what to
do when something is wrong.

---

## Health endpoints

Both daemons serve the same three, unauthenticated, so a probe reports what the process reports
and not what a credential allows.

| | `podium-server` | `podium-node` | `podium-agent` |
|---|---|---|---|
| where | the API listener (`127.0.0.1:8080` under `local`; port 443 of the tailnet device otherwise) | `PODIUM_NODE_METRICS_LISTEN`, default `127.0.0.1:9091` | `PODIUM_AGENT_LISTEN`, default `127.0.0.1:8090` |
| `/healthz` | 200 while the process is up | 200 while the process is up | 200 while the process is up |
| `/readyz` | 200 `ok`; **503** with `postgres unreachable`, `object store unreachable: ...`, or (tailnet) a transport problem such as a node key within 30 days of expiring | 200 only while the **stream to the control plane is up**; 503 with `control plane stream is down` | 200 `ok, podium as <login>`; **503** with `agent database unreachable`, `podium api unreachable` or `memory unreachable` |
| `/metrics` | Prometheus | Prometheus | Prometheus, with `podium_agent_*` |

Three things about `/readyz` that will confuse you otherwise:

- **The node's `/readyz` does not probe Docker.** The engine is checked once, at startup. After
  that readiness tracks only the control plane stream, so a node whose Docker daemon has died
  reports ready until a task fails.
- **The server's `/readyz` failing on a near-expiry Tailscale node key is deliberate.** Tagged
  devices do not expire, so in a correctly tagged install it never fires; it exists to make an
  untagged deployment fail visibly at 60 days instead of silently at 90.
- **The conductor's `/readyz` calls the Podium API's `WhoAmI`.** So it is 503 whenever the
  control plane is down, which is correct — a conductor that cannot submit a task cannot run a
  turn — but it means a server restart shows up as an unready conductor for a few seconds. It
  does not probe Slack: the library reconnects on its own and a disconnect is logged and counted
  rather than made a readiness failure.
- **`memory unreachable` on the conductor does not stop turns.** It is a readiness failure
  because every turn's prompt names the shared memory, but a turn still runs, still answers and
  still succeeds without it. Do not gate anything that restarts the conductor on this.

---

## Metrics

**Read this before you build a dashboard: the control plane exposes no Podium-specific metrics
at all.** `podium-server`'s `/metrics` carries the Go runtime and process collectors and nothing
else. There is no task counter, no schedule latency, no queue depth, no ingest rate. The
Prometheus registry is local to the server's mux and nothing registers into it.

That is a gap, not a design position, and it is the largest one in this document. Until it is
filled, the control plane's state has to be read out of Postgres.

### What is actually emitted

`podium-node`, on `PODIUM_NODE_METRICS_LISTEN`:

| metric | type | meaning |
|---|---|---|
| `podium_node_running_tasks` | gauge | tasks this node is currently running |
| `podium_node_free_slots` | gauge | task slots this node is advertising to the scheduler. Drops to 0 while draining, and while the `data_dir` filesystem is above `image_cache_high_watermark` |
| `podium_node_stream_connected` | gauge | 1 while the node holds an open stream to the control plane |

Both daemons additionally serve the standard `go_*` and `process_*` collectors —
`go_goroutines`, `go_memstats_*`, `process_cpu_seconds_total`, `process_resident_memory_bytes`
and the rest.

**There is no `deploy/grafana/` dashboard.** Shipping one would mean shipping panels for metrics
that are not emitted, which is worse than shipping nothing.

### Alerts worth setting today

With three gauges there are only three honest alerts:

```promql
# A worker has lost the control plane.
podium_node_stream_connected == 0

# A worker is full or refusing work — draining, or out of disk for images.
podium_node_free_slots == 0

# A worker has stopped reporting at all (the scrape itself failing).
up{job="podium-node"} == 0
```

Everything else — queued tasks, failure rates, schedule latency — has to come from SQL:

```sql
-- Queued tasks and why the scheduler is skipping them.
select queued_reason, count(*) from tasks where status = 'queued' group by 1;

-- Outcomes in the last hour. `lost` is not `failed`: nothing about a lost task went wrong.
select status, count(*) from tasks where created_at > now() - interval '1 hour' group by 1;

-- How long tasks are waiting for a node.
select percentile_disc(0.5)  within group (order by scheduled_at - created_at) as p50,
       percentile_disc(0.95) within group (order by scheduled_at - created_at) as p95
from tasks where scheduled_at is not null and created_at > now() - interval '1 hour';

-- Hot log storage.
select pg_size_pretty(pg_total_relation_size('task_log_chunks'));
```

---

## Backup and restore

Two things are the only copy of something. Back up both, and understand that they are useless
apart.

### Postgres

```sh
docker compose exec -T postgres \
  pg_dump -U podium -Fc podium > podium-$(date -u +%Y%m%dT%H%M%SZ).dump
```

`pg_dump` takes a snapshot, so nothing has to stop.

If you run `podium-agent`, `podium_agent` is a **second database on the same server** and needs
its own dump:

```sh
docker compose exec -T postgres \
  pg_dump -U podium -Fc podium_agent > podium-agent-$(date -u +%Y%m%dT%H%M%SZ).dump
```

It is the lower-value of the two. It holds session and turn records — which conversation ran
which playbook, which task answered it, what the answer was — and nothing that cannot be
reconstructed by asking again: the conversations themselves live in Slack. Losing it costs
history and the relay's exactly-once ledger, not work.

If you run the agents' shared memory, `podium_memory` is a **third database** and needs its own
dump — and this one is the only copy of what it holds:

```sh
docker compose exec -T postgres \
  pg_dump -U podium -Fc podium_memory > podium-memory-$(date -u +%Y%m%dT%H%M%SZ).dump
```

Unlike `podium_agent`, this cannot be reconstructed by asking again. It is every durable fact
the agents have extracted about the organisation, and the extraction cost provider tokens.
There is no retention and no pruning: it only grows. Treat it like the object store.

```sh
docker compose exec -T postgres \
  pg_restore -U podium -d podium --clean --if-exists < podium.dump
```

### The master key

`master.key` is the AES-256 key every stored secret is encrypted under. **A database backup
without it is a backup of unreadable secrets, and there is no recovery path.**

Copy it somewhere that is not the control plane, and — if the point of the backup is surviving
the loss of that machine — not the same place as the database dump.

```sh
# Rotating, which is the only safe way to replace one:
podium-server gen-master-key --out /etc/podium/master.key.new
PODIUM_DATABASE_URL=... podium-server rotate-master-key \
  --old /etc/podium/master.key --new /etc/podium/master.key.new
# then point PODIUM_MASTER_KEY_FILE at the new file and restart
```

Every row is re-encrypted in a single transaction, so the table is never half under one key.
Afterwards the old key decrypts nothing. A rotation stopped part way is visible as rows with
different `key_id`s in `podium secret ls` and in the UI.

### The object store

```sh
mc alias set podium http://127.0.0.1:9000 "$PODIUM_S3_ACCESS_KEY" "$PODIUM_S3_SECRET_KEY"
mc mirror --overwrite podium/podium ./podium-bucket-backup
```

Once a finished task's chunks have been pruned out of Postgres, the objects in that bucket are
the **only** copy of its log. See [`storage.md`](storage.md).

### Restoring

Restore is a whole-deployment operation: Postgres, the master key and the bucket together.
Restoring one without the others gives you dangling artifact rows, or secrets nothing can
decrypt. There is no partial restore and no point-in-time story beyond what your Postgres gives
you.

Node identities survive independently — a worker's `identity.json` is on the worker. If you
restore a database that predates a node's enrollment, that node reconnects and is told its key
is unknown, once per backoff, for ever. Re-enroll it.

---

## Log retention

| knob | default | |
|---|---|---|
| `PODIUM_LOG_ROLLUP_INTERVAL` | `1m` | how often finished tasks' logs are swept into the object store |
| `PODIUM_LOG_ROLLUP_SETTLE` | `30s` | how long after a task goes terminal before it is eligible |
| `PODIUM_LOG_PRUNE_INTERVAL` | `1h` | how often rolled-up chunks are deleted from Postgres |
| `PODIUM_LOG_CHUNK_GRACE` | `24h` | how long chunks survive after roll-up — "logs stay fast for N" |

**With no object store configured, nothing is rolled up and nothing is pruned**, so
`task_log_chunks` grows for ever. That is the single most likely way a Podium deployment runs
out of disk.

An unparseable duration is ignored rather than defaulted to zero: a typo must not silently
delete a day of retention.

Everything except log chunks grows without bound: tasks, events, artifacts and audit rows are
never deleted by anything. A retention job is work nobody has done.

---

## Draining and taking a worker out of service

`draining` is a **standing instruction**, separate from status. It survives a restart of the
node and of the server, so a node can read `draining: true, status: OFFLINE`.

```sh
podium node drain worker-3      # finishes what it has, takes nothing new
podium nodes                    # wait for RUNNING to reach 0
podium node undrain worker-3    # back in the pool
```

A node started with `--exit-on-drain` (or `exit_on_drain: true`) **exits 0** once the drain is
requested and its last task finishes. systemd treats that as success and restarts it — which is
what makes the binary swap below work.

```sh
podium node rm worker-3         # once it is drained and idle
```

`DeleteNode` refuses twice over, with different messages: the node is online and not drained
(offer `--force`, or drain first), or tasks are still running on it (never forceable).

**`podium node rm` does not stop the daemon.** A removed node whose `identity.json` survives
reconnects for ever and is told its key is unknown, once per backoff. Stop the daemon yourself.
There is no revocation push.

### Turning a worker down without taking it out

A drain is all or nothing. When a machine is merely too busy — it is also somebody's desktop,
or it shares a disk with something that matters — change its slot count instead:

```sh
podium node slots worker-3 2    # two tasks at once, whatever its max_tasks says
podium nodes                    # RUNNING/MAX reads 1/2*, the * meaning "set from here"
podium node slots worker-3 0    # back to its own configuration
```

Like `draining`, the number is a standing instruction on the node's row: it survives both
daemons restarting and can be set on a node that is offline right now. Lowering it takes
nothing down — the tasks already on the node finish, and it accepts no more until enough have.
It also raises: a node whose file says 4 runs 8 when told to, because the count is sent to the
node and not merely applied to the scheduler's arithmetic.

---

## Upgrading

Podium is `v0.x`. **Assume a control plane and its workers must be the same release**;
`podium version` prints both and warns when they differ.

```
$ podium version
podium v0.3.1 (a1b2c3d)
server v0.4.0 (e4f5g6h)
→ version skew: client v0.3.1, control plane v0.4.0. The wire is only guaranteed between
  matching releases; upgrade whichever is older.
```

### Order

1. **Postgres first**, if its image is changing. It is `pgvector/pgvector:pg16` — Postgres 16,
   the same major version as the Alpine-based image earlier releases ran, so an existing data
   volume keeps working and all that is added is the `pgvector` extension. Verified: a volume
   initialised by the Alpine image starts under this one with its data intact.
2. **The control plane next.** Schema migrations run on start and are additive and idempotent;
   a migration is applied under a `pg_advisory_lock`, so several servers starting at once is
   safe. There is no down-migration and no rollback — take the Postgres backup first.
3. **Then the shared memory** (`hindsight`), after `postgres` and before `agent`. It runs its
   own migrations at startup and needs the `vector` extension in `podium_memory` — see the
   recipe below if the volume predates it.
4. **Then the conductor** (`agent`).
5. **Then the workers**, one at a time. A worker upgrade is also what starts mapping
   `host.docker.internal` into a task container, which is what lets a turn reach the shared
   memory at all — an old node runs turns whose `recall` calls simply fail.
6. **Then the CLI** on whatever submits tasks.

A restart of the control plane does not lose a running task: the node buffers its events and
replays anything unacked when the stream comes back, so `podium logs -f` resumes with no gap and
no repeat.

### Upgrading a worker

```sh
# On the worker. Downloads, verifies against the release's checksums.txt, drains, swaps,
# restarts, undrains — in that order, and nothing on the machine changes until the download
# has been verified.
sudo podium-node upgrade v0.4.0
```

If the drain cannot be done from the worker — a tailnet node with its own embedded Tailscale
device has no credential to lend — drain from the control plane instead and skip it:

```sh
podium node drain worker-3
# wait for `podium nodes` to show 0 running
sudo podium-node upgrade v0.4.0 --drain=false
podium node undrain worker-3
```

Re-running `deploy/install-node.sh` does the same job for the binary and the unit, without the
drain.

> **Partly verified.** `podium-node upgrade` has never been run against two real published
> releases, because there has never been a release. The download, checksum verification,
> extraction, atomic swap and the drain → wait → swap → undrain sequence have all been
> exercised against a local release server and a live control plane. The `systemctl restart`
> has not — the build machine is macOS.

### Upgrading with compose

```sh
cd deploy
PODIUM_IMAGE_TAG=v0.4.0 docker compose pull
PODIUM_IMAGE_TAG=v0.4.0 docker compose up -d --wait
```

Pin `PODIUM_IMAGE_TAG` in `.env` rather than relying on `latest`, which moves.

---

### Adding the agent and memory databases to an existing deployment

`deploy/postgres/init.sql` creates `podium_agent` and `podium_memory`, and the `vector`
extension in the second — but Postgres runs init scripts **only when the data directory is
empty**. A volume created before those databases existed never sees the script, and the
conductor's `/readyz` and the memory service's logs both say so. Create them by hand, once:

```sh
cd deploy
docker compose exec -T postgres createdb -U podium podium_agent
docker compose exec -T postgres createdb -U podium podium_memory
docker compose exec -T postgres psql -U podium -d podium_memory \
  -c 'create extension if not exists vector'
```

`createdb` on a database that already exists is an error and nothing else; it is safe to run.

The dev compose file, whose container has a fixed name:

```sh
docker exec podium-dev-postgres createdb -U podium podium_agent
docker exec podium-dev-postgres createdb -U podium podium_memory
docker exec podium-dev-postgres psql -U podium -d podium_memory \
  -c 'create extension if not exists vector'
```

The extension has to be in `public` and it has to exist before the memory service migrates.
Given the privilege it would create it itself; given the extension in **another** schema it
would try to `drop extension vector cascade` and recreate it in `public`. Creating it up front
avoids both, and leaves the door open to giving the memory service a non-superuser role later.

Podium's own database and the conductor's stay extension-free: neither one's migrations
reference it.

---

## When something is wrong

### A task is stuck in `queued`

The scheduler records why, and the CLI and UI both show it.

```sh
podium task get TASK_ID
```

| `queued_reason` | what it means |
|---|---|
| `no node is connected to the control plane` | nothing is connected |
| `no online node carries every label this task requires` | placement matches on labels being a **superset** of the task's; the reason names the labels |
| `every node that could run this task is draining` | every candidate is drained |
| `no online node has enough free CPU or memory for this task` | free capacity is below what the task **and its sidecars** need; the reason names the ask |
| `every node that could run this task is full` | every eligible node is at `max_tasks` |

A task the scheduler has never had to skip has no reason, and says so rather than inventing one.

### A task is stuck in `scheduled`

A node took the assignment and has 15 seconds to say `provisioning`. Past that the control plane
cancels and requeues it, or fails it with `node did not accept assignment` when its attempts are
spent. If that is happening repeatedly, the node is failing to start containers — look at its
journal.

### A node shows `unreachable` or `offline`

`unreachable` at 30 seconds of heartbeat silence, `offline` at 120. Only `offline` means its
tasks have been written off — requeued if `retry_on_node_loss`, otherwise `lost`.

```sh
journalctl -u podium-node -f
curl -s http://127.0.0.1:9091/readyz    # on the worker
```

If `/readyz` says `control plane stream is down`, it is a networking or credential problem, not
a Docker one. Reconnect backoff is 1s → 30s, doubling, jittered, and reset once a session has
lasted 30 seconds.

### A task ended `lost`

Its node went offline while it was running. **Nothing about the task went wrong** — do not go
looking for a bug in it. Its event stream carries a synthetic `error` event naming the node.
Whether re-running is safe is your call; `retry_on_node_loss: true` in the spec makes Podium
make it for you, and it is off by default because Podium does not know whether your task is
idempotent.

### A task ended `failed` with no exit code

It never ran. `failure_reason` says why:

| `failure_reason` | |
|---|---|
| `oom` | it exceeded `resources.memory_mb`. Swap is pinned to the same number, so this is a kill, not a slowdown |
| `timeout` | it outran `spec.timeout` |
| `secrets: missing secret "X"` | the name does not exist. Failed at admission, before any node saw it |
| `node did not accept assignment` | the 15-second provisioning deadline, with attempts spent |
| `node <name> went offline` | see `lost` above |
| a sidecar message | a readiness probe never passed. The message carries the last 100 lines of that sidecar's log |

### Containers left behind

There should never be any. A clean run leaves nothing:

```sh
docker ps -a --filter label=podium.task
docker network ls --filter name=podium-
docker volume ls --filter name=podium-ws-
```

If something is stranded, it is from a node that was killed mid-task and never came back. A node
that *does* come back adopts what it finds and tears down whatever the control plane has written
off.

### Two nodes on one engine

Don't. At startup `podium-node` claims every container on its engine labelled `podium.task`,
whichever daemon created it, and tears down the ones its own control plane does not recognise.
Two daemons on one engine destroy each other's work, not merely confuse it. Nothing enforces
this.

The same trap catches developers: `make test-integration` and `make e2e` start real
`podium-node` processes against the host's engine. Run either beside a live node and both sides
lose their containers.

### The Agent tab is missing from the web UI

The header only shows it when `WhoAmI` reports `agent_enabled`, which is true when
`PODIUM_AGENT_URL` is set on **`podium-server`** — not on the conductor. Two causes, in order of
likelihood:

1. `PODIUM_AGENT_URL` is unset (or empty) on the server. The conductor being up and healthy
   makes no difference; the server is what mounts the prefix and answers `WhoAmI`. Set
   `PODIUM_AGENT_URL` and `PODIUM_AGENT_TOKEN` — the server refuses to start with a URL and no
   token — and restart it.
2. The control plane predates the field. `podium version` will report the skew; an older server
   sends nothing for `agent_enabled`, proto3 reads that as false, and the tab stays hidden.
   Nothing breaks: `/agent` still resolves and says the conductor is not configured.

```sh
curl -s http://127.0.0.1:8080/podium.v1.IdentityService/WhoAmI \
  -H 'Authorization: Bearer $PODIUM_LOCAL_TOKEN' -H 'Content-Type: application/json' -d '{}'
```

### Agent settings say "podium-agent is not reachable"

The server's proxy could not dial `PODIUM_AGENT_URL`. It answers a Connect `unavailable` (HTTP
503) rather than a bare 502, which is what the page renders. The server's own `/readyz` stays
200 on purpose: a control plane whose bot is down is still a working task runner.

```sh
curl -si http://127.0.0.1:8090/readyz        # the conductor's own, unauthenticated
```

- 503 there means the conductor is up but a dependency is not: its database, or the Podium API's
  `WhoAmI`. The body names which.
- Connection refused means the process is not running, or `PODIUM_AGENT_LISTEN` and
  `PODIUM_AGENT_URL` disagree.
- A 401 on the settings RPCs and nothing in the conductor's log means the two sides hold
  different `PODIUM_AGENT_TOKEN` values.

### The Memory tab says memory is not configured

`PODIUM_AGENT_MEMORY_URL` is empty on the conductor, which is a supported configuration — turns
run with no memory and nothing is retained. Set it and `PODIUM_AGENT_MEMORY_API_KEY` and restart
the conductor. Setting the URL without the key is a startup error naming the key: the memory
service has no authentication until it is given one.

### The conductor's readyz says `memory unreachable`

```sh
curl -si http://127.0.0.1:8888/health           # the memory service's own, unauthenticated
docker compose logs --tail 50 hindsight
```

- **503 from `/health`** means the service is up and its database is not. Check that
  `podium_memory` exists and that the `vector` extension is in it — the recipe is above. A log
  line reading `pgvector extension is required but not installed` is exactly that.
- **Connection refused** means the container is not running, or `PODIUM_AGENT_MEMORY_URL` names
  an address the conductor cannot reach (`http://hindsight:8888` only resolves inside the
  compose network).
- **401 on every memory call**, with the conductor logging `the memory API key is refused`,
  means `PODIUM_AGENT_MEMORY_API_KEY` and the container's
  `HINDSIGHT_API_TENANT_API_KEY` disagree. The compose files build both from the same variable,
  so this is a hand-run container or a `.env` that changed without a restart of both.

Meanwhile turns keep working: `podium_agent_memory_retain_total{result="error"}` counts what was
not remembered, and nothing about it reaches a conversation.

### An agent's `recall` fails inside a turn but the conductor is fine

The conductor and a task container reach the memory service from **different places**. The
conductor uses `PODIUM_AGENT_MEMORY_URL`; the turn uses `PODIUM_AGENT_MEMORY_TASK_URL`, which
lands in the brief as `memory.mcp_url`. See
[`networking.md`](networking.md#reaching-the-shared-memory-from-a-worker) — the usual causes are
a worker on another machine (where `host.docker.internal` is the wrong host) and
`PODIUM_MEMORY_BIND` left on loopback, which the Docker bridge cannot reach.

`/workspace/.podium/artifacts/transcript.jsonl` on the turn's task is where the answer is: the
MCP tool list the SDK saw is in it.

### Two servers on one database

Don't, yet. Node sessions are in memory, so only the server holding a node's stream can assign
to it, cancel on it or drain it — and the second replica's watchdog would see every node as
sessionless and start expiring leases. A leader lock is needed first.

---

## Tuning

Timers live in `internal/server/scheduler/timing.go` and are **not configurable at run time**.
They are here so that you know what the system is doing:

| | default | |
|---|---|---|
| dispatch tick | 500ms | a `pg_notify` on a newly queued task usually beats it |
| watchdog | 5s | the lease sweep and the node health sweep |
| lease TTL | 2m | a fresh assignment's lease |
| provisioning deadline | 15s | how long a node has to say `provisioning` |
| lease grace | 5m | added to `spec.timeout` for a live task's lease |
| unreachable / offline | 30s / 120s | heartbeat silence |
| cancel grace | 60s | before the server writes a cancelled task's status itself |
| claim limit | 50 | dispatch batch size |
| heartbeat | 10s | node → server |
| event batch | 100ms or 64KB | node-side coalescing |
| cancel grace (container) | 30s | SIGTERM → SIGKILL inside the node |

`PODIUM_TEST_FAST_TIMERS=1` divides every one of them by ten. It is for the test suites; the
server logs a loud warning when it is set, and it has no place in a deployment.

The knobs you *do* have are per node — `max_tasks`, `labels`, `image_cache_high_watermark` — and
per task, in the [spec](task-spec.md).
