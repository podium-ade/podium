# Setting up a `podium-node`

A node is any machine with a Docker engine that runs Podium tasks. It dials the control plane,
advertises its capacity, runs containers, and streams logs back. It never listens for inbound
connections.

There are three ways to run one:

| | When | Guide |
|---|---|---|
| **`tailnet`** | The node is on a **different machine** from the server, on your tailnet. The normal case. | [Multi-machine](#multi-machine-the-tailnet-transport) below, then [docs/networking.md](networking.md) |
| **host network** | The node is on a different machine, on a network you already have (WireGuard, a corporate VPN, a LAN). Still the local token. | [docs/networking.md](networking.md#host-network-bring-your-own-routing) |
| **`local`** | Node and server share one machine. | [Single machine](#single-machine-the-local-transport) below |

The `local` transport is loopback-only by design — the server refuses to bind anything else,
because a shared static token is not an authentication system. Its one waiver,
`PODIUM_LOCAL_ALLOW_UNSAFE_LISTEN`, has two sanctioned uses: a **container**, where loopback is
the container's own and the published port is the boundary, and a **host-network deployment**,
where the boundary is a network you already trust. A TCP relay in front of the loopback
listener is the same exposure by another route and is not a substitute for either.

Multi-machine with identity is the tailnet transport, which has now run against a real
tailnet: a `tag:podium-node` worker enrolled with no bearer token anywhere and ran a
linux/amd64 task with live logs and its exit code. What that run did **not** prove is the ACL
— read [`networking.md`](networking.md#the-acl) before relying on the network to refuse
server → node.

**A remote `podium-node` pointed at `http://<host>:8080` only works if the server opted into
host network.** Otherwise the listen address is loopback and there is nothing to connect to.

## Requirements

| | |
|---|---|
| Docker Engine | API **1.43+**, **cgroup v2**. The daemon refuses to start on cgroup v1 — resource limits would be unreliable. |
| Disk | `data_dir` and the image cache are I/O heavy. Put them on fast local storage, never a network share. |
| Network | Outbound to the control plane. **No inbound ports.** |
| Privileges | Access to the Docker socket, which is root-equivalent on that host. |

Check the engine before you start:

```sh
docker info --format 'cgroup v{{.CgroupVersion}}  api {{.ServerVersion}}  {{.Architecture}}'
```

## As a container, or as a binary

A worker is `podium-node` plus a Docker socket, and it runs either way.

**As a container**, from `ghcr.io/podium-ade/podium-node`. Beside the control plane that is the
compose file's `node` profile, and there is nothing to install:

```sh
echo "PODIUM_NODE_ENROLL_TOKEN=$(docker compose run --rm cli \
  node enroll-token --label demo)" >> .env
docker compose --profile node up -d
```

Two mounts are not optional, and both follow from the node driving the **host's** Docker daemon
rather than one of its own:

| mount | why |
|---|---|
| `/var/run/docker.sock:/var/run/docker.sock` | the daemon it runs tasks on. **Root-equivalent on that host**: anything that can talk to this socket can start a privileged container and own the machine. See [`security.md`](security.md) |
| `/var/lib/podium-node:/var/lib/podium-node` | the data directory, at the **same absolute path** inside the container and out. Every bind mount the node asks for — including the `podium-runner` that is PID 1 in every task container — is resolved by the host's daemon against the host filesystem, so from a named volume every task dies at creation with `bind source path does not exist` |

That second one has a consequence worth knowing: the directory outlives `docker compose down
-v`. A fresh control plane against an old data directory leaves the node looping on
`unauthenticated: unknown node key` forever rather than failing, because `identity.json` is
still there so it never reads the new enrollment token.

On a machine of its own the control plane has to be reachable, which on the `local` transport
it is not — that transport is loopback-only. So a standalone container worker means a tailnet
control plane, and the node needs its own Tailscale state and auth key:

```sh
docker run -d --name podium-node --restart unless-stopped \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -v /var/lib/podium-node:/var/lib/podium-node \
  -e PODIUM_NODE_SERVER=https://podium.<tailnet>.ts.net \
  -e PODIUM_NODE_TRANSPORT=tailnet \
  -e PODIUM_NODE_TS_AUTHKEY=tskey-auth-... \
  -e PODIUM_NODE_ENROLL_TOKEN=$TOKEN \
  -e PODIUM_NODE_LABELS=linux/amd64 \
  ghcr.io/podium-ade/podium-node:v0.1.0
```

> **Unverified.** A containerised node on a tailnet is not a tested path; the `node` profile
> beside a control plane is. If you are standing up a dedicated worker, prefer the binary
> below.

**As a binary** is what [`install-node.sh`](../deploy/install-node.sh) installs and what the
rest of this document describes. It is the better answer for a dedicated worker: it verifies
the download against the release's `checksums.txt` before unpacking, and runs under a hardened
systemd unit — neither of which a `docker run` line gives you.

## Which binary — architecture

**The server and its nodes do not have to share an architecture.** A darwin/arm64 control plane
drives linux/amd64 workers perfectly well. **linux/amd64 is the expected default for workers**;
`make build` is host-native, so on a Mac it produces a binary a Linux worker cannot run.

```sh
uname -m            # on the worker. x86_64 -> amd64 ; aarch64 -> arm64
```

```sh
# on the machine with the source
make dist-node GOOS=linux GOARCH=amd64     # -> bin/podium-node-linux-amd64, bin/podium-linux-amd64
make dist-node GOOS=linux GOARCH=arm64     # -> bin/podium-node-linux-arm64, bin/podium-linux-arm64
make dist-node-all                         # both

scp bin/podium-node-linux-amd64 worker:/usr/local/bin/podium-node
scp bin/podium-linux-amd64      worker:/usr/local/bin/podium       # the CLI is handy on a worker too
```

The binaries are static (`CGO_ENABLED=0`) and run on any glibc or musl Linux. `podium-node`
carries `podium-runner` for both Linux architectures inside itself and bind-mounts the right one
into every task container, so there is nothing else to install on a worker and nothing for a task
image to provide.

**A task container's image architecture is a different question.** A task running `alpine:3` on a
linux/amd64 node pulls the amd64 image; on an arm64 node, the arm64 one. Multi-arch images make
that invisible, but an image pinned to one architecture only runs on matching nodes — which is
what labels are for. Label your workers `linux/amd64` and have such specs require it.

## Multi-machine: the tailnet transport

The full picture, including the ACL and what to create in the Tailscale admin console, is in
**[docs/networking.md](networking.md)**. The short version:

**Two different credentials are involved and people mix them up constantly:**

- the **Tailscale auth key** (`tskey-auth-…`) — network level, reusable, tagged
  `tag:podium-node`, minted in the Tailscale admin console. It gets the daemon onto your tailnet.
- the **Podium enrollment token** — Podium level, single-use, minted by your control plane with
  `podium node enroll-token`. It tells Podium which node this is.

You need both, and neither substitutes for the other.

### 1. Once per tailnet

Enable **MagicDNS** and **HTTPS Certificates** (admin console → DNS), apply
[`deploy/tailscale-acl.example.json`](../deploy/tailscale-acl.example.json), and mint a
**reusable, pre-approved** auth key tagged `tag:podium-node`.

### 2. Mint an enrollment token, from anywhere on the tailnet

No token, no login: Tailscale identifies you.

```sh
TOKEN=$(podium --server https://podium.<tailnet>.ts.net \
  node enroll-token --label linux/amd64 --label browser)
```

### 3. Start the node on the worker

Six variables, one per line — the four you have to supply yourself are marked:

```sh
export PODIUM_NODE_SERVER=https://podium.<tailnet>.ts.net  # YOURS: the control plane's MagicDNS name
export PODIUM_NODE_TS_AUTHKEY=tskey-auth-...               # YOURS: Tailscale auth key, tag:podium-node
export PODIUM_NODE_ENROLL_TOKEN=...                        # YOURS: from step 2. First run only
export PODIUM_NODE_LABELS=linux/amd64                      # YOURS: what tasks match on. Comma-separated
export PODIUM_NODE_TRANSPORT=tailnet                       # fixed for this setup
export PODIUM_NODE_DATA_DIR=/var/lib/podium-node           # default; must be local disk and must persist

podium-node
```

The daemon joins the tailnet as `podium-node-<hostname>`, dials the server's MagicDNS name over
it, and **listens for nothing**. Confirm that last part:

```sh
ss -ltnp        # only 127.0.0.1:9091 (metrics) should be Podium's
```

### 4. The node is pinned to that machine

The Tailscale device the node enrolled from is recorded, and every later connection must come
from the same device — so a copied `identity.json` is useless elsewhere. When a worker is
genuinely rebuilt or replaced:

```sh
podium --server https://podium.<tailnet>.ts.net node rekey node_01j…
```

The node keeps its ID, labels and history; the next connection binds it to the new device.

## Single machine: the local transport

### 1. Mint an enrollment token (on the control plane)

Tokens are single-use, expire in 1h by default, and are shown exactly once — the server stores
only their SHA-256.

```sh
TOKEN=$(podium --server http://127.0.0.1:8080 --token "$PODIUM_LOCAL_TOKEN" \
  node enroll-token --label linux/arm64 --label browser)
```

`enroll-token` prints the token to **stdout and nothing else**, so `$( )` captures it cleanly; the
expiry note goes to stderr. Repeat `--label` for each label. Labels decide what the node is
eligible for: a task whose spec lists `labels: [browser]` only ever goes to a node advertising
`browser`. They are not fixed at enrollment — see
[Changing a node's labels](#changing-a-nodes-labels).

You can also mint one from the web UI under **Nodes → Add a node**, which hands you the whole
command with the token filled in.

### 2. Start the node

**In a checkout, you already have this.** `deploy/.env` holds the server's
`PODIUM_LOCAL_TOKEN`, and `make stack-up` derives the node's copy from it:

```sh
echo "PODIUM_NODE_ENROLL_TOKEN=$TOKEN" >> deploy/.env
make stack-up S=node
```

The rest of this section is the same worker run by hand, which is what a machine with no
checkout on it does. Environment-only is a supported deployment — no config file needed:

```sh
export PODIUM_NODE_SERVER=http://127.0.0.1:8080   # the control plane, on this same machine
export PODIUM_NODE_LOCAL_TOKEN=devtoken             # YOURS: must equal the server's PODIUM_LOCAL_TOKEN
export PODIUM_NODE_ENROLL_TOKEN=...               # YOURS: from step 1. First run only
export PODIUM_NODE_TRANSPORT=local                # fixed for this setup
export PODIUM_NODE_DATA_DIR=/var/lib/podium-node  # default; must persist

podium-node
```

On first run the node exchanges the enrollment token for a permanent identity and writes it to
`$PODIUM_NODE_DATA_DIR/identity.json` with mode `0600`. After that the token is spent and no
longer needed — drop it from the environment.

> **`identity.json` contains `node_key`, a real credential.** The server returns it exactly once
> and keeps only its SHA-256, so there is no recovery path: lose it and you need a new enrollment
> token. Under the tailnet transport `<data_dir>/ts/` holds the node's Tailscale device identity
> and is equally durable and equally sensitive. Do not put `data_dir` inside a git repository
> (`.gitignore` guards the obvious spellings) and do not copy it between machines — two daemons
> sharing one identity will fight over the same session, and over the tailnet the device binding
> refuses the copy outright.

### 3. Verify

```sh
podium --server http://127.0.0.1:8080 --token "$PODIUM_LOCAL_TOKEN" nodes
```

```
NAME              ID                     STATUS   LABELS           RUNNING/MAX   HEARTBEAT
worker-1          node_01m1j889e944...   online   browser          0/4           0s ago
```

`online` means the node holds an open stream. Then run something on it:

```sh
podium --server http://127.0.0.1:8080 --token "$PODIUM_LOCAL_TOKEN" \
  run --image alpine:3 -- sh -c 'echo hello from $(hostname)'
```

The node also serves, on `127.0.0.1:9091` by default:

- `/healthz` — the process is up
- `/readyz` — the control-plane stream is connected
- `/metrics` — `podium_node_running_tasks`, `podium_node_free_slots`, `podium_node_stream_connected`

## Configuration reference

`/etc/podium/node.yaml` (or `--config PATH`), overlaid by `PODIUM_NODE_*` environment variables. An
unset or empty variable leaves the file's value alone, so a file and a partial environment compose.

| Env | YAML | Default | Meaning |
|---|---|---|---|
| `PODIUM_NODE_SERVER` | `server` | `http://127.0.0.1:8080` | Control plane base URL |
| `PODIUM_NODE_TRANSPORT` | `transport` | `local` | `local`, `tailnet` or `host` |
| `PODIUM_NODE_LOCAL_TOKEN` | `local_token` | — | `local` only. Must equal the server's `PODIUM_LOCAL_TOKEN` |
| `PODIUM_NODE_TS_AUTHKEY` | `ts_auth_key` | — | `tailnet` only. The **Tailscale** auth key; `TS_AUTHKEY` is honoured as a fallback. First run only |
| `PODIUM_NODE_TS_HOSTNAME` | `ts_hostname` | `podium-node-<hostname>` | `tailnet` only. The Tailscale device name |
| `PODIUM_NODE_ENROLL_TOKEN` | `enroll_token` | — | First run only |
| `PODIUM_NODE_DATA_DIR` | `data_dir` | `/var/lib/podium-node` | Identity + per-task state |
| `PODIUM_NODE_LABELS` | `labels` | — | Comma-separated in env, a list in YAML. **Read at enrollment only** — an already-enrolled node is relabelled from the control plane, see [Changing a node's labels](#changing-a-nodes-labels) |
| `PODIUM_NODE_MAX_TASKS` | `max_tasks` | `4` | Concurrency budget. **`0` means the node never gets work.** An operator can override it from the control plane — see [Changing a node's slots](#changing-a-nodes-slots) |
| `PODIUM_NODE_METRICS_LISTEN` | `metrics_listen` | `127.0.0.1:9091` | Health and metrics |
| `PODIUM_NODE_DOCKER_HOST` | `docker_host` | — | Engine endpoint; empty uses the normal Docker resolution |
| `PODIUM_NODE_IMAGE_CACHE_PRUNE` | `image_cache_prune` | `false` | Turns the image cache prune on. **Off by default — read the section below before turning it on.** |
| `PODIUM_NODE_IMAGE_CACHE_HIGH_WATERMARK` | `image_cache_high_watermark` | `0.80` | Disk-usage fraction above which the node stops accepting work, and prunes if pruning is enabled |
| `PODIUM_NODE_EXIT_ON_DRAIN` | `exit_on_drain` | `false` | Exit 0 once drained and the last task has finished. `--exit-on-drain` is the flag form |
| `PODIUM_NODE_ALLOW_PRIVILEGED_SIDECARS` | `allow_privileged_sidecars` | `false` | Honour a spec's `privileged: true` on a sidecar, which is **root on this machine's kernel**. `--allow-privileged-sidecars` is the flag form. Pair it with a label and dedicate the node — see [security.md](security.md) |

### Private registries

A node has no registry credentials of its own. A login for a private registry — Google Artifact
Registry, GHCR, anything `docker login` takes — is stored once on the control plane, on the
**Registries** screen of the web UI, and arrives on whichever node a task lands on inside that
task's assignment, for the registries its images actually use. See
[task-spec.md](task-spec.md#private-registries). Nothing needs configuring here.

### Draining a node

`podium node drain NODE` stops the control plane giving a node new work; whatever it is
already running finishes normally. The instruction is stored on the node's row, so it
survives both daemons restarting and can be set on a node that is offline right now.
`podium node undrain NODE` puts it back.

A node started with `--exit-on-drain` exits 0 once its last task finishes, which is what an
upgrade wants: drain, wait, let the supervisor start the new binary. Without the flag the
daemon stays connected and idle, which is what maintenance wants.

```sh
podium node drain worker-3        # from anywhere with a CLI
# … wait for `podium nodes` to show 0 running …
# the daemon exits 0; systemd restarts it on the new binary
```

### Changing a node's slots

`max_tasks` above is what this machine is configured for. `podium node slots NODE COUNT`
overrides it from the control plane, in both directions, without touching the node's file or
restarting anything:

```sh
podium node slots worker-3 8      # this box can take more than its file says
podium node slots worker-3 2      # it is thrashing; give it less
podium node slots worker-3 0      # back to whatever max_tasks says
```

The same number is on the node's page in the web UI (**Nodes** → the node's name).

Three things are worth knowing about it:

- **It is stored against the node**, like `draining`. It survives both daemons restarting,
  and it can be set on a node that is offline right now — every stream is sent the count just
  after its reconciliation reply, so the node picks it up on its next connection.
- **The node enforces it, not just the scheduler.** A node rejects an assignment it has no
  slot for, so a raise that only reached the scheduler would produce work the node refuses.
  The control plane holds the number and sends it on every stream; the daemon keeps none of
  it, which is why `max_tasks` in the file is what a node that has never been told anything
  runs on.
- **Lowering it takes nothing down.** Tasks already running finish normally, and the node
  accepts nothing new until enough of them have — the same shape as a drain.

`podium nodes` marks an overridden count with `*`, so a machine configured for 4 and capped
at 2 never reads as a machine with two slots.

### Changing a node's labels

`PODIUM_NODE_LABELS` is read once, at enrollment. `podium node label NODE` changes the set
afterwards, from the control plane, without touching the node's configuration or restarting
anything:

```sh
podium node label worker-3 --add monorepo      # this box has the checkout; pin work to it
podium node label worker-3 --remove browser    # it no longer has a display
```

Labels are added and removed rather than replaced, so two operators tagging different things
cannot clobber each other. The stored set is sorted and deduplicated, exactly as enrollment
leaves it, and `podium nodes` prints it.

A node that is connected is retagged on the spot — the labels the scheduler matches on live
on the node's session, and this changes them there as well as on the row, so work routes on
the new set from the scheduler's next tick. A node that is offline is relabelled just the
same, and reads the change on its next connection: the row is what a stream copies its labels
from, and the `labels` a node re-advertises in its Hello never overwrite it.

Nothing is taken away from a task already running, whichever label went.

### The image cache — pruning is off by default, and why

A node's Docker engine is not Podium's. On a developer's laptop it holds their own images;
on a shared build host it holds someone else's. So the LRU prune is **opt-in**
(`image_cache_prune: true`), and even when it is on it can only ever remove an image
**Podium itself pulled**.

The record is `data_dir/images.json`, written at the moment of a successful pull. It is the
LRU bookkeeping *and* the allow-list: the one function in the tree that calls Docker's image
remove refuses anything absent from it, so an image that arrived some other way is not a
candidate however old it is and however full the disk gets. Podium never runs `docker system
prune`, `docker image prune`, or any other bulk removal.

What the watermark does when pruning is **off** — which is the default — is make the node
advertise zero free slots while the data dir's filesystem is above it. A machine that cannot
fit another image cannot reliably start another task, and refusing the work is more honest
than accepting it and failing at the pull. The scheduler stops assigning to it and says so in
the queued task's reason.

With pruning on, the node also removes its own least recently used images until the disk is
ten points below the watermark, never touching one a running task needs.

```yaml
image_cache_prune: true
image_cache_high_watermark: 0.85
```

Equivalent config file:

```yaml
# /etc/podium/node.yaml — a tailnet worker
server: https://podium.tail0a1b2c.ts.net
transport: tailnet
data_dir: /var/lib/podium-node
labels: [linux/amd64, browser]
max_tasks: 4
ts_auth_key: ""      # or leave to PODIUM_NODE_TS_AUTHKEY / TS_AUTHKEY; first run only
enroll_token: ""     # first run only
```

```yaml
# /etc/podium/node.yaml — a worker beside the server, local transport
server: http://127.0.0.1:8080
transport: local
data_dir: /var/lib/podium-node
labels: [linux/arm64, browser]
max_tasks: 4
local_token: ""      # or leave to PODIUM_NODE_LOCAL_TOKEN
enroll_token: ""     # first run only
```

`--log-level` takes `debug`, `info`, `warn` or `error`.

## Operating notes

**One node per Docker engine.** At startup the daemon claims containers by the `podium.task`
label across the whole engine, whichever daemon created them, and tears down the ones its own
control plane does not recognise. Two `podium-node` processes on one host therefore destroy each
other's work. Nothing enforces this — don't do it.

**That includes a test run.** `make test-integration` and `make e2e` start real `podium-node`
processes against the host's engine, wired to their own throwaway control plane. Run either
beside a live node and both sides lose their containers — which looks like flakiness or memory
pressure, and is not. Stop the node first.

**Restarts are safe.** SIGTERM leaves running containers alone; on restart the node re-adopts them
via their labels, resumes their log streams where the server last acked, and finishes them. A brief
network loss is also safe: events are buffered (8 MB per task) and replayed, so logs have no gap.

**Cancelling is prompt.** `/podium/runner` is PID 1 in a task container — `/proc/1/cmdline` reads
`/podium/runner -- sh -c …` — and it forwards `SIGTERM` to the task command as an ordinary
process, so the kernel does not discard it the way it discards a default-disposition signal sent
to a namespace's init. An untrapped `sleep 300` is cancelled in **about a second**, exits **143**
and the task's status is `cancelled`. The 30s grace before `SIGKILL` is there for a command that
traps `TERM` and needs the time.

**Task isolation.** Each task gets its own bridge network `podium-<task_id>` and a workspace volume
mounted at `/workspace`, both removed at teardown. Task containers have internet egress but no
access to the Docker socket or to other tasks.

## Troubleshooting

The daemon tries to fail with an actionable message. The common ones:

| Message | Fix |
|---|---|
| `server is empty` | Set `PODIUM_NODE_SERVER` |
| `local_token is empty` | Set `PODIUM_NODE_LOCAL_TOKEN` to the server's `PODIUM_LOCAL_TOKEN` |
| `transport tailnet dials "http://…"` | A tailnet server serves HTTPS: use its `https://podium.<tailnet>.ts.net` URL |
| `transport dev dials "https://…"` | An https:// control plane means `transport: tailnet` |
| `holds no Tailscale device yet and neither PODIUM_NODE_TS_AUTHKEY nor TS_AUTHKEY is set` | First run needs the **Tailscale** auth key, not the enrollment token |
| `this device is not tagged as a Podium node` | The auth key was not tagged `tag:podium-node` |
| `is bound to another Tailscale device` | This node key belongs to a different machine. If the move is deliberate: `podium node rekey NODE_ID` |
| `more than 5 attempts in 1m0s from this address` | The enrollment rate limit. Wait a minute |
| `data_dir ... is not writable` | Fix ownership or point it somewhere writable |
| `max_tasks is 0` | Set it to 1 or more, or the scheduler will never pick this node |
| `no identity ... and no enrollment token` | Mint a token and pass `PODIUM_NODE_ENROLL_TOKEN` |
| `token already used` | Enrollment tokens are single-use — mint a fresh one |
| `unauthenticated: stream: unknown node key` | The `data_dir` holds an `identity.json` from a **different control plane**, and a stored identity always wins over a supplied `PODIUM_NODE_ENROLL_TOKEN`. Delete `<data_dir>/identity.json` and start again. The node says `ignoring the supplied enrollment token` at info level when this happens; without that line the only symptom is a reconnect loop with no `node enrolled` in it |
| cgroup v1 / old API error at startup | Upgrade the Docker engine |

Node shows `online` but never gets tasks: check `max_tasks` is not 0, that no slot count of
its own has been set from the control plane (`podium nodes` marks one with `*`), and that the
task's `labels` are a subset of the node's (`podium nodes` prints them).
