# Task spec

The YAML a `podium run --spec FILE` reads, and the shape of `pkg/spec.TaskSpec`. Flags on
`podium run` override the fields they name; everything below has no flag and can only be
written in a file.

```yaml
image: pgvector/pgvector:pg16          # required
command: ["psql", "-h", "db", "-c", "select 1"]
working_dir: /workspace            # default
env:
  PGPASSWORD: podium
labels: [linux/arm64]              # node labels the task requires
timeout: 1h                        # default
max_attempts: 1                    # default
retry_on_node_loss: false          # default: see below

secrets: []                        # see below
sidecars: {}                       # see below
resources: {}                      # see below
hardening: {}                      # see below
```

`command` is optional: a task that names none runs the image's own `ENTRYPOINT` plus `CMD`.
`env` keys must be valid shell identifiers.

Unknown keys are an **error**, not a warning: the decoder runs with `KnownFields(true)`, so a
misspelled `privilged: true` fails loudly instead of quietly submitting a task that does not do
what you asked. The web UI's YAML editor mirrors that.

Complete, runnable examples — every one of them parsed by `go test ./examples/...` with this same
decoder, and every one using only `alpine:3`, `pgvector/pgvector:pg16` or `redis:7-alpine`:

| | |
|---|---|
| [`examples/hello.yaml`](../examples/hello.yaml) | the smallest useful task |
| [`examples/postgres-sidecar.yaml`](../examples/postgres-sidecar.yaml) | a database beside the task, waited for |
| [`examples/secrets.yaml`](../examples/secrets.yaml) | both secret targets, and what redaction does not cover |
| [`examples/limits.yaml`](../examples/limits.yaml) | limits and hardening, from inside the container — including an OOM kill |
| [`examples/artifacts.yaml`](../examples/artifacts.yaml) | both ways to keep a file |

## Timeout, attempts and losing a node

`timeout` is enforced by the control plane, not by the node: past it the server asks the node
to stop the container and the task ends `failed` with `failure_reason: timeout`. The exit
code and the usage are still the container's own, because the container is what actually
stops.

`max_attempts` counts *assignments*, not runs. An attempt is spent when a task is handed to a
node, so a node that takes an assignment and never acknowledges it costs one; the task is
requeued until the budget runs out and then fails with `node did not accept assignment`.

`retry_on_node_loss` is the one dial for what happens when the machine running a task goes
offline mid-run. It is **off by default**, and the default is the careful one: with it off the
task is marked `lost` and a human decides, because a task that is not idempotent must not be
silently run twice. With it on the task comes back as a new attempt on whatever node can take
it, up to `max_attempts`.

`lost` is deliberately not `failed`. Nothing about the task went wrong — its machine
disappeared — and the two need different answers.

## Secrets

A secret is stored once with `podium secret set NAME` and referenced by name. The spec
never carries a value — only the name and where the task wants it.

```yaml
secrets:
  - name: DB_PASSWORD          # required: the name it was stored under
    target: env                # env (default) or file
    key: PGPASSWORD            # required: the variable name, or the path for a file
  - name: DEPLOY_KEY
    target: file
    key: /podium/secrets/deploy_key
```

`podium run` has a shorthand for the same thing:

```sh
podium run --secret DB_PASSWORD                              # env DB_PASSWORD
podium run --secret DB_PASSWORD:env:PGPASSWORD               # env PGPASSWORD
podium run --secret DEPLOY_KEY:file:/podium/secrets/key      # a file
```

- `name` must match `^[A-Za-z_][A-Za-z0-9_.-]*$`. `key` is required — Podium will not guess
  which variable or path you meant.
- `target: env` puts the value in an environment variable. `key` must be a shell identifier.
  Secret variables are appended after the spec's own `env` block, so a secret always wins
  over a plaintext `env` entry of the same name.
- `target: file` writes the value to `key` inside the container, mode `0444`, mounted
  read-only. `key` must be an absolute, clean path. Put it under `/podium/secrets/`: that is
  a `noexec,nosuid`, 1 MB tmpfs every task container already has, so the value lives in
  memory and dies with the container. The file is world-readable because the node cannot
  know which user your image runs as, and a bind mount keeps the node's ownership; on the
  node itself the enclosing directory is `0700`. Anything that can read a file in your
  container can read the secret, which is the same rule Docker and Kubernetes secrets follow.
- The values are resolved by the server immediately before the task is assigned, travel
  inside the `Assign` message, and exist on the node only for as long as the container runs.
  They are never written to `tasks.spec`, never returned by any API, and never logged.
- **A name that does not exist fails the task before it reaches a node**, with
  `failure_reason: missing secret "NAME"`. One missing name fails the whole task; a task
  running with half its credentials is worse than one that does not run.
- **A task's secrets are not given to its sidecars.** A sidecar that needs a credential
  still has to take it from a plaintext `env:` entry. Per-sidecar secrets are the obvious
  next step and are not implemented.

Values are redacted from the task's logs on the node, before a chunk is buffered or sent —
see [Redaction](#redaction).

### Redaction

Every secret value of 8 bytes or more, plus its base64 and URL-encoded forms, is replaced
with `[redacted:NAME]` in the task's `stdout`, `stderr` and sidecar log streams. A match
straddling a chunk boundary is caught: the node holds back up to 256 trailing bytes when
they could be the beginning of a value.

This happens on the node, so a redacted value never crosses the network at all — which also
means `podium run` shows you `[redacted:NAME]` and not the value. There is one log path and
the value does not travel it.

**Redaction is defence in depth, not a guarantee.** It is string matching. A task that
gzips its credential, prints it one character per line, hex-encodes it, or leaks it through
a length or a timing is not covered, and no redactor could cover it. The controls that
actually matter are that a value only ever reaches the node running the task that asked for
it, that it lives in a tmpfs and in process memory, and that it is never written to the
database. Redaction catches the common accident — a task echoing its environment, a client
library logging a connection URL — and should not be relied on for more.

Two limits worth knowing:

- A value shorter than 8 bytes is not redacted at all. Matching two or three bytes would
  destroy a task's output and protect nothing worth protecting.
- **A task adopted after a node restart loses its redactor**, because the values were in the
  previous incarnation's memory. The container keeps working — its environment and mounted
  files are untouched — but anything it prints from then on reaches the server unredacted.

## Sidecars

A sidecar is a sibling container on the task's private network, started before the task and
removed after it. The key is its DNS name, so the task reaches `db` at `db`.

```yaml
sidecars:
  db:
    image: pgvector/pgvector:pg16
    command: ["postgres", "-c", "fsync=off"]   # optional; the image's own by default
    env:
      POSTGRES_PASSWORD: podium
    readiness:
      command: ["pg_isready", "-U", "postgres"]   # or tcp_port: 5432 — see below
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
images have. An image with neither fails immediately with a message saying so rather than
waiting out the timeout; give it a `readiness.command` that uses a binary it does have.

That is not only `scratch` images: **the Debian-based database images have no `nc` either**,
`postgres:16` and `pgvector/pgvector:pg16` included. Use their own health command —
`pg_isready`, `redis-cli ping`, `mysqladmin ping` — which is the better probe anyway, because
`nc` succeeds as soon as the port is bound and a database binds before it will serve.

### A Docker daemon beside the task

Two sidecar fields, both off by default, exist for one shape: `docker:28-dind` as a sidecar, so
a task can run `docker compose`, build images, or use testcontainers.

```yaml
labels: [privileged]                    # see below; this is not automatic
image: docker:28-dind                   # any image with the docker CLI will do
command: ["sh", "-c", "docker compose up -d && ./run-tests"]
env:
  DOCKER_HOST: tcp://dind:2375          # so the CLI needs no -H
sidecars:
  dind:
    image: docker:28-dind
    privileged: true
    share_workspace: true
    env:
      DOCKER_TLS_CERTDIR: ""            # plain 2375, on the task's own private bridge
    readiness:
      tcp_port: 2375
      timeout: 2m                       # dockerd takes ~20s to listen
```

`privileged: true` gives that container every capability and the host's devices — **root on the
node's kernel**, which is the one thing the rest of `hardening` exists to prevent. So the spec
only *asks*. A node honours it only if its operator started it with `--allow-privileged-sidecars`
(`PODIUM_NODE_ALLOW_PRIVILEGED_SIDECARS`, off by default); a node that did not **fails the task at
provisioning**, without retrying, with a message naming the sidecar and the flag. See
[`security.md`](security.md).

**Getting the task to such a node is manual.** The scheduler matches labels and knows nothing
about which nodes allow privilege, so pair the two by hand: start the node with
`--allow-privileged-sidecars --labels privileged` and give the spec `labels: [privileged]`.
Without the pairing the task lands on whatever node has a free slot and fails there.

`share_workspace: true` mounts the task's workspace volume in the sidecar at `/workspace`, the
same path the task sees it at. **A nested daemon resolves a bind-mount source in its own
filesystem, not the task's**, so without it `docker run -v /workspace/x:/x` mounts an empty
directory the daemon invents, and a `docker compose` build context under `/workspace` is simply
not there. It needs no privilege of its own; it is documented here because docker-in-docker is
the only thing that has ever wanted it.

Two consequences of the daemon being a sibling container rather than the node's own:

- **Its image store starts empty and dies with the task.** Everything the nested daemon pulls
  is pulled again next run, into the `/var/lib/docker` anonymous volume Podium removes at
  teardown along with the container. Budget the pull time, and give the sidecar a
  `resources.memory_mb` if the stack inside it is large.
- **A port a nested container publishes belongs to the *sidecar*.** `-p 8080:80` inside dind
  binds dind's network namespace, so the task reaches it at `dind:8080`, not `localhost:8080`.

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
- a tmpfs at `/podium/secrets`, mounted `noexec,nosuid,size=1m`, which is where
  [`target: file`](#secrets) secrets land.

`capabilities` adds back a bounded set, spelled with or without the `CAP_` prefix and in any
case:

`CHOWN`, `DAC_OVERRIDE`, `FOWNER`, `SETUID`, `SETGID`, `NET_BIND_SERVICE`, `KILL`

Anything else is rejected when the spec is validated. These seven cover the things ordinary
software legitimately does — fixing up file ownership, dropping to an unprivileged user,
binding port 80 — and exclude everything that is a route out of the container.

`read_only_rootfs: true` mounts the image's filesystem read-only. `/workspace` is a volume
and stays writable; `/tmp` gets a 1 GB tmpfs, because too much software assumes it can write
there.

**There is no `/dev/shm` knob.** Every task container gets the engine's default, **64 MB**, and
a spec cannot change it. The one thing Podium runs that would plausibly want more is Chromium in
the browser agent image, and it was measured: rendering and full-page-capturing a 32 MB page
(eight 1000×1000 images, 1500 DOM nodes, thirty canvases) inside a Podium task with the default
hardening used **0 KB** of `/dev/shm`, with and without `--disable-dev-shm-usage`. Modern
Chromium on Linux prefers `memfd` for its shared buffers. So the helper at
`/opt/podium-agent/bin/screenshot` passes `--disable-dev-shm-usage` anyway — it costs nothing and
covers the engines where Chromium does fall back to `/dev/shm` — and anything writing its own
Playwright inside a task should pass it too. If some future workload genuinely needs shared
memory, adding `hardening.shm_mb` is an additive proto field (`Hardening` field 3) applied as
`HostConfig.ShmSize`; nothing needs it today.

**Sidecars are hardened less.** They get `no-new-privileges` and their own `resources`, and
nothing else: a stock database image usually chowns a data directory and drops to an
unprivileged user on the way up, which dropping every capability would break. The one dial
beyond that is a sidecar's own `privileged: true`, which only a node whose operator allowed it
will honour — see *A Docker daemon beside the task* above.

## Artifacts

A task keeps a file by putting it in one place, or by naming it.

```yaml
image: alpine:3
command: ["sh", "-c", "my-tool --out /workspace/.podium/artifacts/report.txt"]
```

**`/workspace/.podium/artifacts/` is collected when the container exits.** Every regular
file under it becomes an artifact whose name is its path relative to that directory, so
`/workspace/.podium/artifacts/shots/first.png` arrives as `shots/first.png`. The directory
does not have to exist; most tasks never create one, and that costs nothing.

It is inside the workspace volume on purpose: `/workspace` outlives any single step, a
sidecar can write there too, and a read-only rootfs (`hardening.read_only_rootfs`) leaves it
writable.

To hand a file over **during** the run, from any shell inside the container:

```sh
/podium/runner artifact add /tmp/shot.png --type image/png
/podium/runner artifact add /tmp/out.csv --name results.csv --type text/csv
```

`--name` defaults to the file's basename and `--type` to nothing, which the object store
stores as `application/octet-stream`. The helper writes one line to the node's event socket
and exits; the node copies the file out of the container and uploads it through the server.
It is not a library and needs no credential — the socket is already mounted at
`/podium/events.sock`, and the node is the only thing listening on it.

Either way the bytes go **node → server → object store**. A node never talks to S3.

Three things worth knowing:

- **An artifact is capped at 512 MB**, and the node refuses an oversized one before it
  crosses the wire.
- **A failed artifact never fails the task.** An upload that is refused — too large, object
  store down, no object store configured at all — becomes an error event that does not abort
  the run, which implies no status transition. A task does not need artifacts to run.
- **A task adopted after a node restart collects nothing.** The auto-collection pass belongs
  to the run that created the container, and an adopted run has no runner event socket
  either, so a mid-run `artifact add` is lost as well. This is the same seam the log
  redactor falls through — see [Redaction](#redaction).

`podium artifacts TASK_ID` lists them and `podium artifact get ARTIFACT_ID` downloads one;
see [docs/cli.md](cli.md).

## Egress

A task's network reaches the internet and its own sidecars, and nothing else on the host or
the tailnet. An egress allow-list is not implemented; the hook it will plug into is
`internal/node/docker/egress.go`.
