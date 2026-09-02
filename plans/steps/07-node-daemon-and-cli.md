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
- [ ] `make e2e` passes locally against Docker Desktop and in CI on Linux.
- [ ] Killing `podium-node` with SIGTERM during a task: the task container keeps running; on restart the node re-adopts it via
      `ListOwned` + `Hello.running_task_ids` and continues streaming (the server side may still be naive; the node must at least not orphan it).
- [ ] Replay buffer test: unacked events survive a reconnect and arrive exactly once server-side (store PK dedupes; assert no `error` markers).
- [ ] CLI `run` returns the task's exit code; `--detach` prints the task ID and returns 0.
- [ ] Node refuses to start if `data_dir` is not writable or `server` is empty, with actionable messages.

## Verification
```sh
make build e2e
./bin/podium run --image alpine:3 -- sh -c 'echo hi; sleep 5; exit 3'; echo "exit=$?"   # prints hi …, exit=3
./bin/podium nodes
```

## Notes
- The CLI must never require Docker; it only talks to the server.
- Keep `internal/node` free of server-side packages; the only shared code is `internal/proto`, `pkg/spec`, `internal/transport`.

## Hand-off notes
_(fill in when done)_
