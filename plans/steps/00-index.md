# Podium — Implementation Steps (Control Plane, Node Daemon, Networking)

Reference design: `../control-plane-node-networking.html` (open in a browser). Each step below is a
self-contained work item an agent can execute in one session. Do them **in order**; every step
declares what it depends on and what the next step expects to exist.

## How to work a step

1. Read `00-index.md` (this file) and the step file. Skim the linked section of the HTML design.
2. Respect the **Canonical names** below — every step uses them. Do not invent alternatives.
3. Implement only what is in the step's *In scope*. Anything in *Out of scope* belongs to a later step.
4. Run the step's **Verification** commands. All must pass before the step is done.
5. Update the step file's checklist (`- [ ]` → `- [x]`) and append a short *Hand-off notes* section
   with anything the next step needs to know (deviations, gotchas, TODOs).
6. Commit with message `step NN: <title>`.

## Steps

| # | File | Milestone | Depends on |
|---|------|-----------|------------|
| 01 | `01-repo-scaffold.md` | M0 | — |
| 02 | `02-wire-contract.md` | M0 | 01 |
| 03 | `03-store-and-queue.md` | M1 | 02 |
| 04 | `04-runner.md` | M1 | 02 |
| 05 | `05-node-docker-executor.md` | M1 | 04 |
| 06 | `06-server-api-and-node-stream.md` | M1 | 03 |
| 07 | `07-node-daemon-and-cli.md` | M1 ✔ | 05, 06 |
| 08 | `08-sidecars-limits-hardening.md` | M2 | 07 |
| 09 | `09-secrets.md` | M2 | 07 |
| 10 | `10-artifacts.md` | M2 ✔ | 07 |
| 11 | `11-tailnet-transport.md` | M3 ✔ | 07 |
| 12 | `12-scheduler-leases-reconciliation.md` | M4 ✔ | 08, 11 |
| 13 | `13-web-ui.md` | M5 ✔ | 09, 10, 12 |
| 14 | `14-packaging-and-docs.md` | M6 ✔ | 13 |

Steps 08/09/10 are independent of each other and may run in parallel after 07. Everything else is
sequential. `--mtls` transport is deliberately deferred and has no step.

## Canonical names (use exactly these)

**Module & layout**
- Go module: `github.com/alvaroibarguen/podium` (change once in step 01 if the org differs; never elsewhere).
- Binaries: `cmd/podium` (CLI), `cmd/podium-server`, `cmd/podium-node`, `cmd/podium-runner`.
- Packages: `internal/server/{api,nodes,scheduler,store,secrets,logs,artifacts}`,
  `internal/node/{docker,exec,reconcile,heartbeat}`, `internal/runner`, `internal/transport/{dev,tailnet}`,
  `internal/proto` (generated), `proto/podium/v1/*.proto`, `pkg/spec`, `web/`, `deploy/`, `docs/`.

**Wire**
- Proto package `podium.v1`. Services: `NodeService` (Enroll, Stream), `TaskService`, `NodeAdminService`,
  `SecretService`, `ArtifactService`. Codegen via `buf` into `internal/proto`. Connect RPC.
- Node stream messages: node→server `Hello`, `Heartbeat`, `TaskEvent`; server→node `Assign`, `Ack`, `Cancel`, `Drain`.
- `TaskEvent.kind` values: `provisioning`, `pulling`, `started`, `log`, `step`, `artifact`, `exited`, `finished`, `error`.

**Task status** (`tasks.status`): `queued → scheduled → provisioning → running → succeeded | failed | cancelled | lost`.

**Node status**: `online`, `unreachable` (≥30s no heartbeat), `offline` (≥120s), `draining`.

**Timings**: heartbeat 10s; provisioning ack deadline 15s after Assign; cancel grace 30s SIGTERM→SIGKILL;
event batch flush 100ms or 64KB.

**Container labels**: `podium.task=<task_id>`, `podium.lease=<lease_id>`, `podium.role=task|sidecar`,
`podium.sidecar=<name>`. Network: `podium-<task_id>`. Workspace volume: `podium-ws-<task_id>` mounted at `/workspace`.

**Runner mounts**: binary at `/podium/runner` (read-only), event socket at `/podium/events.sock`,
secret files under `/podium/secrets/` (tmpfs).

**Server config (env)**: `PODIUM_DATABASE_URL`, `PODIUM_S3_ENDPOINT`, `PODIUM_S3_BUCKET`, `PODIUM_S3_ACCESS_KEY`,
`PODIUM_S3_SECRET_KEY`, `PODIUM_MASTER_KEY_FILE`, `PODIUM_TRANSPORT=dev|tailnet`, `PODIUM_DEV_LISTEN=127.0.0.1:8080`,
`PODIUM_DEV_TOKEN`, `TS_AUTHKEY`, `PODIUM_TS_HOSTNAME=podium`, `PODIUM_TS_STATE_DIR`.

**Node config** (`/etc/podium/node.yaml`, overridable by `PODIUM_NODE_*` env):
```yaml
server: http://127.0.0.1:8080        # or https://podium.<tailnet>.ts.net
transport: dev                        # dev | tailnet | host
data_dir: /var/lib/podium-node
labels: [linux/amd64, browser]
max_tasks: 4
enroll_token: ""                      # first run only
dev_token: ""                         # dev transport only
image_cache_high_watermark: 0.80
```

**IDs**: all IDs are ULIDs as lowercase strings (`task_01j…`, `node_01j…`, `lease_01j…`, prefixed).

## MVP-0 track (the minimum demo loop)

Goal: start the daemons on ONE machine, enroll a node, run a basic image as a task, watch it live in a web UI.
Dev transport only (localhost). Everything not on that path is deferred. When told to "do the MVP-0 track",
execute these units in this order, applying the trims — the trims override the step files where they conflict.

| Unit | Base step | Trims / notes |
|------|-----------|---------------|
| A | 01 | As written. |
| B | 02 | Only `NodeService` (Enroll, Stream), `TaskService`, `NodeAdminService` (CreateEnrollmentToken, ListNodes). `TaskSpec` fields: image, command, working_dir, env, labels, timeout, max_attempts (others omitted). `Assign` has no `resolved_secrets`/`registry_auths`. No secret/artifact/sidecar messages, no `SecretService`/`ArtifactService`. |
| C | 03 | Tables: `nodes`, `enrollment_tokens`, `tasks`, `task_events`, `task_log_chunks` only. Skip secrets/artifacts/users/audit and their store methods. |
| D | 05 | **No runner.** Container `Cmd = Spec.Command` directly, default entrypoint kept. No events socket, no runner mount. Events from the Docker API only: provisioning, pulling, started, log, exited (incl. `oom_killed` from inspect), finished, error. Everything else in 05 (network, volume, labels, teardown, cancel, seq ordering) as written. Step 04 is skipped entirely. |
| E | 06 | As written minus secrets resolution (pass nothing). Naive scheduler stays. |
| F | 07 | CLI commands: `run`, `tasks`, `task get`, `task cancel`, `logs -f`, `nodes`, `node enroll-token`, `version`. Node runs on the same host as the server. e2e test scenario 1 required; scenario 2 (server restart mid-task, no log gap) required too — the replay buffer is in scope. |
| G | 13 | Screens: Tasks list, Task detail with live logs + cancel, Nodes list with "create enrollment token" panel. No Secrets screen, no artifacts panel, no `WhoAmI` (show "dev" in header under dev transport). Embedded via `go:embed`; `-tags noui` build supported. |

Parallelism: `A → B → (C ‖ D) → E (needs C) → F (needs D, E) → G`.

Deferred after MVP-0, in recommended order: 11 (Tailscale, multi-machine), 04 (runner — becomes the entrypoint, additive),
08 (sidecars/limits), 09 (secrets), 12 (scheduler), 10 (artifacts), full 13, 14.

MVP-0 acceptance (all must hold):
```sh
docker compose -f deploy/docker-compose.dev.yml up -d postgres      # dev compose: postgres only
PODIUM_TRANSPORT=dev PODIUM_DEV_TOKEN=devtoken PODIUM_DATABASE_URL=postgres://podium:podium@127.0.0.1:5432/podium ./bin/podium-server &
TOKEN=$(./bin/podium --server http://127.0.0.1:8080 --token devtoken node enroll-token --label demo)
PODIUM_NODE_SERVER=http://127.0.0.1:8080 PODIUM_NODE_TRANSPORT=dev PODIUM_NODE_DEV_TOKEN=devtoken PODIUM_NODE_ENROLL_TOKEN=$TOKEN PODIUM_NODE_DATA_DIR=/tmp/podium-node ./bin/podium-node &
./bin/podium --server http://127.0.0.1:8080 --token devtoken nodes                       # one node, online
./bin/podium --server http://127.0.0.1:8080 --token devtoken run --image alpine:3 -- sh -c 'for i in 1 2 3 4 5; do echo tick $i; sleep 1; done; exit 3'
# → prints tick 1..5 live, exits 3
open http://127.0.0.1:8080   # UI: task visible with status, live logs while running, node online; token panel works
make e2e                     # green
```

## Conventions
- Go 1.23+, `slog` for logging, `context.Context` first arg everywhere, errors wrapped with `%w`.
- Tests: unit tests next to code; integration tests behind `//go:build integration` using real Docker/Postgres
  via testcontainers; `make test` runs unit, `make test-integration` runs both.
- No global state except the process-level logger.
- Every daemon exposes `/healthz` (process up) and `/readyz` (dependencies reachable) and `/metrics` (Prometheus).
- Never log secret values. Redaction is defense in depth, not the primary control.
