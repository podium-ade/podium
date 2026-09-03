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

> If port 5432 or 8080 is already taken on your machine, set `PODIUM_PG_PORT` and
> `PODIUM_DEV_LISTEN=127.0.0.1:18080`, and match `PODIUM_DATABASE_URL` to the Postgres port.

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

## Documentation

- **[docs/networking.md](docs/networking.md)** — the tailnet transport, identity, ACL, the two keys
- **[docs/node-setup.md](docs/node-setup.md)** — setting up a worker node
- [docs/cli.md](docs/cli.md) — CLI reference, exit codes and streams (contractual)
- [docs/task-spec.md](docs/task-spec.md) — the task spec: sidecars, readiness, limits, hardening
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
| `TS_AUTHKEY` | `tailnet` only, first run — a reusable, pre-approved key tagged `tag:podium-server` |
| `PODIUM_TS_HOSTNAME` | `podium`. The device name, and the first label of the MagicDNS name |
| `PODIUM_TS_STATE_DIR` | `/var/lib/podium/tsnet`. **Must persist**, or the server re-registers as a new device |
| `PODIUM_TS_REQUIRED_NODE_TAG` | `tag:podium-node`. Which tag makes a device a worker |
| `PODIUM_TS_ALLOW_UNTAGGED_NODES` | `false`. Escape hatch for a tailnet with no tags; warns loudly |

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
- **No secrets and no artifacts.** Sidecars, resource limits and container hardening are
  implemented — see [`docs/task-spec.md`](docs/task-spec.md) — but a task cannot yet be given a
  credential, and nothing it produces is collected.
- **No egress policy.** A task's network reaches its own sidecars and the internet, and nothing
  else on the host or the tailnet. Narrowing that is not implemented.
- **A CPU limit is not visible inside the container.** `nproc` reports the host's cores whatever
  `resources.cpu` says, because a CPU quota is not namespaced.
- **No lease expiry or reconciliation** — nothing marks a task `lost` or reschedules one whose
  node vanished, and `timeout` in a task spec is not enforced.
- **A task adopted after a node restart loses its runner event socket** for the rest of the run
  and falls back to Docker API events. See [`docs/runner-events.md`](docs/runner-events.md).
- **Single server process.** Node sessions are held in memory; two replicas would not share them.
- **No RBAC.** The tailnet transport records who is visiting in a `users` table, and every one of
  them can do everything.
- Verified on macOS/arm64 with Docker Desktop only. Linux CI is unproven, though the node and CLI
  do cross-compile (`make dist-node-all`).

## License

Not yet chosen — see [`LICENSE`](LICENSE). This code is not published under any license until
that decision is made.
