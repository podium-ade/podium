# Podium

Podium runs containerised tasks on a fleet of machines you own. A control plane
(`podium-server`) schedules work and serves a web UI; a daemon (`podium-node`) on each worker
executes tasks with the local Docker engine and streams logs back live; a CLI (`podium`) submits
and watches them.

**Status: pre-alpha.** The end-to-end loop works — submit a task, watch it run on a real node,
get its exit code — but this is a long way from production. See [Limitations](#limitations).

```
podium run --image alpine:3 -- sh -c 'for i in 1 2 3; do echo tick $i; sleep 1; done; exit 3'
→ task task_01m1j88gv0sg8xaxawdh1fqm0z
→ scheduled on node_01m1j889e944prdn29s2x2d6pa
→ running
tick 1
tick 2
tick 3
→ finished exit 3 in 3.2s
```

The CLI exits with the task's exit code.

## Binaries

| | |
|---|---|
| `podium-server` | API, scheduler, node registry, embedded web UI. Needs Postgres. |
| `podium-node` | Runs on every worker; executes tasks via the local Docker engine. |
| `podium` | CLI. Talks only to the server — never needs Docker. |
| `podium-runner` | PID 1 inside every task container. Runs the task command, forwards signals, reaps orphans, reports events to the node. Embedded in `podium-node` and bind-mounted in; never installed by hand. |

## Quickstart

Requires Go 1.25+, Docker (cgroup v2, API 1.43+), Node 22+ and pnpm.

```sh
make build                                                    # builds the UI, then all four binaries
docker compose -f deploy/docker-compose.dev.yml up -d --wait postgres

PODIUM_TRANSPORT=dev \
PODIUM_DEV_TOKEN=devtoken \
PODIUM_DATABASE_URL=postgres://podium:podium@127.0.0.1:5432/podium \
  ./bin/podium-server &
```

Then enroll a node and run something — see **[docs/node-setup.md](docs/node-setup.md)**.

A task can bring its own environment with it:

```sh
podium run --spec examples/postgres-sidecar.yaml
→ task task_01m1jsfzne7c8v1p1n4h4rjkq3
→ scheduled on node_01m1jsfze5svgz819kp98rp6fb
→ sidecar/db started
[db] LOG:  database system is ready to accept connections
→ sidecar/db ready
→ running
 ?column?
----------
        1
(1 row)
→ finished exit 0 in 200ms
```

A sidecar is a sibling container on the task's private network, addressed by name — `psql -h db`
— started before the task and waited for. See [docs/task-spec.md](docs/task-spec.md).

Open <http://127.0.0.1:8080> for the UI. Under the `dev` transport it asks once for the bearer
token (`devtoken` above) and keeps it in `localStorage`; on a tailnet that prompt never appears,
because Tailscale has already said who you are.

The UI runs the fleet, not just watches it: submit a task from a form or from the same YAML
`--spec` takes, re-run a finished one, follow live logs with a per-sidecar filter, cancel,
download artifacts, drain / undrain / delete / rekey a node, and set or delete secrets. A task
that is still `queued` says which of the scheduler's reasons is keeping it there.

> If port 5432 or 8080 is already taken on your machine, set `PODIUM_PG_PORT` and
> `PODIUM_DEV_LISTEN=127.0.0.1:18080`, and match `PODIUM_DATABASE_URL` to the Postgres port.

### Keeping what a task produces

Point the server at an S3-compatible store and anything a task drops in
`/workspace/.podium/artifacts/` is kept, along with the task's log once it finishes:

```sh
docker compose -f deploy/docker-compose.yml up -d --wait postgres minio
PODIUM_S3_ENDPOINT=127.0.0.1:9000 PODIUM_S3_BUCKET=podium \
  PODIUM_S3_ACCESS_KEY=podium PODIUM_S3_SECRET_KEY=... ./bin/podium-server

podium run --image alpine:3 -- sh -c \
  'mkdir -p /workspace/.podium/artifacts; echo ok > /workspace/.podium/artifacts/r.txt'
podium artifacts TASK_ID
podium artifact get ARTIFACT_ID
```

Nodes never talk to the object store: an artifact travels node → server → S3. See
[docs/task-spec.md](docs/task-spec.md#artifacts).

## Running across machines

The `dev` transport above is loopback-only, so node and server share a host. For real workers,
Podium joins your Tailscale network: the server serves HTTPS on its MagicDNS name, workers dial
out and listen for nothing, and there is no login page, no API token and no public ingress.

```sh
PODIUM_TRANSPORT=tailnet \
TS_AUTHKEY=tskey-auth-...                        `# reusable, pre-approved, tag:podium-server` \
PODIUM_DATABASE_URL=postgres://... \
  ./bin/podium-server

# from any device on the tailnet — no token, no login
./bin/podium --server https://podium.<tailnet>.ts.net nodes
```

Read **[docs/networking.md](docs/networking.md)** first: it covers what to create in the
Tailscale admin console, the ACL, and the two different keys involved (a Tailscale auth key and a
Podium enrollment token are not the same thing).

## What happens when things go wrong

The control plane places work on the node with the most free slots that carries every label
the task asks for and has room for its CPU and memory (its sidecars' included). A task it
cannot place stays `queued` and says why — `podium task get` shows `queued_reason`.

After that it keeps the promises placement made:

- a node that takes an assignment and does not acknowledge it within 15 seconds loses it;
- a task that outruns its `timeout` is stopped and ends `failed{reason: timeout}`;
- a node that stops heartbeating is `unreachable` at 30 seconds and `offline` at 120, at
  which point its tasks are requeued (`retry_on_node_loss: true`) or marked **`lost`** —
  which is not `failed`: nothing about the task went wrong, its machine went away;
- a node that comes back is told what the control plane actually holds for each container it
  still has, so its logs resume at the right byte, and any container the control plane has
  written off is torn down instead of being left running.

```sh
podium node drain worker-3      # finishes what it has, takes nothing new
podium node undrain worker-3
podium node rm worker-3         # once it is drained and idle
```

A node started with `--exit-on-drain` exits 0 when its last task finishes, which is the
upgrade path. See [docs/cli.md](docs/cli.md) and [docs/protocol.md](docs/protocol.md).

## Documentation

- **[docs/networking.md](docs/networking.md)** — the tailnet transport, identity, ACL, the two keys
- **[docs/node-setup.md](docs/node-setup.md)** — setting up a worker node
- [docs/cli.md](docs/cli.md) — CLI reference, exit codes and streams (contractual)
- [docs/task-spec.md](docs/task-spec.md) — the task spec: secrets, sidecars, readiness, limits, hardening
- [docs/protocol.md](docs/protocol.md) — the node↔server stream, event ordering and acks
- [docs/runner-events.md](docs/runner-events.md) — `podium-runner` as PID 1 and its event socket

## Configuration

`podium-server` is configured entirely by environment:

| | |
|---|---|
| `PODIUM_DATABASE_URL` | **required** — Postgres DSN; the server migrates on start |
| `PODIUM_TRANSPORT` | `dev` (default), `tailnet`, or `host` |
| `PODIUM_DEV_LISTEN` | `dev` only. `127.0.0.1:8080`; must be loopback or the server refuses to start |
| `PODIUM_DEV_TOKEN` | **required** for `dev` — the shared bearer token |
| `PODIUM_MASTER_KEY_FILE` | the 32-byte AES key secrets are encrypted under. Must be mode `0600`/`0400`. Unset means secrets are unavailable |
| `PODIUM_MASTER_KEY` | the same key inline, for development only; the server warns loudly |
| `PODIUM_S3_ENDPOINT` | `host:port` (or a URL) of the S3-compatible store artifacts and rolled-up logs live in. Unset disables artifacts entirely, which is supported |
| `PODIUM_S3_BUCKET` | the bucket, created on start if missing |
| `PODIUM_S3_ACCESS_KEY` / `PODIUM_S3_SECRET_KEY` | its credentials |
| `PODIUM_S3_REGION` | `us-east-1`. MinIO ignores it; a real S3 does not |
| `PODIUM_LOG_ROLLUP_INTERVAL` | `1m`. How often finished tasks' logs are swept into the object store |
| `PODIUM_LOG_PRUNE_INTERVAL` | `1h`. How often rolled-up log chunks are pruned from Postgres |
| `PODIUM_LOG_CHUNK_GRACE` | `24h`. How long a task's hot log rows survive after they reach the object store |
| `TS_AUTHKEY` | `tailnet` only, first run — a reusable, pre-approved key tagged `tag:podium-server` |
| `PODIUM_TS_HOSTNAME` | `podium`. The device name, and the first label of the MagicDNS name |
| `PODIUM_TS_STATE_DIR` | `/var/lib/podium/tsnet`. **Must persist**, or the server re-registers as a new device |
| `PODIUM_TS_REQUIRED_NODE_TAG` | `tag:podium-node`. Which tag makes a device a worker |
| `PODIUM_TS_ALLOW_UNTAGGED_NODES` | `false`. Escape hatch for a tailnet with no tags; warns loudly |

`podium-server` also has two subcommands: `gen-master-key` mints a key for
`PODIUM_MASTER_KEY_FILE`, and `rotate-master-key --old FILE --new FILE` re-encrypts every stored
secret under a new one. See [docs/cli.md](docs/cli.md#secrets-at-rest).

`/healthz`, `/readyz` and `/metrics` are open. Under `dev` every RPC requires
`Authorization: Bearer`; under `tailnet` identity comes from the connection and no RPC takes a
credential at all.

## Development

```sh
make build              # UI + runner-embed + all four binaries into bin/, host-native
make runner-embed       # just the two Linux podium-runner builds that podium-node embeds
make dist-node-all      # cross-compiled podium-node + podium for linux/amd64 and linux/arm64
make test               # unit tests
make test-integration   # + real Postgres and Docker via testcontainers
make e2e                # boots a full stack and drives the real CLI
make lint proto fmt
go build -tags noui ./...   # skip the embedded UI, no Node required
```

`podium-runner` is the one binary that is never host-native: it is PID 1 inside a Linux task
container, so `make build` cross-compiles it for `linux/amd64` and `linux/arm64` into
`internal/node/docker/runnerbin/` (embedded into `podium-node`, gitignored, never committed) and
copies the host architecture's build to `bin/podium-runner`. A clone that has never run
`make runner-embed` still compiles — `go:embed` finds a committed placeholder — but a node
started from it refuses to run tasks and says which target to build.

## Limitations

This is an early slice. Known and deliberate:

- **The tailnet transport is unproven in the wild.** It is implemented and unit-tested, but it
  has not yet been run against a real tailnet — that needs tagged auth keys and HTTPS enabled on
  the tailnet. The `host` transport is likewise unverified. The `dev` transport is the tested one.
- **Artifacts have never run against a real S3.** The client is `minio-go` against the S3 API
  and `deploy/docker-compose.yml` wires up MinIO, but the build machine may not pull images, so
  every object-store path is tested against an in-process endpoint
  (`internal/server/artifacts/fakes3`) that verifies presigned signatures for real. A run
  against an actual MinIO or S3 is still owed.
- **A task adopted after a node restart collects no artifacts**, the same way it loses its log
  redactor: the auto-collection pass and the runner's event socket both belong to the run that
  created the container.
- **Rolled-up logs lose the interleaving between streams.** Once a finished task's chunks have
  been pruned, its log is replayed from one object per stream — stdout as one run, then stderr —
  because nothing records how they were braided together. Within a stream the order is exact.
- **A sidecar cannot reference a secret of its own**, and log redaction is best-effort string
  matching rather than a guarantee — see [`docs/task-spec.md`](docs/task-spec.md#redaction).
  Secrets are set from `podium secret set/ls/rm`, from the web UI, and consumed with
  `--secret NAME[:env:KEY|:file:/path]`. A value cannot be read back from anywhere: there is no
  read endpoint, by design.
- **No egress policy.** A task's network reaches its own sidecars and the internet, and nothing
  else on the host or the tailnet. Narrowing that is not implemented.
- **A CPU limit is not visible inside the container.** `nproc` reports the host's cores whatever
  `resources.cpu` says, because a CPU quota is not namespaced.
- **Image cache pruning is off by default and only ever removes images Podium pulled.** A node
  shares its Docker engine with everything else on the machine, so the LRU prune is opt-in
  (`image_cache_prune: true`) and treats `data_dir/images.json` — written at pull time — as an
  allow-list. An image Podium did not fetch is never a candidate, however full the disk gets.
- **One `podium-node` per Docker engine.** The daemon claims every container labelled
  `podium.task` on the engine, so two of them adopt each other's work.
- **A task adopted after a node restart loses its runner event socket** for the rest of the run
  and falls back to Docker API events. See [`docs/runner-events.md`](docs/runner-events.md).
- **Single server process, and now emphatically so.** Node sessions are held in memory, so only
  the server holding a node's stream can assign to it, cancel on it or drain it — and the health
  watchdog on a second replica would see every node as sessionless and start expiring leases.
  A leader lock is needed before a second replica is ever started.
- **No RBAC.** The tailnet transport records who is visiting in a `users` table, and every one of
  them can do everything. The web UI is the same: whoever can reach it can submit tasks, drain
  nodes and delete secrets.
- **The web UI holds a task's whole log in memory** and has no charts, no node CPU/memory
  utilisation (`ListNodes` returns enrolment-time capacity, not the last heartbeat's readings)
  and no way to jump to an arbitrary page of the task list — paging walks cursors it has
  already seen.
- Verified on macOS/arm64 with Docker Desktop only. Linux CI is unproven, though the node and CLI
  do cross-compile (`make dist-node-all`).

## License

Not yet chosen — see [`LICENSE`](LICENSE). This code is not published under any license until
that decision is made.
