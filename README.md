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
make build agent-runtime                                          # + the agent runtime images

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

**To get a real answer rather than a dry run** you also need a model credential, set in **Agent →
Settings**, and at least one enrolled node whose engine has the runtime images. Without one the
machinery runs end to end and returns a canned answer.

Two backends, one runtime image:

- **Claude** — paste an Anthropic key. It is validated against `GET /v1/models` and stored as the
  Podium secret `podium.agent.anthropic_api_key`.
- **Grok** — paste an xAI key, or sign in with a SuperGrok / X Premium+ subscription. The
  subscription sign-in is an OAuth device code; the client id ships as a default, and setting
  the variable to the empty string turns it off.

The harness is [opencode](https://opencode.ai), which takes `--model provider/model` — so a
backend is a flag rather than a dialect, one runtime image serves every provider, and adding a
third is a catalogue entry.

A profile picks the default backend, model and reasoning effort, and any skill can override all
three. The picker on **Agent → Skills** is one control for the three, because they are one
decision — a model only runs on one backend, and which effort levels exist depends on the model.

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

The `dev` transport above is loopback-only, so node and server share a host. **The tailnet
transport is the only supported way to reach a worker on another machine** — in development as
much as in production, and not merely the recommended one. Podium joins your Tailscale network:
the server serves HTTPS on its MagicDNS name, workers dial out, and there is no login page, no
API token and no public ingress.

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
| Artifacts and log roll-up | ✅ | ⚠️ proved against a **real RustFS**: bucket auto-create, a 40 MB artifact over the multipart threshold, fetched back byte-identical both proxied and by presigned URL, plus log roll-up and read-back. Earlier MinIO runs covered a zero-byte artifact and a browser task's PNG. The automated suite uses an in-process endpoint. TLS, bucket policies and AWS S3 proper are unexercised |
| Tailnet transport (tsnet, WhoIs identity, HTTPS) | ✅ | ✅ **run against a real tailnet.** Real Let's Encrypt certificate on the MagicDNS name; `WhoAmI` named a caller with no bearer token sent; a `tag:podium-node` worker enrolled and ran a linux/amd64 task with live logs and its exit code. Two workers now run against it, routed by label |
| The tailnet ACL's outbound-only guarantee | ✅ | ❌ **never enforced.** A blanket allow-all rule on that tailnet made [the shipped policy](deploy/tailscale-acl.example.json)'s `tests` block fail, and the block was dropped rather than the rule narrowed |
| Device approval; more than one WhoIs identity | ✅ | ❌ never run. One login has ever authenticated, on a tailnet with approval off |
| `host` transport | ✅ | ❌ never run |
| Container images (GHCR, multi-arch, distroless, signed) | ✅ configured | ⚠️ one image, one registry. `podium-agent-runtime:dev` — the base, and the only image Podium ships for general use — built multi-arch with buildx and pushed to a **private** LAN registry, which Podium can pull from only because it needs no login. **Nothing on GHCR, nothing signed, no release.** `-dev` is a host-architecture local tag; the four Go service images have never been built at all |
| Release pipeline (archives, checksums, SBOM) | ✅ | ⚠️ snapshot only — no tag, no signature ever produced |
| `deploy/install-node.sh`, systemd unit | ✅ | ❌ `shellcheck` and `bash -n` only. Never run on a machine — there is no published release for it to download |
| `podium-node upgrade` | ✅ | ⚠️ download, checksum verification, atomic swap and drain→swap→undrain exercised against a local release server and a live control plane. Never against two real releases; `systemctl restart` untested |
| Linux | ✅ | ✅ a real `podium-node` on Pop!_OS 24.04, linux/amd64, Docker Engine 29.7.2, cgroup v2, driven by a darwin/arm64 control plane over a real tailnet — first relayed to the dev transport, since then over the tailnet transport itself. cgroup v2 limits, OOM (exit 137), hardening, secrets on tmpfs, artifacts, log roll-up, cancellation and node-restart adoption all exercised. **Still unrun on Linux:** `deploy/install-node.sh`, the systemd unit, and the service container images |
| Runner `message` events (a task talks back mid-run) | ✅ | ✅ end-to-end to the CLI, the UI timeline and the database |
| Agent runtime image (one opencode turn per task) | ✅ | ⚠️ a real turn completed on **both** providers, with accounting and artifacts. Tool calls, repo clones, memory and the max-turns cap are unit-tested only |
| xAI credentials: API key and subscription sign-in | ✅ | ✅ **both proved end to end against the real xAI.** A bad key refused by `api.x.ai` in its own words; a device-code sign-in approved by a human, a refresh token issued, the access token validated and stored |
| Per-turn agent/model/effort picker | ✅ | ✅ resolution, credential routing and the brief proved on a live stack |
| Running a turn **on a Grok model** | ✅ | ✅ `xai/grok-4.6` completed a turn through the runtime on the live stack |
| Conductor: sessions, turns, exactly-once relay, restart recovery | ✅ | ✅ end-to-end, including a mid-turn kill and a second message queued behind a running turn |
| Slack source | ✅ | ❌ **never connected to Slack.** Driven by a fake |
| Linear source | ✅ | ❌ **never connected to Linear.** Driven by a fake GraphQL server, against a ticket skill the test defines: Podium ships no skill with `linear: true` |
| Shared memory (Hindsight, pgvector) | ✅ | ✅ against a **real Hindsight container**: auth, retain, list, search, tombstone. The SDK's own MCP client is unproven (needs a model) |
| Web chat | ✅ | ⚠️ chat turns round-trip for real as dry runs, through podium-server's proxy |

Milestones, for anyone reading the history: M0 scaffold and wire contract, M1 the first
end-to-end task, M2 sidecars / secrets / artifacts, M3 the tailnet transport, M4 the scheduler,
M5 the web UI, M6 packaging and documentation, M7 the `message` event and the agent runtime
image, M8 the conductor and Slack, M9 the settings UI / memory / Linear, M10 the web chat — this
commit.

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

Everything here is real, current, and deliberate about being said out loud.

### Where it has and has not actually run

- **The tailnet transport has now run against a real tailnet.** `podium-server` joined as a
  `tag:podium-server` device, served 443 on its MagicDNS name under a real Let's Encrypt
  certificate, and answered `WhoAmI` with a login **with no bearer token sent** — the identity
  came from Tailscale's `WhoIs` and nothing else. A `tag:podium-node` worker enrolled over it and
  ran a linux/amd64 task with live logs and its exit code preserved. Two workers now run against
  it, routed by label.
- **Four things on that path are still unproved, and one is the guarantee itself.** The ACL's
  outbound-only property was **never enforced**: the tailnet already had a blanket allow-all
  rule, which made the shipped policy's `tests` block fail, and the block was dropped rather than
  the rule narrowed. Nothing has ever refused server → node.
  [`deploy/tailscale-acl.example.json`](deploy/tailscale-acl.example.json) has not been applied
  intact. Also unproved: **device approval**; **more than one identity** — one login has ever
  authenticated, so `users` has never held two rows; and the `host` transport, never run at all.
- **Artifacts have run against a real RustFS, but not against S3 itself.** Bucket auto-create,
  storing, listing and log roll-up are proved end to end against a real RustFS server, including
  a 40 MB artifact over `minio-go`'s multipart threshold fetched back byte-identical both proxied
  and by presigned URL. Earlier runs against MinIO covered a zero-byte artifact and a real PNG a
  browser task produced. The automated suite still uses an in-process endpoint that speaks
  the same API and verifies presigned signatures for real. **Bucket policies,
  TLS, lifecycle rules and AWS S3 proper remain unexercised.**
- **Linux has now run a real worker, and here is exactly how much of it.** A real `podium-node`
  ran on Pop!_OS 24.04, linux/amd64, Docker Engine 29.7.2, cgroup v2, 24 cores, driven by a
  darwin/arm64 control plane on another machine over a real tailnet. The **direct-dial readiness
  path** — the one Linux takes instead of `exec`ing inside the container, and the one that had
  only ever been unit-tested — is proved for `tcp_port` and `http_path`, both passing and
  failing. So are cgroup v2 resource limits, OOM reporting with exit 137, container hardening,
  secrets on tmpfs, artifact collection, log roll-up, cancellation, and a node restart adopting
  the containers it left behind. That run is also what found the artifact-collection bug this
  release fixes: it only reproduces where the daemon and the node share a filesystem, which
  Docker Desktop does not. That first run relayed its traffic over the tailnet into the dev
  transport's loopback listener; the tailnet transport itself has since driven the same worker
  directly.
- **Two things on Linux are still unrun.** `deploy/install-node.sh` — there is no published
  release for it to download, and it passes `shellcheck` and `bash -n` only. And the systemd
  unit — `systemd-analyze verify` has not been run on it.
- **The base agent image has been built and pushed. The ones you would deploy have not.**
  `podium-agent-runtime:dev` is built multi-arch — linux/amd64 and linux/arm64 in one OCI index —
  with `docker buildx`, and pushed to a private plain-HTTP registry on the LAN. That registry
  needs no login, which is the only reason a node can pull from it: Podium has no registry
  authentication. That is the whole of it: nothing on GHCR, nothing signed, no release cut,
  `podium-agent-runtime-dev` still a host-architecture local tag, and **the four Go service
  images — `podium-server`, `podium-node`, `podium`, `podium-agent` — never built on any
  architecture.**

  Two things that cost an afternoon, if you repeat this. A plain-HTTP registry must be in
  `insecure-registries` on **both** the pushing and the pulling daemon. And buildx's
  `docker-container` driver does **not** inherit that from its daemon — the builder needs its own
  `buildkitd.toml` with `[registry."host:port"] http = true`, given at `docker buildx create`.

### The agent layer has never met the services it exists to talk to

The conductor, the runtime image and all three skills are implemented, unit-tested,
integration-tested against fakes, and covered by end-to-end scenarios that run real containers on
a real Docker engine. What has **not** happened:

- **Both providers run a real turn.** The runtime's harness is opencode, not the Claude Agent
  SDK — the SDK could only talk to Anthropic, and pointing it at xAI failed on the first
  request with `400 invalid-argument: Invalid message role` because it puts a `system`-role
  entry inside `messages[]`. Under opencode, `xai/grok-4.6` and `anthropic/claude-opus-5` each
  completed a turn end to end on a live stack, with cost and turn count recorded and the
  transcript and `turn.json` artifacts intact. **What has not been exercised on a live model is
  everything past a one-word answer**: a turn that calls tools, clones a repository, writes an
  artifact, uses memory, or hits `max_turns`. Those paths have unit tests and nothing more.
- **The harness adds its own scaffolding to every turn.** A custom agent's prompt is applied
  and is behaviourally authoritative — verified with a sentinel instruction — but it does not
  replace opencode's own ~5k tokens of tool definitions and base instructions. The previous
  harness did the same thing, so this is not a regression, but it is not nothing either.
- **No Slack workspace.** Socket Mode, `app_mention`, thread reading, threaded replies, file
  upload, reactions and the 4000-character split are written against `slack-go v0.29.0` and
  driven by a fake in tests. Nothing has connected to Slack.
- **No Linear workspace.** The poller, the issue and comment reads, the state transition and
  `commentCreate` are driven against a fake GraphQL server through `PODIUM_AGENT_LINEAR_URL`.
  `fileUpload` in particular is implemented from documentation alone and has never run.
- **No data warehouse, and no warehouse image any more.** The read-only role recipe in
  `docs/agent.md` was proved once against a throwaway Postgres — an `UPDATE` under
  `podium_analyst` failed with `cannot execute UPDATE in a read-only transaction` — from an
  image that no longer ships. BigQuery was never proved beyond `bq version`. Anyone wanting
  `psql`, `bq` or `duckdb` in a turn now builds that image themselves.
- **Memory is the exception: Hindsight ran for real.** A real `ghcr.io/vectorize-io/hindsight`
  container, pointed at a real pgvector Postgres, authenticated, retained, listed, searched and
  tombstoned memories. The one unproven link is the Agent SDK's own MCP client reaching it from
  inside a task container, which needs a model call.
- **The conductor has only ever run on one host, under the `dev` transport.** Its entry in
  `deploy/docker-compose.tailnet.yml` is a recipe, not a tested service, and the file says so:
  under `PODIUM_TRANSPORT=tailnet` the server has **no port on the compose network**, so a plain
  sidecar cannot reach it. That entry works only where the conductor can itself route into the
  tailnet — the `host` transport, or `podium-agent` run on the host.
- **An image your fleet cannot pull is a skill your fleet cannot run.** Task images are resolved
  by the node's own engine, so a local `:dev` tag works only on the host that built it.
  `podium-agent-runtime:dev` is multi-arch on a private registry, so a second worker can pull it.
  `podium-agent-runtime-dev:dev` is not, and neither is an image you build `FROM` the base until
  you push it — to a registry every node can pull from **anonymously**, because Podium has
  nowhere to put a pull credential.

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
