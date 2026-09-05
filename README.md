# Podium

**Run containerised tasks on machines you own, from anywhere, with one command.**

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

<!-- 60-second demo: an asciinema cast belongs here. Not recorded yet. -->

> ### ⚠️ Status: pre-alpha, and **not licensed**
>
> [`LICENSE`](LICENSE) is still a placeholder. **Nobody has been granted any right to use,
> copy or redistribute this code**, and it cannot be published until that is resolved — the
> release workflow refuses to run while the file contains `TODO`, and every other release job
> depends on that check.
>
> There has never been a tagged release, so there is no binary to download and no image to
> pull. Building from source is the only way in. See [Limitations](#limitations) for what has
> and has not actually been proved.

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

Needs Go 1.26+, Docker (Engine 24+, **cgroup v2**), Node 22+ and pnpm.

```sh
git clone https://github.com/alvaroibarguen/podium.git && cd podium
make build
docker compose -f deploy/docker-compose.dev.yml up -d --wait postgres

PODIUM_TRANSPORT=dev PODIUM_DEV_TOKEN=devtoken \
PODIUM_DATABASE_URL=postgres://podium:podium@127.0.0.1:5432/podium \
  ./bin/podium-server &

export PODIUM_SERVER=http://127.0.0.1:8080 PODIUM_TOKEN=devtoken
TOKEN=$(./bin/podium node enroll-token --label demo)

PODIUM_NODE_SERVER=$PODIUM_SERVER PODIUM_NODE_TRANSPORT=dev PODIUM_NODE_DEV_TOKEN=devtoken \
PODIUM_NODE_ENROLL_TOKEN=$TOKEN PODIUM_NODE_DATA_DIR=/tmp/podium-node \
  ./bin/podium-node &

./bin/podium nodes
./bin/podium run --image alpine:3 -- echo hello
open http://127.0.0.1:8080
```

Full walkthrough: **[docs/quickstart.md](docs/quickstart.md)**.

> If 5432 or 8080 are taken on your machine, set `PODIUM_PG_PORT` and
> `PODIUM_DEV_LISTEN=127.0.0.1:18080`, and match `PODIUM_DATABASE_URL` and `--server` to them.

A task can bring its own environment with it:

```sh
podium run --spec examples/postgres-sidecar.yaml
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

### Starting the agent layer

The conductor is a second process. It is an ordinary API client of `podium-server`, with its own
database and its own token, so the server has to be told where it is before the **Agent** tab
appears in the UI.

```sh
docker exec podium-dev-postgres createdb -U podium podium_agent   # once
make build agent-runtime                                          # + the three runtime images

# the server needs these two, or /agent stays hidden
PODIUM_AGENT_URL=http://127.0.0.1:8090 PODIUM_AGENT_TOKEN=agenttoken \
  ./bin/podium-server &                                           # plus its usual variables

PODIUM_AGENT_SERVER=http://127.0.0.1:8080 PODIUM_AGENT_API_TOKEN=devtoken \
PODIUM_AGENT_DATABASE_URL=postgres://podium:podium@127.0.0.1:5432/podium_agent \
PODIUM_AGENT_TOKEN=agenttoken PODIUM_AGENT_PROFILE_DIR=examples/agent \
  ./bin/podium-agent &

open http://127.0.0.1:8080/agent
```

Five variables are mandatory and the conductor names the missing one and exits:

| | |
|---|---|
| `PODIUM_AGENT_SERVER` | the Podium API base URL |
| `PODIUM_AGENT_API_TOKEN` | required whenever that URL is `http://`; empty on a tailnet, where WhoIs supplies identity |
| `PODIUM_AGENT_DATABASE_URL` | its **own** database, `podium_agent`. It never opens the server's |
| `PODIUM_AGENT_TOKEN` | the bearer `podium-server` presents on proxied `AgentService` calls. The same value goes in the server's environment |
| `PODIUM_AGENT_PROFILE_DIR` | defaults to `/etc/podium/agent`, so in a checkout you must point it at `examples/agent`. Must contain `profile.yaml` |

Everything else is optional and switches a feature on: both Slack tokens together (one alone is
an error naming the other), `PODIUM_AGENT_LINEAR_API_KEY`, and the `PODIUM_AGENT_MEMORY_*` set.
`PODIUM_AGENT_LISTEN` defaults to `127.0.0.1:8090` — keep it on loopback, because the server
proxies it and nothing else should reach it.

**To get a real answer rather than a dry run** you also need an Anthropic key, set in **Agent →
Settings**, which validates it against `GET /v1/models` and stores it as the Podium secret
`podium.agent.anthropic_api_key`; and at least one enrolled node whose engine has the runtime
images. Without a key the machinery runs end to end and returns a canned answer.

Full reference, including the Slack app manifest and the Linear setup:
**[docs/agent.md](docs/agent.md)**.

### Things that will trip you up locally

- **The dev token is stored per browser origin.** It lives in `localStorage` under
  `podium.devToken`, so it persists — but `127.0.0.1:8080` and `localhost:8080` and any other
  port are each a different origin with their own copy. Pick one address and stay on it, or you
  will be asked for the token again every time.
- **A secret is only readable under the master key it was written with.** `secrets.key_id` records
  which one — the first eight bytes of the key's SHA-256. Point the server at a different
  `PODIUM_MASTER_KEY_FILE` and listing still works, because that reads metadata only, but
  resolving the secret into a task fails with a key mismatch. `podium secret set` re-encrypts
  under the current key.
- **Task history belongs to the database, not the server.** Pointing `PODIUM_DATABASE_URL` at a
  fresh database gives you an empty UI; the old one is untouched and switching back restores it.
- **The conductor's `/readyz` is stricter than the server's.** It checks its own database, the
  Podium API and, when configured, Hindsight. The server's readiness deliberately ignores the
  conductor: a control plane whose bot is down is still a working task runner.
- **There are two Anthropic keys, held very differently.** The agents' key is set in the UI and
  encrypted at rest; the memory service's (`PODIUM_MEMORY_LLM_API_KEY`) is a container
  environment variable it reads at start-up, before anything Podium controls is running, so it
  is **visible in `docker inspect`**. They may be the same value. Give the memory service its own
  scoped key with a spend limit, and prefer mounting it as a read-only `.env` at `/app/.env`
  over passing `-e` — see [docs/security.md](docs/security.md). Memory *reads* need no key at
  all: search and rerank run on models baked into the image. Only writing does, because storing
  a memory means extracting facts with an LLM.

---

## The binaries

| | |
|---|---|
| `podium-server` | API, scheduler, node registry, secrets, log ingest, embedded web UI. Needs Postgres (`pgvector/pgvector:pg16`); optionally an S3-compatible store |
| `podium-node` | One per worker. Runs tasks on the local Docker engine. **Root-equivalent on its host** — read [security.md](docs/security.md) |
| `podium-agent` | The conductor. Turns a Slack mention, a Linear assignment or a web-chat message into one task running an agent runtime image, and relays the answer back. An ordinary API client of `podium-server`: its own database, its own token, never touches Docker. See [docs/agent.md](docs/agent.md) |
| `podium` | The CLI. Talks only to the server, never to Docker, so it runs anywhere |
| `podium-runner` | PID 1 inside every task container: runs the command, forwards signals, reaps orphans, reports events. Embedded in `podium-node` and bind-mounted in; never installed by hand |

## Running across machines

The `dev` transport above is loopback-only, so node and server share a host. For real workers,
Podium joins your Tailscale network: the server serves HTTPS on its MagicDNS name, workers dial
out, and there is no login page, no API token and no public ingress.

```sh
PODIUM_TRANSPORT=tailnet TS_AUTHKEY=tskey-auth-... PODIUM_DATABASE_URL=... ./bin/podium-server

# from any device on the tailnet — no token, no login
./bin/podium --server https://podium.<tailnet>.ts.net nodes
```

Read **[docs/networking.md](docs/networking.md)** first: what to create in the Tailscale admin
console, the ACL, and the two different keys involved (a Tailscale auth key and a Podium
enrollment token are not the same thing).

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
podium node rm worker-3         # once it is drained and idle
```

A node started with `--exit-on-drain` exits 0 when its last task finishes, which is the upgrade
path.

---

## Status

Everything below is built and its tests pass. The **Proved** column is the honest one: it says
what has actually been observed running. Most of it is macOS/arm64 with Docker Desktop; a real
`podium-node` has now also run on Linux/amd64, and the rows say which is which.

| | Built | Proved |
|---|---|---|
| Task lifecycle, live logs, cancel, exit codes | ✅ | ✅ end-to-end suite |
| Sidecars, readiness probes, teardown | ✅ | ✅ both probe paths for real: `exec` inside the container on macOS, where the engine's bridges are unreachable from the host, and the **direct dial** on a Linux node, for `tcp_port` and `http_path`, passing and failing |
| Resource limits, OOM reporting, hardening | ✅ | ✅ |
| Secrets: encrypted store, env and file injection, shredding, log redaction | ✅ | ✅ |
| Scheduler, leases, heartbeats, reconciliation, drain | ✅ | ✅ including chaos scenarios |
| Web UI: submit, re-run, live logs, node actions, secrets, artifacts, agent | ✅ | ✅ 221 unit tests, Playwright against a live stack |
| Artifacts and log roll-up | ✅ | ⚠️ storing and listing proved against a **real MinIO**, including a zero-byte artifact and a browser task's PNG. The automated suite uses an in-process endpoint. Multipart, TLS, bucket policies and AWS S3 proper are unexercised |
| Tailnet transport (tsnet, WhoIs identity, HTTPS, ACL) | ✅ | ❌ **never run against a real tailnet** |
| `host` transport | ✅ | ❌ never run |
| Container images (GHCR, multi-arch, distroless, signed) | ✅ configured | ❌ never built or published |
| Release pipeline (archives, checksums, SBOM) | ✅ | ⚠️ snapshot only — no tag, no signature ever produced |
| `deploy/install-node.sh`, systemd unit | ✅ | ❌ `shellcheck` and `bash -n` only. Never run on a machine — there is no published release for it to download |
| `podium-node upgrade` | ✅ | ⚠️ download, checksum verification, atomic swap and drain→swap→undrain exercised against a local release server and a live control plane. Never against two real releases; `systemctl restart` untested |
| Linux | ✅ | ✅ a real `podium-node` on Pop!_OS 24.04, linux/amd64, Docker Engine 29.7.2, cgroup v2, driven by a darwin/arm64 control plane over a real tailnet. cgroup v2 limits, OOM (exit 137), hardening, secrets on tmpfs, artifacts, log roll-up, cancellation and node-restart adoption all exercised. **Still unrun on Linux:** `deploy/install-node.sh`, the systemd unit, the container images — and the tailnet *transport*, which was relayed over the tailnet rather than used |
| Runner `message` events (a task talks back mid-run) | ✅ | ✅ end-to-end to the CLI, the UI timeline and the database |
| Agent runtime image (one Claude Agent SDK turn per task) | ✅ | ⚠️ every path **except the model call**. No Anthropic key exists here, so every turn ever run was a dry run |
| Conductor: sessions, turns, exactly-once relay, restart recovery | ✅ | ✅ end-to-end, including a mid-turn kill and a second message queued behind a running turn |
| Slack source | ✅ | ❌ **never connected to Slack.** Driven by a fake |
| Linear source and the `coder` skill | ✅ | ❌ **never connected to Linear.** Driven by a fake GraphQL server. The browser image did produce a real screenshot as a task |
| Shared memory (Hindsight, pgvector) | ✅ | ✅ against a **real Hindsight container**: auth, retain, list, search, tombstone. The SDK's own MCP client is unproven (needs a model) |
| Web chat and the `analyst` skill | ✅ | ⚠️ chat turns round-trip for real as dry runs. The read-only warehouse role is proved against a real Postgres; **BigQuery is unproven beyond `bq version`** |

Milestones, for anyone reading the history: M0 scaffold and wire contract, M1 the first
end-to-end task, M2 sidecars / secrets / artifacts, M3 the tailnet transport, M4 the scheduler,
M5 the web UI, M6 packaging and documentation, M7 the `message` event and the agent runtime
image, M8 the conductor and Slack, M9 the settings UI / memory / Linear, M10 the web chat and
the analyst skill — this commit.

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
- [docs/agent.md](docs/agent.md) — the conductor (`podium-agent`): the Slack bot, profiles and skills, how a turn works, the agents' shared memory
- [deploy/README.md](deploy/README.md) — compose, the installer, the systemd unit
- [deploy/.env.example](deploy/.env.example) — every `PODIUM_*` variable, commented

**Internals**

- [docs/protocol.md](docs/protocol.md) — the node↔server stream, event ordering, acks, reconciliation
- [docs/runner-events.md](docs/runner-events.md) — `podium-runner` as PID 1 and its event socket
- [CONTRIBUTING.md](CONTRIBUTING.md) — dev setup, house rules, the things that will confuse you

---

## Configuration

Every daemon is configured entirely by environment. **Every variable is documented in
[`deploy/.env.example`](deploy/.env.example)** — and `go test ./deploy/...` fails the build if
the code reads one that file does not mention, or if that file documents one nothing reads any
more.

The four you cannot skip:

| | |
|---|---|
| `PODIUM_DATABASE_URL` | the Postgres DSN. The server migrates on start |
| `PODIUM_DEV_TOKEN` | the `dev` transport's shared bearer token |
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

`podium-runner` is the one binary that is never host-native: it is PID 1 inside a Linux task
container, so `make build` cross-compiles it for `linux/amd64` and `linux/arm64` into
`internal/node/docker/runnerbin/` (embedded into `podium-node`, gitignored, never committed) and
copies the host architecture's build to `bin/podium-runner`. A clone that has never run
`make runner-embed` still compiles — `go:embed` finds a committed placeholder — but a node
started from it refuses to run tasks and says which target to build.

See [CONTRIBUTING.md](CONTRIBUTING.md).

---

## Limitations

Everything here is real, current, and deliberate about being said out loud.

### Where it has and has not actually run

- **The tailnet transport has never touched a real tailnet.** It is implemented, unit-tested and
  integration-tested, but proving it needs tagged auth keys and HTTPS enabled on a tailnet, and
  the build machine had neither. The `host` transport is likewise implemented and never run.
  **The `dev` transport is the tested one.**
- **Artifacts have run against a real MinIO, but not against S3 itself.** Storing and listing
  are proved end to end against a real MinIO server, including a zero-byte artifact and a real
  PNG a browser task produced. The automated suite still uses an in-process endpoint that speaks
  the same API and verifies presigned signatures for real. **Multipart upload, bucket policies,
  TLS, lifecycle rules and AWS S3 proper remain unexercised**, as does a presign round trip
  against anything but the in-process endpoint.
- **Linux has now run a real worker, and here is exactly how much of it.** A real `podium-node`
  ran on Pop!_OS 24.04, linux/amd64, Docker Engine 29.7.2, cgroup v2, 24 cores, driven by a
  darwin/arm64 control plane on another machine over a real tailnet. The **direct-dial readiness
  path** — the one Linux takes instead of `exec`ing inside the container, and the one that had
  only ever been unit-tested — is proved for `tcp_port` and `http_path`, both passing and
  failing. So are cgroup v2 resource limits, OOM reporting with exit 137, container hardening,
  secrets on tmpfs, artifact collection, log roll-up, cancellation, and a node restart adopting
  the containers it left behind. That run is also what found the artifact-collection bug this
  release fixes: it only reproduces where the daemon and the node share a filesystem, which
  Docker Desktop does not.
- **Three things on Linux are still unrun.** `deploy/install-node.sh` — there is no published
  release for it to download, and it passes `shellcheck` and `bash -n` only. The systemd unit —
  `systemd-analyze verify` has not been run on it. And **the container images have never been
  built or published.** The tailnet *transport* is likewise still unproved: the tailnet carried
  the traffic, but through a TCP relay in front of the dev transport's loopback listener.

### The agent layer has never met the services it exists to talk to

The conductor, the runtime image and all three skills are implemented, unit-tested,
integration-tested against fakes, and covered by end-to-end scenarios that run real containers on
a real Docker engine. What has **not** happened:

- **No agent turn has ever called a model.** There is no Anthropic API key on the build machine,
  so every turn ever executed — in tests, in the acceptance script, by hand — ran with
  `PODIUM_AGENT_DRY_RUN=1` and returned a canned answer. Exactly one code path is unproven, and
  it is the one that matters: the single `query()` call into the Claude Agent SDK. Everything
  around it is exercised. **Run the smoke test in `examples/agent/README.md` before trusting the
  `coder` skill to write a pull request.**
- **No Slack workspace.** Socket Mode, `app_mention`, thread reading, threaded replies, file
  upload, reactions and the 4000-character split are written against `slack-go v0.29.0` and
  driven by a fake in tests. Nothing has connected to Slack.
- **No Linear workspace.** The poller, the issue and comment reads, the state transition and
  `commentCreate` are driven against a fake GraphQL server through `PODIUM_AGENT_LINEAR_URL`.
  `fileUpload` in particular is implemented from documentation alone and has never run.
- **No data warehouse.** `psql`, `bq` and `duckdb` are installed and report their versions, and
  the read-only story is proved for real against a throwaway Postgres: an `UPDATE` under the
  `podium_analyst` role fails with `cannot execute UPDATE in a read-only transaction`, and the
  agent produced a real CSV and a real matplotlib PNG. **BigQuery is unproven beyond `bq
  version`.**
- **Memory is the exception: Hindsight ran for real.** A real `ghcr.io/vectorize-io/hindsight`
  container, pointed at a real pgvector Postgres, authenticated, retained, listed, searched and
  tombstoned memories. The one unproven link is the Agent SDK's own MCP client reaching it from
  inside a task container, which needs a model call.
- **The conductor has only ever run on one host, under the `dev` transport.** Its tailnet compose
  entry cannot work as written, because it points at a `server:8080` that does not exist under
  `PODIUM_TRANSPORT=tailnet`; the file says so in a comment.
- **The three runtime images have never been published.** Task images are resolved from the
  node's own engine, so the local `:dev` tags work on a single host. A worker on a second machine
  cannot pull them until they are pushed to a registry.

### Architectural, and not going to change soon

- **Single server process.** Node sessions are held in memory, so only the server holding a
  node's stream can assign to it, cancel on it or drain it — and a second replica's health
  watchdog would see every node as sessionless and start expiring leases. A leader lock is
  needed before a second replica is ever started.
- **One `podium-node` per Docker engine.** The daemon claims every container labelled
  `podium.task` on the engine, so two of them adopt each other's work. Nothing enforces it.
- **No RBAC.** The tailnet transport records who is visiting in a `users` table and lets every
  one of them do everything: submit tasks (and therefore run code as root on every worker),
  drain nodes, delete secrets. The web UI is the same. **The bot widens this a long way**:
  anyone who can mention it in a Slack channel it has joined, or assign it a Linear issue, can
  make it run code on a worker with that skill's credentials. A skill's `secrets:` list scopes
  what one bot hands one turn — keep it minimal — but it is not a boundary around the secret
  store: `CreateTask` checks only that a named secret exists, so anyone who can reach the API
  can already mount any registered secret into an image of their own. Nothing in the agent track
  fixes this.
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
- **Under the `dev` transport, resolved secret values cross an unencrypted loopback socket.**
  Loopback is doing all the work; the server refuses to bind anywhere else.
- **Nothing is ever deleted except rolled-up log chunks.** Tasks, events, artifacts and audit
  rows grow without bound, and the object store has no lifecycle policy. Retention is work
  nobody has done.
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
  list, shows no node CPU/memory utilisation, and keeps the dev token in `localStorage`.

## Security

Read [docs/security.md](docs/security.md) before deciding which machines run a node. The short
version: **a `podium-node` is root-equivalent on its host**, and **a task container is
untrusted**. Report a vulnerability privately — see [SECURITY.md](SECURITY.md).

## License

**Not yet chosen** — see [`LICENSE`](LICENSE). This code is not published under any license
until that decision is made, and the release workflow refuses to run until it is.
