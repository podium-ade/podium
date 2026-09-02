# Step 08 — Sidecars, readiness, resource limits, hardening

**Milestone:** M2 · **Depends on:** 07 · **Design ref:** §7.2 steps 3–5, §8 topology, §10 failure modes

## Goal
Make a task an isolated pod: sibling sidecar containers with readiness checks and their own log streams,
enforced CPU/memory/PID limits, and a hardened task container. After this, an adopter's "Postgres + Redis +
app" environment runs unchanged.

## In scope (all in `internal/node/docker`)

### Sidecars
- For each `Spec.Sidecars[name]` (map iteration in sorted order for determinism):
  - Create container: image, command, env, labels `podium.task`, `podium.lease`, `podium.role=sidecar`, `podium.sidecar=<name>`;
    join network `podium-<task_id>` with alias `<name>` (so the task reaches it as `db`, `cache`, …).
  - Start all sidecars concurrently, then wait for readiness **in parallel** with per-sidecar timeout (`Readiness.Timeout`, default 60s):
    - `TCPPort`: dial `<container-ip>:<port>` from the node every 500ms.
    - `HTTPPath` (+`HTTPPort`, default 80): GET returns 2xx/3xx.
    - `Command`: `ContainerExec` returns 0.
    - None specified: ready when `State.Running` is true.
  - Emit `step{name:"sidecar/<name>", status:started|ready|failed}` events. On any failure: emit `error{retryable:false,
    message:"sidecar <name> not ready: …"}` **with the sidecar's last 100 log lines attached in the message**, tear down, return error.
- Attach to each sidecar's logs → `log` events with `stream=sidecar`, `sidecar_name=<name>` (so the UI/CLI can filter).
- Task container starts only after all sidecars are ready.
- Teardown removes sidecars first (SIGTERM, 10s, SIGKILL), then the task container, then network and volume.

### Resource limits (task container; sidecars get the same treatment with their own optional `Resources` — add the field to `spec.Sidecar`)
- `Resources.CPU` → `NanoCPUs = CPU * 1e9`. `MemoryMB` → `Memory` and `MemorySwap = Memory` (no swap). `PIDs` → `PidsLimit`
  (default 4096). Reject specs whose totals exceed the node's advertised capacity **at the server** (step 12); the node applies limits blindly.
- OOM detection: on exit, `State.OOMKilled` → `exited{oom_killed:true}`; the server marks failure reason `oom` (step 06 `logs.Ingest`
  extension: set `failure_reason`).

### Hardening (task container; sidecars: `no-new-privileges` only, since DBs often need caps)
- `SecurityOpt: ["no-new-privileges:true"]`, default seccomp profile (do not pass `unconfined`).
- `CapDrop: ["ALL"]`, `CapAdd: Spec.Hardening.Capabilities` (validated against an allow-list: `CHOWN, DAC_OVERRIDE, FOWNER, SETUID,
  SETGID, NET_BIND_SERVICE, KILL` — anything else rejected at spec validation with a message pointing to docs).
- `ReadonlyRootfs = Spec.Hardening.ReadOnlyRootfs`; when true, add tmpfs at `/tmp` (size 1 GB) and keep `/workspace` writable.
- Never mount the Docker socket; never `Privileged`; `Tmpfs["/podium/secrets"]` mounted `noexec,nosuid,size=1m` (populated in step 09).
- Egress stays open in this step; document the future allow-list hook (`internal/node/docker/egress.go` interface stub).

### Spec validation additions (`pkg/spec`)
- Sidecar `Resources`; capability allow-list; `Readiness` requires exactly one probe or none; sidecar names must not collide with `task`.

## Out of scope
- Secret injection contents (09). Node-level capacity accounting and scheduler enforcement (12). Egress policy (later).

## Acceptance checklist (integration)
- [ ] Task `postgres:16-alpine` sidecar `db` with `POSTGRES_PASSWORD` env + readiness `tcp 5432`; task image `postgres:16-alpine` command
      `psql -h db -U postgres -c 'select 1'` with `PGPASSWORD` → exit 0; events include `step sidecar/db ready`.
- [ ] Sidecar with readiness `tcp 9999` (never listens) and `timeout 5s` → task fails at provisioning; error message contains the sidecar's logs;
      nothing leaked.
- [ ] Two sidecars start concurrently (total provisioning time ≈ max, not sum — assert < 1.5× the slower one).
- [ ] Memory limit 64 MB + `python:3-alpine -c "bytearray(200*1024*1024)"` → `exited{oom_killed:true}`, task `failed`, reason `oom`.
- [ ] CPU limit 0.5 → `nproc` inside still reports host CPUs, but `docker inspect` shows `NanoCpus=500000000` (assert via API).
- [ ] PIDs limit 50 + fork bomb (`sh -c ':(){ :|:& };:'` with timeout 5s) → container survives to be cancelled; host unaffected.
- [ ] `capsh --print` (or `/proc/self/status` CapEff) inside the task shows only added caps; `no-new-privileges` present in inspect.
- [ ] `ReadOnlyRootfs: true` → `touch /x` fails, `touch /workspace/x` and `touch /tmp/x` succeed.
- [ ] Sidecar logs appear as `log{stream:sidecar, sidecar_name:db}` events and the CLI `logs` shows them prefixed `[db]`.

## Verification
```sh
make test-integration ./internal/node/docker/... ./pkg/spec/...
./bin/podium run --spec examples/postgres-sidecar.yaml    # add this example file
```

## Notes
- Readiness dialing from the node uses the container's IP on the task network (`NetworkSettings.Networks[net].IPAddress`); the node
  process itself is not on that network, but Docker bridges are host-reachable by default on Linux. On Docker Desktop (macOS) they
  are **not** — implement readiness via `ContainerExec` fallback (`nc -z`/`sh -c '</dev/tcp/…'`) when the host cannot route.
- Keep seccomp default; if a future adopter needs `unconfined`, that is a per-node operator setting, not a spec field.

## Hand-off notes
_(fill in when done)_
