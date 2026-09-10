<p align="center">
  <img src="docs/assets/podium-logo.png" alt="" width="200">
</p>

<h1 align="center">Podium</h1>

<p align="center">
  <b>Run containerised tasks on machines you own, from anywhere, with one command.</b>
</p>

<p align="center">
  <a href="LICENSE"><img alt="MIT licence" src="https://img.shields.io/badge/licence-MIT-3b2fd4.svg"></a>
  <a href="go.mod"><img alt="Go 1.27+" src="https://img.shields.io/badge/go-1.27%2B-00ADD8.svg"></a>
  <a href=".github/workflows/ci-go.yml"><img alt="ci / go" src="https://github.com/podium-ade/podium/actions/workflows/ci-go.yml/badge.svg"></a>
  <a href=".github/workflows/ci-web.yml"><img alt="ci / web" src="https://github.com/podium-ade/podium/actions/workflows/ci-web.yml/badge.svg"></a>
</p>

A control plane (`podium-server`) schedules work and serves a web UI. A daemon (`podium-node`)
on each worker runs tasks with the local Docker engine and streams their logs back live. A CLI
(`podium`) submits and follows them, and exits with the task's own exit code.

```sh
podium run --image alpine:3 -- sh -c 'for i in 1 2 3; do echo tick $i; sleep 1; done; exit 3'
```

```
→ task task_01m1j88gv0sg8xaxawdh1fqm0z
→ scheduled on node_01m1j889e944prdn29s2x2d6pa
→ running
tick 1
tick 2
tick 3
→ finished exit 3 in 3.2s
```

> **No tagged release yet.** There is no binary to download and no image to pull — building
> from source is the only way in, and the [Quickstart](#quickstart) below is the whole of it.
> Read [Limitations](#limitations) before putting Podium anywhere that matters.

---

## Why

Three machines under your desk, or three in a rack, or one laptop and two boxes on a shelf.
You want to run something on them — a scrape, a build, a browser, a batch job — and you want to
watch it happen, get its exit code, and keep what it produced.

Podium is that, and deliberately not much more:

- **Workers dial out.** A node opens one stream to the control plane and listens for nothing.
  No inbound port, no firewall rule, no address for the control plane to know. Adding a worker
  on another continent is the same operation as adding one on the same desk.
- **A task is one container run**, on one machine, to one exit code — with its own private
  network, a fresh workspace volume, optional sidecars started and waited for, resource limits
  that are actually enforced, and secrets that are encrypted at rest and shredded afterwards.
- **You see it happen.** Logs stream live to the CLI and the browser, byte-exact across a
  server restart or a node reconnect.
- **It cleans up after itself.** Containers, networks, volumes, secret files. A clean run
  leaves nothing behind.

## Why not

Read [What a task is *not*](docs/concepts.md#what-a-task-is-not-in-this-version) before you
start. Podium has **no pipelines, no cron, no task retries, no build cache, no interactive
exec**, and a task cannot span two machines. If you want a pipeline, the shell inside your
container is the pipeline.

---

## Quickstart

One file, one command. Save [`deploy/docker-compose.yml`](deploy/docker-compose.yml) anywhere
and run:

```sh
docker compose up -d --wait
open http://127.0.0.1:8080          # the token is `podium`
```

That is the whole procedure. Postgres, an object store, the control plane and the conductor,
with the web UI on 8080 and the **Agent** screen already wired. No Go toolchain, no Node, no
binary on the host, no second file, and no `.env` to write first — the Postgres bootstrap
script is inline in the compose file, the conductor's profile directory ships inside its
image, and the master key is generated into a volume on first `up`.

> **It ships with default credentials**, which is what makes that one command possible. Three
> of the four are only reachable inside the compose network. The fourth, `PODIUM_LOCAL_TOKEN`,
> is the only thing between a caller and the whole API — and 8080 is published, on `127.0.0.1`
> alone, so the exposure is anyone on that machine. For anything you would miss, write a
> `.env` beside the compose file **before the first `up`** and override them:
>
> ```sh
> printf 'PODIUM_LOCAL_TOKEN=%s\nPODIUM_PG_PASSWORD=%s\n' \
>   "$(openssl rand -hex 32)" "$(openssl rand -hex 16)" > .env
> ```
>
> `PODIUM_PG_PASSWORD` is baked into the Postgres volume when it is initialised, so changing
> it later means `down -v` or an `ALTER ROLE`.

**A worker.** Tasks need one, and it is the one thing that stays opt-in: it mounts the host's
Docker socket, which is **root-equivalent on that host** — read
[docs/security.md](docs/security.md) before putting one anywhere real. Without it a submitted
task sits in `queued` and says why.

```sh
echo "PODIUM_NODE_ENROLL_TOKEN=$(docker compose run --rm cli \
  node enroll-token --label demo)" >> .env
docker compose --profile node up -d
```

The enrollment token is the one value that cannot be written ahead of time: only a running
control plane can mint one, and it is single-use — a worker that has enrolled has
`identity.json` and never reads it again. For a worker on *another* machine the `local`
transport is the wrong tool, being loopback-only; see
[Running across machines](#running-across-machines).

**Run something.** The `cli` profile is the CLI as a one-shot, so there is no binary to
install; `docker compose run` turns the profile on by itself.

```sh
docker compose run --rm cli run --image alpine:3 -- echo hello
```

The CLI exits with the task's exit code, which is what makes it usable as a CI step. Podium's
own commentary goes to stderr with a `→`, so redirecting stdout captures exactly the task's.

A task can bring its own environment with it. A `--spec` is read by the CLI, so mount it where
the container can see it:

```sh
docker compose run --rm -v "$PWD/specs:/specs:ro" cli run --spec /specs/postgres-sidecar.yaml
```

```
→ task task_01m1jsfzne7c8v1p1n4h4rjkq3
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

A sidecar is a sibling container on the task's private network, addressed by name — `psql -h
db` — started before the task and waited for. More in [`examples/`](examples): `hello.yaml`,
`postgres-sidecar.yaml`, `secrets.yaml`, `limits.yaml`, `artifacts.yaml`.

**Back up the `server-state` volume.** It holds the master key every stored secret is
encrypted under, and there is no recovery path:

```sh
docker compose cp server:/var/lib/podium/master.key ./master.key
```

Full walkthrough, including tearing it down: **[docs/quickstart.md](docs/quickstart.md)**.

> **The `ghcr.io/podium-ade/*` tags do not exist until a `v*` tag is pushed.** Until then,
> build the four images and set `PODIUM_IMAGE_REPO` to a registry you can reach — the recipe is
> in [docs/quickstart.md](docs/quickstart.md#building-the-images-yourself). To work *on* Podium
> rather than run it, [CONTRIBUTING.md](CONTRIBUTING.md) has the source-built stack.

### The agent layer

The conductor comes up with everything else. It is an ordinary API client of `podium-server` —
its own database, its own token, and it never touches Docker — and it turns a Slack mention, a
Linear assignment or a web-chat message into one turn. The UI's **Agent** screen is its front
end, reverse-proxied behind the server's identity middleware so there is one origin and one
login. The playbook a turn runs is the worked example baked into the agent image at
`/etc/podium/agent`; mount your own profile directory over it to replace it.

Two things it does not have out of the box, and both are credentials:

- **A model key.** A turn needs an Anthropic key, and it is set in the UI rather than in
  `.env` on purpose — that way it lands in the encrypted secret store instead of in
  `docker inspect` output.
- **Shared memory.** Hindsight is behind the `memory` compose profile, and not by preference:
  it exits at boot without an Anthropic key of its own, for fact extraction, and a paid
  third-party credential is the one thing a compose file cannot default. Without it the
  conductor runs with no memory at all, which is supported — briefs carry no memory block and
  nothing is retained.

  ```sh
  printf 'PODIUM_MEMORY_LLM_API_KEY=sk-ant-...\nPODIUM_AGENT_MEMORY_URL=http://hindsight:8888\n' >> .env
  docker compose --profile memory up -d
  ```

  Turning it on has a networking consequence worth reading before you do: the memory port is
  published on loopback by default, but an agent turn runs in a task container that reaches
  the host through the bridge gateway, which loopback is not reachable from. Widening it means
  setting `PODIUM_MEMORY_BIND` — and a real `PODIUM_AGENT_MEMORY_API_KEY` at the same time,
  because Hindsight has no authentication beyond that key. See
  [docs/agent.md](docs/agent.md) and [docs/security.md](docs/security.md).

## The binaries

Each of the first four is also a published image — `ghcr.io/podium-ade/podium-server`,
`-node`, `-agent`, and `ghcr.io/podium-ade/podium` for the CLI — and that is the way in. They
are single static Go binaries on a distroless base, so an image is the binary and a
certificate bundle and nothing else. The release archive has the same binaries loose, for a
host that would rather run them directly.

| | |
|---|---|
| `podium-server` | API, scheduler, node registry, secrets, log ingest, embedded web UI. Needs Postgres (`pgvector/pgvector:pg16`); optionally an S3-compatible store |
| `podium-node` | One per worker. Runs tasks on the local Docker engine. **Root-equivalent on its host** — read [security.md](docs/security.md) |
| `podium-agent` | The conductor. Turns a Slack mention, a Linear assignment or a web-chat message into one turn — a conversation answered on its own host, or a task running an agent runtime image — and relays the answer back. An ordinary API client of `podium-server`: its own database, its own token, never touches Docker. See [docs/agent.md](docs/agent.md) |
| `podium` | The CLI. Talks only to the server, never to Docker, so it runs anywhere |
| `podium-runner` | PID 1 inside every task container: runs the command, forwards signals, reaps orphans, reports events. Embedded in `podium-node` and bind-mounted in; never installed by hand |

## Transports

`PODIUM_TRANSPORT` decides how clients and workers reach the control plane. The two supported
values differ on one thing — who names the caller — and everything else follows from it.

| | `local` | `tailnet` |
|---|---|---|
| The wire | HTTP on loopback | HTTPS on the server's MagicDNS name |
| Who the caller is | nobody. One shared bearer and no identity behind it | a Tailscale identity, from `WhoIs` |
| What you present | `PODIUM_LOCAL_TOKEN` — from the CLI, the browser and every node | nothing. There is no token to hold |
| Where a worker can be | the same machine | anywhere on your tailnet |
| Set up | the [Quickstart](#quickstart) | [Running across machines](#running-across-machines), below |

The `local` transport refuses to bind anywhere but loopback, because that one token is the only
thing between a caller and the whole API. It is for one machine you are sitting at, and it is
what the Quickstart runs. Anything else is `tailnet`, including a second machine on the same
desk — see below.

There is a third value, `host`, which serves the same HTTPS over the machine's existing
`tailscaled` rather than an embedded device. It has never been run.

## Running across machines

**The tailnet transport is the only supported way to reach a worker on another machine** — in
development as much as in production, and not merely the recommended one. Podium joins your
Tailscale network: the server serves HTTPS on its MagicDNS name, workers dial out, and there is
no login page, no API token and no public ingress.

```sh
docker run --rm -v "$PWD:/out" --user "$(id -u):$(id -g)" \
  ghcr.io/podium-ade/podium-server:latest \
  init --dir /out --transport tailnet --tailnet <magicdns-suffix>
$EDITOR .env                            # paste TS_AUTHKEY; init reports what else is missing
docker compose -f docker-compose.tailnet.yml up -d --wait

# from any device on the tailnet — no token, no login
podium --server https://podium.<tailnet>.ts.net nodes
```

That last line is a CLI on your own machine, not in a container: the tailnet compose file has
no `cli` profile, because the server listens on port 443 of its own Tailscale device and has no
address on the compose network for a sibling container to reach.

Read **[docs/networking.md](docs/networking.md)** first: what to create in the Tailscale admin
console, the ACL, and the two different keys involved (a Tailscale auth key and a Podium
enrollment token are not the same thing). Then **[docs/node-setup.md](docs/node-setup.md)** for
the worker at the other end.

## What happens when things go wrong

The control plane places work on the node with the most free slots that carries every label the
task asks for and has room for its CPU and memory — its sidecars' included. A task it cannot
place stays `queued` and says why.

After that it keeps the promises placement made:

- a node that takes an assignment and does not acknowledge it within 15 seconds loses it;
- a task that outruns its `timeout` is stopped and ends `failed{reason: timeout}`;
- a task that exceeds `resources.memory_mb` is OOM-killed and reported as such, not swapped;
- a node that stops heartbeating is `unreachable` at 30 seconds and `offline` at 120, at which
  point its tasks are requeued (`retry_on_node_loss: true`) or marked **`lost`** — which is not
  `failed`: nothing about the task went wrong, its machine went away;
- a node that comes back is told what the control plane actually holds for each container it
  still has, so its logs resume at the right byte, and any container the control plane has
  written off is torn down instead of being left running.

```sh
podium node drain worker-3      # finishes what it has, takes nothing new
podium node undrain worker-3
podium node slots worker-3 2    # or just turn it down: 2 tasks at once, 0 to undo
podium node rm worker-3         # once it is drained and idle
```

A node started with `--exit-on-drain` exits 0 when its last task finishes, which is the upgrade
path. A slot count is the softer version of a drain: like draining it is stored against the node
and survives both daemons restarting, and it goes up as well as down — the number is sent to the
node, because a node enforces its own budget and rejects work it has no slot for.

---

## Documentation

**Start here**

- **[docs/quickstart.md](docs/quickstart.md)** — a control plane, a worker and a first task
- **[docs/concepts.md](docs/concepts.md)** — the five nouns, and what a task is *not*
- **[docs/security.md](docs/security.md)** — the trust model. Read before putting a node anywhere real

**Using it**

- [docs/task-spec.md](docs/task-spec.md) — every spec field: secrets, sidecars, readiness, limits, hardening, artifacts
- [docs/cli.md](docs/cli.md) — every command, its exit codes and its streams (contractual)
- [examples/](examples) — hello, sidecar, secrets, limits, artifacts

**Running it**

- [docs/operations.md](docs/operations.md) — backup, restore, upgrade, drain, metrics, what to do when something is wrong
- [docs/storage.md](docs/storage.md) — Postgres, the object store, a worker's data dir, the image cache
- [docs/networking.md](docs/networking.md) — the tailnet transport, identity, the ACL, troubleshooting
- [docs/node-setup.md](docs/node-setup.md) — setting up a worker
- [docs/agent.md](docs/agent.md) — the conductor (`podium-agent`): the Slack bot, profiles and playbooks, how a turn works, the agents' shared memory
- [deploy/README.md](deploy/README.md) — compose, the installer, the systemd unit
- [deploy/.env.example](deploy/.env.example) — every `PODIUM_*` variable, commented

**Internals**

- [docs/protocol.md](docs/protocol.md) — the node↔server stream, event ordering, acks, reconciliation
- [docs/runner-events.md](docs/runner-events.md) — `podium-runner` as PID 1 and its event socket
- [CONTRIBUTING.md](CONTRIBUTING.md) — dev setup, house rules, the things that will confuse you

---

## Configuration

Every daemon is configured entirely by environment, and **one file is the whole of it**: the
`.env` that `podium-server init` writes beside the compose file. The compose files interpolate
it, and `set -a; . .env; set +a` configures a host CLI from the same lines. Nothing here asks
you to declare a variable anywhere else.

[`deploy/.env.example`](deploy/.env.example) is the annotated version of that file — copy it
instead of running `init` if you would rather choose your own credentials. Working from a
clone, it is `deploy/.env`, which `make stack-up` sources before starting a host binary.

`.env.example` documents every variable there is, with its default, and `go test ./deploy/...`
fails the build if the code reads one that file does not mention, or if that file documents
one nothing reads any more.

The four you cannot skip — though `make stack-up` derives the first from `PODIUM_PG_PASSWORD`
and the third from the file's own directory:

| | |
|---|---|
| `PODIUM_DATABASE_URL` | the Postgres DSN. The server migrates on start |
| `PODIUM_LOCAL_TOKEN` | the `local` transport's shared bearer token |
| `PODIUM_MASTER_KEY_FILE` | the AES-256 key secrets are encrypted under. Mode 0600/0400 enforced. Unset means secrets are unavailable, which is supported |
| `PODIUM_S3_*` | the object store artifacts and rolled-up logs live in. Unset disables artifacts entirely, which is also supported |

`podium-server` has three subcommands: `init` (writes a master key and a filled `.env`),
`gen-master-key`, and `rotate-master-key`. `/healthz`, `/readyz` and `/metrics` are open on both
daemons; every RPC is behind the transport's identity check.

## Development

```sh
make build              # UI + runner-embed + all four binaries into bin/, host-native
make test               # unit tests
make test-integration   # + real Postgres and Docker via testcontainers
make e2e                # boots a full stack and drives the real CLI
make lint proto fmt
go build -tags noui ./...   # skip the embedded UI, no Node required
```

> **Stop any running `podium-node` before `make test-integration` or `make e2e`.** Both suites
> start real nodes against the host's Docker engine, and a node claims containers by the
> `podium.task` label alone — no node scoping. Each side reports the other's containers to its
> own control plane, which has never heard of them, and tears them down. You lose the test run
> *and* whatever the live node was running, and it looks like flakiness or memory pressure. It is
> not. (`DOCKER_HOST` or `PODIUM_NODE_DOCKER_HOST` pointed at a second engine separates them too,
> if you have one.)

`podium-runner` is the one binary that is never host-native: it is PID 1 inside a Linux task
container, so `make build` cross-compiles it for `linux/amd64` and `linux/arm64` into
`internal/node/docker/runnerbin/` (embedded into `podium-node`, gitignored, never committed) and
copies the host architecture's build to `bin/podium-runner`. A clone that has never run
`make runner-embed` still compiles — `go:embed` finds a committed placeholder — but a node
started from it refuses to run tasks and says which target to build.

See [CONTRIBUTING.md](CONTRIBUTING.md).

---

## Limitations

What Podium does not do, and what will surprise you if nobody says it first.

### Architectural, and not going to change soon

- **Single server process.** Node sessions are held in memory, so only the server holding a
  node's stream can assign to it, cancel on it or drain it — and a second replica's health
  watchdog would see every node as sessionless and start expiring leases. A leader lock is
  needed before a second replica is ever started.
- **One `podium-node` per Docker engine.** At startup the daemon claims every container on the
  engine labelled `podium.task`, whichever daemon created it, and tears down the ones its own
  control plane does not recognise. So two daemons on one engine destroy each other's work.
  Nothing enforces it. **It bites hardest in development** — see [Development](#development).
- **No RBAC.** The tailnet transport records who is visiting in a `users` table and lets every
  one of them do everything: submit tasks (and therefore run code as root on every worker),
  drain nodes, delete secrets. The web UI is the same. **The bot widens this a long way**:
  anyone who can mention it in a Slack channel it has joined, or assign it a Linear issue, can
  make it run code on a worker with that playbook's credentials. A playbook's `secrets:` list scopes
  what one bot hands one turn — keep it minimal — but it is not a boundary around the secret
  store: `CreateTask` checks only that a named secret exists, so anyone who can reach the API
  can already mount any registered secret into an image of their own. The agent layer does not
  change this.
- **No egress policy.** A task reaches its sidecars and the internet. Whether it can also reach
  its worker's other networks depends on the host's routing, and Docker's default forwards it —
  **assume it can**, and firewall the host if that matters.

### Things that will bite you in normal use

- **A task adopted after a node restart loses three things**: its log redactor, its runner event
  socket, and any sidecar log stream. The container keeps running and its stdout/stderr keep
  flowing, byte-exact. Auto-collected artifacts are lost. The seam is marked in the task's own
  history as `step{name: "node/reattached"}`.
- **Log redaction is best-effort string matching**, not a guarantee. It does not catch a value
  the task transformed, split, or shorter than 8 bytes. It is defence against an accidental
  `echo $PASSWORD`, not the control that keeps a secret out of a log.
- **A sidecar cannot reference a secret.** A database sidecar that needs a password takes it from
  a plaintext `env:` entry.
- **Under the `local` transport, resolved secret values cross an unencrypted loopback socket.**
  Loopback is doing all the work; the server refuses to bind anywhere else.
- **Nothing is ever deleted except rolled-up log chunks.** Tasks, events, artifacts and audit
  rows grow without bound, and the object store has no lifecycle policy. There is no retention
  policy and no way to configure one.
- **Rolled-up logs lose the interleaving between streams.** Once a finished task's chunks have
  been pruned, its log replays as stdout then stderr — one object per stream, and nothing records
  how they were braided together. Within a stream the order is exact.
- **A CPU limit is not visible inside the container.** `nproc` reports the host's cores whatever
  `resources.cpu` says, because a CPU quota is not namespaced.
- **Image cache pruning is off by default** and only ever removes images Podium pulled itself
  (`data_dir/images.json` is the allow-list). A long-lived node accumulates images until someone
  intervenes; that is the intended trade, because a node shares its engine with the rest of the
  machine.
- **A moving tag is never refreshed.** An image is pulled only when the engine says it is absent,
  so `alpine:3` stays whatever version that node first cached.
- **The control plane emits no Podium metrics at all.** `/metrics` carries the Go and process
  collectors and nothing else. The node emits three gauges. There is no Grafana dashboard,
  because there would be nothing honest to put on it.
- **`podium node rm` does not stop the daemon.** A removed node whose `identity.json` survives
  reconnects for ever and is told its key is unknown, once per backoff.
- **The web UI holds a task's whole log in memory**, cannot jump to an arbitrary page of the task
  list, shows no node CPU/memory utilisation, and keeps the bearer token in `localStorage`.

## Security

Read [docs/security.md](docs/security.md) before deciding which machines run a node. The short
version: **a `podium-node` is root-equivalent on its host**, and **a task container is
untrusted**. Report a vulnerability privately — see [SECURITY.md](SECURITY.md).

## License

[MIT](LICENSE).
