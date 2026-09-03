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
| `podium-runner` | Placeholder. Becomes PID 1 inside task containers; not yet implemented. |

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

Open <http://127.0.0.1:8080> for the UI. Under the `dev` transport it asks once for the bearer
token (`devtoken` above) and keeps it in `localStorage`; that prompt disappears when the Tailscale
transport lands and identity comes from the tailnet.

> If port 5432 or 8080 is already taken on your machine, set `PODIUM_PG_PORT` and
> `PODIUM_DEV_LISTEN=127.0.0.1:18080`, and match `PODIUM_DATABASE_URL` to the Postgres port.

## Documentation

- **[docs/node-setup.md](docs/node-setup.md)** — setting up a worker node
- [docs/cli.md](docs/cli.md) — CLI reference, exit codes and streams (contractual)
- [docs/protocol.md](docs/protocol.md) — the node↔server stream, event ordering and acks

## Configuration

`podium-server` is configured entirely by environment:

| | |
|---|---|
| `PODIUM_DATABASE_URL` | **required** — Postgres DSN; the server migrates on start |
| `PODIUM_TRANSPORT` | `dev` (default). `tailnet` is not implemented yet |
| `PODIUM_DEV_LISTEN` | `127.0.0.1:8080`. Must be loopback or the server refuses to start |
| `PODIUM_DEV_TOKEN` | **required** for `dev` — the shared bearer token |

`/healthz`, `/readyz` and `/metrics` are open; every RPC requires `Authorization: Bearer`.

## Development

```sh
make build              # UI + all four binaries into bin/
make test               # unit tests
make test-integration   # + real Postgres and Docker via testcontainers
make e2e                # boots a full stack and drives the real CLI
make lint proto fmt
go build -tags noui ./...   # skip the embedded UI, no Node required
```

## Limitations

This is an early slice. Known and deliberate:

- **Single machine only.** The `dev` transport is loopback-only, so nodes must share a host with
  the server. Multi-machine needs the Tailscale transport, which is not built.
- **No secrets, sidecars, resource limits, or artifacts.**
- **No lease expiry or reconciliation** — nothing marks a task `lost` or reschedules one whose
  node vanished, and `timeout` in a task spec is not enforced.
- **Cancelling takes up to 30s.** Without the runner as PID 1, the kernel discards a
  default-disposition SIGTERM to a namespace's init, so containers die at the SIGKILL (exit 137).
- **Single server process.** Node sessions are held in memory; two replicas would not share them.
- Verified on macOS/arm64 with Docker Desktop only. Linux CI is unproven.

## License

Not yet chosen — see [`LICENSE`](LICENSE). This code is not published under any license until
that decision is made.
