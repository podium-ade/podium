# Task spec

The YAML a `podium run --spec FILE` reads, and the shape of `pkg/spec.TaskSpec`. Flags on
`podium run` override the fields they name; everything below has no flag and can only be
written in a file.

```yaml
image: postgres:16-alpine          # required
command: ["psql", "-h", "db", "-c", "select 1"]
working_dir: /workspace            # default
env:
  PGPASSWORD: podium
labels: [linux/arm64]              # node labels the task requires
timeout: 1h                        # default
max_attempts: 1                    # default

sidecars: {}                       # see below
resources: {}                      # see below
hardening: {}                      # see below
```

`command` is optional: a task that names none runs the image's own `ENTRYPOINT` plus `CMD`.
`env` keys must be valid shell identifiers.

A complete example is `examples/postgres-sidecar.yaml`.

## Sidecars

A sidecar is a sibling container on the task's private network, started before the task and
removed after it. The key is its DNS name, so the task reaches `db` at `db`.

```yaml
sidecars:
  db:
    image: postgres:16-alpine
    command: ["postgres", "-c", "fsync=off"]   # optional; the image's own by default
    env:
      POSTGRES_PASSWORD: podium
    readiness:
      tcp_port: 5432
      timeout: 60s
    resources:
      cpu: 1
      memory_mb: 512
```

- The name must be a valid hostname label — `^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`. `task`
  is reserved: it is the alias the task container itself answers to.
- All sidecars are started at once and waited for in parallel, so two slow ones cost the
  time of the slower, not the sum.
- Their output is streamed live as its own log stream, tagged with the sidecar's name. The
  CLI prints it on stderr prefixed `[db]`.
- **The task container is not created until every sidecar is ready.** One that never
  becomes ready fails the task at provisioning, and the error carries the last 100 lines of
  that sidecar's log — so an adopter sees their database's real complaint.
- Teardown stops sidecars first (their stop signal, 10s, then SIGKILL), then removes the
  task container, the network and the workspace volume.

### Readiness

At most one probe. A sidecar with none is ready as soon as its container is running, which
is almost never what you want for a database.

| Field | Ready when |
|---|---|
| `tcp_port: 5432` | something is listening on that port |
| `http_path: /healthz` (+ `http_port`, default 80) | a GET returns 2xx or 3xx |
| `command: ["pg_isready"]` | the command exits 0 |
| *(none)* | the container is running |

`timeout` (default `60s`) bounds the whole wait; the probe repeats every 500ms until then.

**How a probe runs.** On a Linux node whose Docker daemon is on the same host, the node
dials the sidecar's address on the task bridge directly. Everywhere else — Docker Desktop
on macOS or Windows, where the engine lives in a VM the host cannot route into — the probe
runs *inside* the sidecar with `ContainerExec`, using the `nc` and `wget` that busybox-based
images have. An image with neither (a `scratch`-based one, say) fails immediately with a
message saying so rather than waiting out the timeout; give it a `readiness.command` that
uses a binary it does have.

## Resources

```yaml
resources:
  cpu: 0.5          # cores; 0 means unlimited
  memory_mb: 256    # 0 means unlimited
  pids: 4096        # default
```

The same block is available per sidecar. The node applies the limits blindly; rejecting a
task that asks for more than a node has is the scheduler's job.

- `cpu` becomes the engine's `NanoCpus`. A CPU quota is **not** namespaced: `nproc` inside
  the container still reports the host's core count. Software that sizes a thread pool from
  `nproc` needs telling separately.
- `memory_mb` caps memory *and* swap, so a container never swaps. A task that exceeds it is
  OOM-killed, and Podium reports that as `exited{oom_killed: true}` with the task's
  `failure_reason` set to `oom` — not as a generic non-zero exit.
- `pids` defaults to 4096, so a runaway `fork` loop kills its own container rather than the
  node.

## Hardening

```yaml
hardening:
  read_only_rootfs: false
  capabilities: []        # added back after all are dropped
```

Not negotiable, applied to every task container whatever this section says:

- every Linux capability dropped (`CapDrop: ALL`),
- `no-new-privileges`, so a setuid binary cannot raise privileges,
- the engine's **default seccomp profile** — `unconfined` is not a spec field; a node that
  needs it is an operator decision, not a task's,
- never `Privileged`, and the Docker socket is never mounted,
- a tmpfs at `/podium/secrets`, mounted `noexec,nosuid,size=1m`.

`capabilities` adds back a bounded set, spelled with or without the `CAP_` prefix and in any
case:

`CHOWN`, `DAC_OVERRIDE`, `FOWNER`, `SETUID`, `SETGID`, `NET_BIND_SERVICE`, `KILL`

Anything else is rejected when the spec is validated. These seven cover the things ordinary
software legitimately does — fixing up file ownership, dropping to an unprivileged user,
binding port 80 — and exclude everything that is a route out of the container.

`read_only_rootfs: true` mounts the image's filesystem read-only. `/workspace` is a volume
and stays writable; `/tmp` gets a 1 GB tmpfs, because too much software assumes it can write
there.

**Sidecars are hardened less.** They get `no-new-privileges` and their own `resources`, and
nothing else: a stock database image usually chowns a data directory and drops to an
unprivileged user on the way up, which dropping every capability would break.

## Egress

A task's network reaches the internet and its own sidecars, and nothing else on the host or
the tailnet. An egress allow-list is not implemented; the hook it will plug into is
`internal/node/docker/egress.go`.
