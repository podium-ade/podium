# Concepts

The five nouns Podium is built out of, and — just as important — what a task is *not* in this
version.

---

## The shape of the system

```
        you                      the control plane                 a worker
   ┌───────────┐              ┌────────────────────┐         ┌──────────────────┐
   │  podium   │──── API ────►│                    │         │   podium-node    │
   │   (CLI)   │              │   podium-server    │◄────────│                  │
   └───────────┘              │                    │  one    │   ┌──────────┐   │
   ┌───────────┐              │  · scheduler       │  bidi   │   │  Docker  │   │
   │  web UI   │──── API ────►│  · node registry   │ stream  │   │  engine  │   │
   └───────────┘              │  · secrets         │────────►│   └──────────┘   │
                              │  · log ingest      │         │        │         │
                              └─────────┬──────────┘         │   ┌────▼─────┐   │
                                        │                    │   │   task   │   │
                              ┌─────────▼──────────┐         │   │ container│   │
                              │ Postgres   RustFS  │         │   └──────────┘   │
                              └────────────────────┘         └──────────────────┘
```

Two things about that picture are load-bearing:

**The worker dials out.** A node opens one bidirectional stream to the control plane and listens
for nothing. There is no inbound port on a worker, no firewall rule to open and no address for
the control plane to know. Adding a worker on a different continent is the same operation as
adding one on the same desk.

**The control plane never touches Docker, and the worker never touches Postgres or the object
store.** They are separate blast radiuses. A node that is compromised has the Docker engine of
its own host and nothing else — not the database, not the bucket credentials, not the other
workers.

---

## Control plane — `podium-server`

One process. It owns:

- **the API** (Connect RPC over HTTP; the CLI, the web UI and the nodes all speak it),
- **the scheduler**, which decides which node runs which task,
- **the node registry**, which is the live view of who is connected,
- **the secrets store**, encrypted under a master key it holds,
- **log ingest**, which turns a node's event batches into rows,
- **the embedded web UI**, compiled into the binary.

It needs Postgres. It optionally needs an S3-compatible object store, without which artifacts
and log roll-up are simply unavailable — a supported configuration.

**It is a single process, deliberately and for now unavoidably.** Node sessions live in memory,
so only the server holding a node's stream can assign to it, cancel on it or drain it. A second
replica would run a second scheduler whose watchdog saw every node as sessionless and started
expiring leases. Do not start one until there is a leader lock.

## Worker — `podium-node`

One daemon per machine, and **one per Docker engine**: it claims every container labelled
`podium.task` on the engine, so two of them would adopt each other's work.

It enrolls once, keeps one stream open, and turns each `Assign` into containers. It batches the
resulting events, buffers them until the server acknowledges them, and replays anything unacked
across a reconnect. It has the Docker socket, which makes it root-equivalent on its host — see
[`security.md`](security.md).

A restarted node does not lose its work. It re-discovers the containers it left behind, asks the
control plane what it already holds for each of them, and resumes their logs from the right
byte.

## CLI — `podium`

Talks only to the server. It never touches Docker, so it runs anywhere: a laptop, a CI job, the
`ghcr.io/alvaroibarguen/podium` image. `podium run` follows the task it submits and exits with
the task's own exit code, which is what makes it usable as a CI step.

## Runner — `podium-runner`

PID 1 inside every task container. It is not something you install: it is embedded in
`podium-node`, extracted at startup and bind-mounted read-only into each container at
`/podium/runner`.

It exists because the kernel discards a default-disposition signal sent to a PID namespace's
init. With the task command as PID 1, `podium task cancel` on a plain `sleep 60` had nothing to
catch SIGTERM and took the full 30-second grace before the engine's SIGKILL. With the runner
there, cancellation is prompt for *every* command:

| | cancelling `sleep 60` |
|---|---|
| without a runner | 31.10s, exit 137 (SIGKILL) |
| with the runner | 1.09s, exit 143 (SIGTERM) |

It also reaps orphaned processes and reports structured events back over a Unix socket. See
[`runner-events.md`](runner-events.md).

---

## Task

A task is **one container run, on one machine, from one image, to one exit code.**

```yaml
image: alpine:3
command: ["sh", "-c", "echo hello"]
```

Everything about it is in its [spec](task-spec.md): the image, the command, the environment,
which secrets it needs, which sidecars come up beside it, its resource limits and hardening,
which node labels it requires, its timeout and its retry budget.

A task gets:

- **a private bridge network**, `podium-<task_id>`, shared only with its own sidecars,
- **a fresh workspace volume**, `podium-ws-<task_id>`, mounted at `/workspace`,
- **a tmpfs at `/podium/secrets`**, `noexec,nosuid`,
- **`podium-runner` as PID 1**,
- and `PODIUM_TASK_ID`, `PODIUM_LEASE_ID` and `PODIUM_WORKDIR` in its environment.

All of it is removed when the task ends.

### What a task is *not*, in this version

This is the part that saves you an afternoon:

- **Not a pipeline.** There are no steps, no stages, no fan-out, no dependencies between tasks
  and no way for one task to trigger another. If you want a pipeline, the shell inside your
  container is the pipeline.
- **Not a cron job.** Nothing schedules a task on a timer. Something outside Podium submits it.
- **Not restartable.** A terminal task has no outgoing edges in the state graph. "Re-run" in the
  UI creates a *new* task from the same spec; it is not a retry of the old one.
- **Not a build cache.** `/workspace` is fresh every time and is deleted at the end. Nothing is
  carried between tasks except what you put in the object store as an artifact.
- **Not multi-machine.** A task and its sidecars are one pod on one node. There is no way to
  spread one task across two workers.
- **Not interactive.** There is no exec, no shell, no attach and no port forward into a running
  task. You get its stdout and stderr.
- **Not idempotent by assumption.** `retry_on_node_loss` is **off by default** precisely because
  Podium does not know whether running your task twice is safe.

### Task statuses

```
queued ──► scheduled ──► provisioning ──► running ──┬──► succeeded
   │            │              │                    ├──► failed
   │            │              │                    ├──► cancelled
   └────────────┴──────────────┴────────────────────┴──► lost
```

| status | meaning |
|---|---|
| `queued` | admitted; no node has it yet. `queued_reason` says why |
| `scheduled` | assigned to a node; the node has 15s to acknowledge |
| `provisioning` | the node is pulling images and starting sidecars |
| `running` | the task command has been forked |
| `succeeded` | exit code 0 |
| `failed` | non-zero exit, or the task never ran (`failure_reason` says which) |
| `cancelled` | an operator asked for it to stop |
| `lost` | the machine went away |

**`lost` is not `failed`, and the difference matters.** `failed` means the task, or its spec,
did not work — go and read the logs. `lost` means nothing about the task went wrong: its node
went offline while it was running, and whether re-running is safe is your call, not Podium's.

## Sidecar

A sibling container on the task's private network, addressed by the name it is keyed under.

```yaml
sidecars:
  db:
    image: pgvector/pgvector:pg16
    readiness: { command: ["pg_isready", "-U", "postgres"] }
```

The task reaches it at `db`. Sidecars are pulled in order, started concurrently, and **waited
for**: the task container is not created until every readiness probe has passed. A sidecar that
never becomes ready fails the task at `provisioning`, with the last 100 lines of that sidecar's
log in the error message — the task container is never created at all.

Sidecars are torn down with the task. They are *not* hardened like the task container, and they
cannot reference secrets. See [`task-spec.md`](task-spec.md#sidecars).

## Node

A machine with a Docker engine and a `podium-node` daemon. It has:

- **labels** — set at enrollment, and the only thing scheduling matches on. A task is placed
  only on a node whose labels are a **superset** of the task's.
- **capacity** — `max_tasks` (the slot budget the scheduler assigns against), plus CPU cores and
  memory from the machine. **A capacity of zero means "unmeasured", not "none"**, and imposes no
  constraint.
- **a status** — `online`, `unreachable` at 30s of heartbeat silence, `offline` at 120s (at
  which point its tasks are written off), `draining`.
- **a `draining` flag**, which is separate from status: it is the operator's standing
  instruction and survives a restart of either end. A node can be `draining: true, status:
  OFFLINE`.

## Lease

Every assignment mints a lease: a node id, a lease id and an expiry, written on the task row.
It is the backstop, not the primary detector — the heartbeat watchdog notices a dead node long
before a lease runs out. It fires only when the lease has passed *and* the node has no session,
which catches a task pointing at a node row that no longer exists, or one stranded by a control
plane that was down while its node died.

## Artifact

A file a task kept. Two ways to make one:

1. anything under `/workspace/.podium/artifacts` is collected when the task exits;
2. `/podium/runner artifact add PATH` uploads immediately, mid-run.

A finished task's **log** also becomes an artifact: a minute after it settles, its chunks are
compressed into the object store and later pruned out of Postgres. Those rows have `kind: log`
rather than `kind: file`.

**Nothing about artifacts can fail a task.** Every failure — too large, store down, no store
configured — is a non-fatal event. See [`storage.md`](storage.md).

---

## Where to go next

- [`quickstart.md`](quickstart.md) — a working control plane and a first task
- [`task-spec.md`](task-spec.md) — every field, with examples
- [`security.md`](security.md) — read before you choose which machines run a node
- [`networking.md`](networking.md) — workers on other machines
- [`operations.md`](operations.md) — backup, upgrade, drain, metrics
