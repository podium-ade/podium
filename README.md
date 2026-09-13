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

A control plane on this machine. Workers anywhere that can already reach it.

```sh
mkdir podium && cd podium
curl -fsSLo docker-compose.yml \
  https://raw.githubusercontent.com/podium-ade/podium/main/deploy/docker-compose.host.yml

docker run --rm -v "$PWD:/out" --user "$(id -u):$(id -g)" \
  ghcr.io/podium-ade/podium-server:latest \
  init --dir /out
```

`init` writes `master.key` and `.env` with fresh credentials, and never overwrites either.
Fill `PODIUM_SERVER` — this machine's address on your network, for example
`http://10.8.0.2:8080`. `0.0.0.0` is a bind address, not a URL.

```sh
docker compose up -d --wait
```

Linux Engine. On a Mac, clone the repo and `make stack-up` — the binaries already use this
machine's network. The same `.env` either way.

Postgres, an object store, the control plane and the conductor, with the web UI on 8080 and
the **Agent** screen already wired. No Go toolchain, no Node, no binary on the host. The
Postgres bootstrap script is inline in the compose file, the conductor's profile directory
ships inside its image, and the master key is generated on first `up`.

It fails closed: nothing has a default password. `init` minted the token the UI will ask for.

### The whole file

This is all of it. Save it as `docker-compose.yml` next to the `.env` `init` wrote.

<!-- BEGIN deploy/docker-compose.host.yml -->
```yaml
# A Podium control plane on this machine's own network — LAN, WireGuard, a corporate VPN,
# whatever already routes here. One file, like the others, and it FAILS CLOSED on every
# credential because a deployment that other machines can reach is not a place for a
# default password.
#
#   docker compose -f docker-compose.host.yml up -d --wait
#
# `podium-server init --transport host` writes a .env with the credentials generated and
# PODIUM_SERVER left for you: that is the URL clients actually dial (this host's address on
# the network you already have). 0.0.0.0 is a bind address, not a URL.
#
# The server, conductor and a co-located worker share the host's network namespace
# (`network_mode: host`), so they inherit this machine's routing table instead of Docker's
# bridge NAT. Podium does not add a tunnel. A worker on the other side of your WireGuard
# (or VPN, or LAN) dials PODIUM_SERVER the same way any other host process would.
#
# Authentication is the local transport's shared token. A VPN does not name the caller the
# way Tailscale WhoIs does, so there is no per-user identity here. Anyone who can route to
# the listen address and holds the token can do everything.
#
# Linux Engine. Docker Desktop's "host networking" is not the same thing — on a Mac run
# the binaries with `make stack-up` instead; they already use this machine's routes.
#
# PODIUM_TRANSPORT is local, not host. `host` as a PODIUM_TRANSPORT value means something
# else: borrow the machine's tailscaled. This file's "host" is Docker's network_mode.
name: podium

services:
  postgres:
    image: pgvector/pgvector:pg16@sha256:ccc6e83d6e35e931dc7c5def2022729d5a6c370318d099181995567ff1fb4d6b
    environment:
      POSTGRES_USER: podium
      POSTGRES_PASSWORD: ${PODIUM_PG_PASSWORD:?set PODIUM_PG_PASSWORD}
      POSTGRES_DB: podium
    volumes:
      - pgdata:/var/lib/postgresql/data
    configs:
      - source: postgres-init               # inline below; runs only on an EMPTY volume
        target: /docker-entrypoint-initdb.d/10-databases.sql
    ports:
      # Loopback on the host, because the server is in the host's network namespace and
      # reaches Postgres at 127.0.0.1. Not published off the machine.
      - "127.0.0.1:${PODIUM_PG_PORT:-5432}:5432"
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U podium -d podium"]
      interval: 2s
      timeout: 3s
      retries: 30
      start_period: 5s
    restart: unless-stopped

  hindsight:
    image: ghcr.io/vectorize-io/hindsight:0.9.2@sha256:84ab276b8f501546deb6ea9c64a57291718b4e16a59dd9e02a02fdd5adfe9028
    depends_on:
      postgres:
        condition: service_healthy
    shm_size: 1g
    environment:
      HINDSIGHT_API_DATABASE_URL: postgresql://podium:${PODIUM_PG_PASSWORD:?}@postgres:5432/podium_memory
      HINDSIGHT_API_TENANT_EXTENSION: hindsight_api.extensions.builtin.tenant:ApiKeyTenantExtension
      # `:-` not `:?`: a `:?` here makes compose refuse to interpolate the WHOLE file, so
      # `docker compose up postgres` would stop working on a deployment with no memory.
      # Left empty, the container refuses to start and names the variable.
      HINDSIGHT_API_TENANT_API_KEY: ${PODIUM_AGENT_MEMORY_API_KEY:-}
      HINDSIGHT_API_LLM_PROVIDER: ${PODIUM_MEMORY_LLM_PROVIDER:-anthropic}
      HINDSIGHT_API_LLM_MODEL: ${PODIUM_MEMORY_LLM_MODEL:-claude-opus-5}
      HINDSIGHT_API_LLM_API_KEY: ${PODIUM_MEMORY_LLM_API_KEY:-}
      HINDSIGHT_API_EMBEDDINGS_PROVIDER: local
      HINDSIGHT_ENABLE_CP: "false"
      HINDSIGHT_API_WORKER_ID: hindsight
    ports:
      # Workers on the other side of the VPN reach this at the host's VPN address. Set a
      # real PODIUM_AGENT_MEMORY_API_KEY before widening the bind; the default below is
      # every interface because that is the point of this file.
      - "${PODIUM_MEMORY_BIND:-0.0.0.0}:${PODIUM_MEMORY_PORT:-8888}:8888"
    healthcheck:
      test: ["CMD-SHELL", "curl -fsS http://127.0.0.1:8888/health >/dev/null || exit 1"]
      interval: 10s
      timeout: 5s
      retries: 30
      start_period: 30s
    restart: unless-stopped

  objectstore:
    image: rustfs/rustfs:1.0.0-rc.5@sha256:c36b3efea3d1e503f1a2581abd0e7611e0e5820dd30e1850a52384b3fc52bda4
    security_opt:
      - "no-new-privileges:true"
    environment:
      RUSTFS_VOLUMES: /data
      RUSTFS_ADDRESS: 0.0.0.0:9000
      RUSTFS_ACCESS_KEY: ${PODIUM_S3_ACCESS_KEY:-podium}
      RUSTFS_SECRET_KEY: ${PODIUM_S3_SECRET_KEY:?set PODIUM_S3_SECRET_KEY, at least 8 characters}
      RUSTFS_CONSOLE_ENABLE: "false"
      RUSTFS_OBS_LOGGER_LEVEL: warn
    volumes:
      - objectstore-data:/data
    ports:
      # Same reason as Postgres: the server is on the host network and dials 127.0.0.1.
      - "127.0.0.1:${PODIUM_S3_PORT:-9000}:9000"
    healthcheck:
      test: ["CMD", "sh", "-ec", "wget -q -O- http://127.0.0.1:9000/health >/dev/null || exit 1"]
      interval: 5s
      timeout: 3s
      retries: 20
      start_period: 5s
    restart: unless-stopped

  init:
    image: busybox:1.37@sha256:9db7b59979c38555a39def84a31fb98b5296952f9e3afd4f6f11f05b07adfab0
    volumes:
      - ${PODIUM_STATE_DIR:-server-state}:/state
    command:
      - sh
      - -ec
      - |
        if [ ! -s /state/master.key ]; then
          umask 077
          head -c 32 /dev/urandom | od -An -tx1 | tr -d '[:space:]' > /state/master.key
          echo "init: generated a master key — back up the server-state volume"
        fi
        chmod 0600 /state/master.key
        chown -R 65532:65532 /state
    restart: "no"

  server:
    image: ${PODIUM_SERVER_IMAGE:-${PODIUM_IMAGE_REPO:-ghcr.io/podium-ade}/podium-server:${PODIUM_IMAGE_TAG:-latest}}
    depends_on:
      postgres:
        condition: service_healthy
      objectstore:
        condition: service_healthy
      init:
        condition: service_completed_successfully
    network_mode: host
    environment:
      # Host network has no compose DNS, so the dependencies are the ports they publish
      # on this machine's loopback.
      PODIUM_DATABASE_URL: postgres://podium:${PODIUM_PG_PASSWORD:?}@127.0.0.1:${PODIUM_PG_PORT:-5432}/podium
      PODIUM_TRANSPORT: local
      # Binds every interface of the HOST, because that is this file's whole point: a
      # worker on the other side of your WireGuard (or VPN, or LAN) has to have something
      # to dial. The token is the only credential. Do not bind a public address.
      PODIUM_LOCAL_LISTEN: ${PODIUM_LOCAL_LISTEN:-0.0.0.0:8080}
      PODIUM_LOCAL_ALLOW_UNSAFE_LISTEN: "true"
      PODIUM_LOCAL_TOKEN: ${PODIUM_LOCAL_TOKEN:?set PODIUM_LOCAL_TOKEN}
      PODIUM_MASTER_KEY_FILE: /var/lib/podium/master.key
      PODIUM_S3_ENDPOINT: 127.0.0.1:${PODIUM_S3_PORT:-9000}
      PODIUM_S3_BUCKET: ${PODIUM_S3_BUCKET:-podium}
      PODIUM_S3_ACCESS_KEY: ${PODIUM_S3_ACCESS_KEY:-podium}
      PODIUM_S3_SECRET_KEY: ${PODIUM_S3_SECRET_KEY:?}
      PODIUM_S3_USE_SSL: "false"
      PODIUM_LOG_ROLLUP_INTERVAL: ${PODIUM_LOG_ROLLUP_INTERVAL:-1m}
      PODIUM_LOG_PRUNE_INTERVAL: ${PODIUM_LOG_PRUNE_INTERVAL:-1h}
      PODIUM_LOG_CHUNK_GRACE: ${PODIUM_LOG_CHUNK_GRACE:-24h}
      PODIUM_AGENT_URL: ${PODIUM_AGENT_URL-http://127.0.0.1:8090}
      PODIUM_AGENT_TOKEN: ${PODIUM_AGENT_TOKEN:?set PODIUM_AGENT_TOKEN}
    volumes:
      - ${PODIUM_STATE_DIR:-server-state}:/var/lib/podium
    restart: unless-stopped
    # No ports: host network binds PODIUM_LOCAL_LISTEN on the machine itself.

  agent:
    image: ${PODIUM_AGENT_IMAGE:-${PODIUM_IMAGE_REPO:-ghcr.io/podium-ade}/podium-agent:${PODIUM_IMAGE_TAG:-latest}}
    depends_on:
      - server
    network_mode: host
    environment:
      PODIUM_AGENT_SERVER: http://127.0.0.1:8080
      PODIUM_AGENT_API_TOKEN: ${PODIUM_LOCAL_TOKEN:?}
      PODIUM_AGENT_DATABASE_URL: postgres://podium:${PODIUM_PG_PASSWORD:?}@127.0.0.1:${PODIUM_PG_PORT:-5432}/podium_agent
      PODIUM_AGENT_LISTEN: 127.0.0.1:8090   # only the server on this host talks to it
      PODIUM_AGENT_TOKEN: ${PODIUM_AGENT_TOKEN:?set PODIUM_AGENT_TOKEN}
      PODIUM_AGENT_PROFILE_DIR: ${PODIUM_AGENT_PROFILE_DIR:-/etc/podium/agent}
      PODIUM_AGENT_SKILLS_DIR: ${PODIUM_AGENT_SKILLS_DIR:-/etc/podium/skills}
      PODIUM_AGENT_SLACK_APP_TOKEN: ${PODIUM_AGENT_SLACK_APP_TOKEN:-}
      PODIUM_AGENT_SLACK_BOT_TOKEN: ${PODIUM_AGENT_SLACK_BOT_TOKEN:-}
      PODIUM_AGENT_LINEAR_API_KEY: ${PODIUM_AGENT_LINEAR_API_KEY:-}
      PODIUM_AGENT_LINEAR_POLL_INTERVAL: ${PODIUM_AGENT_LINEAR_POLL_INTERVAL:-30s}
      PODIUM_AGENT_LINEAR_URL: ${PODIUM_AGENT_LINEAR_URL:-https://api.linear.app/graphql}
      PODIUM_AGENT_UI_URL: ${PODIUM_AGENT_UI_URL:-}
      PODIUM_AGENT_MEMORY_URL: ${PODIUM_AGENT_MEMORY_URL-http://127.0.0.1:8888}
      # A task container on a WORKER. Co-located, host.docker.internal is this machine;
      # a worker elsewhere needs this host's address on your network.
      PODIUM_AGENT_MEMORY_TASK_URL: ${PODIUM_AGENT_MEMORY_TASK_URL:-http://host.docker.internal:8888}
      PODIUM_AGENT_MEMORY_BANK: ${PODIUM_AGENT_MEMORY_BANK:-podium}
      PODIUM_AGENT_MEMORY_API_KEY: ${PODIUM_AGENT_MEMORY_API_KEY:-}
      PODIUM_AGENT_XAI_BASE_URL: ${PODIUM_AGENT_XAI_BASE_URL:-https://api.x.ai}
      PODIUM_AGENT_XAI_OAUTH_ISSUER: ${PODIUM_AGENT_XAI_OAUTH_ISSUER:-https://auth.x.ai}
      PODIUM_AGENT_XAI_OAUTH_CLIENT_ID: ${PODIUM_AGENT_XAI_OAUTH_CLIENT_ID:-}
    volumes:
      # Bare name → named volume (starter copied from the image on first up). A path → bind.
      - ${PODIUM_AGENT_PROFILE_HOST:-agent-profile}:${PODIUM_AGENT_PROFILE_DIR:-/etc/podium/agent}
      - ${PODIUM_AGENT_SKILLS_HOST:-agent-skills}:${PODIUM_AGENT_SKILLS_DIR:-/etc/podium/skills}
    restart: unless-stopped

  node:
    profiles: ["node"]
    image: ${PODIUM_NODE_IMAGE:-${PODIUM_IMAGE_REPO:-ghcr.io/podium-ade}/podium-node:${PODIUM_IMAGE_TAG:-latest}}
    depends_on:
      - server
    network_mode: host
    environment:
      PODIUM_NODE_SERVER: http://127.0.0.1:8080
      PODIUM_NODE_TRANSPORT: local
      PODIUM_NODE_LOCAL_TOKEN: ${PODIUM_LOCAL_TOKEN:?}
      PODIUM_NODE_ENROLL_TOKEN: ${PODIUM_NODE_ENROLL_TOKEN:-}
      PODIUM_NODE_DATA_DIR: /var/lib/podium-node
      PODIUM_NODE_MAX_TASKS: ${PODIUM_NODE_MAX_TASKS:-4}
      PODIUM_NODE_LABELS: ${PODIUM_NODE_LABELS:-}
    volumes:
      - /var/lib/podium-node:/var/lib/podium-node
      - /var/run/docker.sock:/var/run/docker.sock
    restart: unless-stopped

  cli:
    profiles: ["cli"]
    image: ${PODIUM_CLI_IMAGE:-${PODIUM_IMAGE_REPO:-ghcr.io/podium-ade}/podium:${PODIUM_IMAGE_TAG:-latest}}
    depends_on:
      - server
    network_mode: host
    environment:
      PODIUM_SERVER: http://127.0.0.1:8080
      PODIUM_LOCAL_TOKEN: ${PODIUM_LOCAL_TOKEN:?}
    restart: "no"

volumes:
  pgdata:
  objectstore-data:
  server-state:
  agent-profile:
  agent-skills:

# Inline, so this file needs nothing beside it. Byte-identical to postgres/init.sql, which
# docker-compose.dev.yml mounts from disk; `go test ./deploy/...` fails if they drift.
configs:
  postgres-init:
    content: |
      -- The databases beside podium's own. Postgres runs this ONLY on an empty data directory;
      -- to add them to an existing install, see docs/operations.md.
      create database podium_agent owner podium;   -- the conductor's, migrated by podium-agent
      create database podium_memory owner podium;  -- Hindsight's, migrated by Hindsight

      \connect podium_memory

      -- Hindsight needs pgvector in `public` and would otherwise DROP EXTENSION ... CASCADE and
      -- recreate it there itself. podium and podium_agent stay extension-free.
      create extension if not exists vector;
```
<!-- END deploy/docker-compose.host.yml -->

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
`identity.json` and never reads it again. A worker on another machine is the same thing
pointed at `PODIUM_SERVER` with the same token — see
[docs/node-setup.md](docs/node-setup.md).

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

**The master key.** `init` generates it on the first `up`, every stored secret is encrypted
under it, and there is no recovery path. By default it lives in the `server-state` volume, so
copying it out is a command you have to remember — and `down -v` destroys it:

```sh
docker compose cp server:/var/lib/podium/master.key ./master.key
```

Simpler: keep it on the host from the start. Set this **before the first `up`** and the key is
an ordinary file at `./state/master.key`, mode `0600`, that `down -v` cannot touch:

```sh
echo 'PODIUM_STATE_DIR=./state' >> .env
```

A bare name there is a Docker volume, a path is a bind mount, and compose tells them apart by
the leading dot or slash. Either way, get a copy somewhere that is not this machine.

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
- **Shared memory.** Hindsight comes up with everything else, but it wants an LLM key of its
  own for fact extraction and exits at boot without one. It is the only container that does,
  so `up` brings the rest of the stack up regardless — and one line turns it on:

  ```sh
  echo 'PODIUM_MEMORY_LLM_API_KEY=...' >> .env
  docker compose up -d
  ```

  It does not have to be Anthropic. Hindsight runs on
  [any of ~25 providers](https://hindsight.vectorize.io/developer/models) with LiteLLM
  underneath — OpenAI, Gemini, Groq, Bedrock, Vertex AI, or a local Ollama or LM Studio, which
  keeps memory extraction off the network entirely. Set `PODIUM_MEMORY_LLM_PROVIDER` and
  `PODIUM_MEMORY_LLM_MODEL` to match the key; the defaults are `anthropic` and `claude-opus-5`.

  Hindsight has no authentication beyond `PODIUM_AGENT_MEMORY_API_KEY`. The Quickstart
  publishes 8888 on this machine so a turn's container can reach it; set a real key. See
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

## How clients reach the control plane

The Quickstart binds this machine's interfaces. Clients and workers present
`PODIUM_LOCAL_TOKEN` over HTTP. Anyone who can route to that address and holds the token can
do everything — bind a network you already trust, not a public one.

Workers dial out. Point them at `PODIUM_SERVER` with the same token;
[docs/node-setup.md](docs/node-setup.md) is the worker.

**Tailscale is optional.** If you want the control plane on a MagicDNS name with no token at
all — Tailscale's `WhoIs` names every caller — that is `init --transport tailnet` and
[`docker-compose.tailnet.yml`](deploy/docker-compose.tailnet.yml):

```sh
docker run --rm -v "$PWD:/out" --user "$(id -u):$(id -g)" \
  ghcr.io/podium-ade/podium-server:latest \
  init --dir /out --transport tailnet --tailnet <magicdns-suffix>
$EDITOR .env                            # paste TS_AUTHKEY; init reports what else is missing
docker compose -f docker-compose.tailnet.yml up -d --wait

# from any device on the tailnet — no token, no login
podium --server https://podium.<tailnet>.ts.net nodes
```

The tailnet compose file has no `cli` profile: the server listens on port 443 of its own
Tailscale device and has no address on the compose network for a sibling container to reach.

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
- [docs/networking.md](docs/networking.md) — how clients reach the control plane, Tailscale, the ACL, troubleshooting
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

Every daemon is configured entirely by environment, and **one file is the whole of it**: a
`.env` beside the compose file. The compose file interpolates it, and `set -a; . .env; set +a`
configures a host CLI from the same lines. Nothing here asks you to declare a variable
anywhere else. [`deploy/.env.example`](deploy/.env.example) documents every variable there is
with its default, and `go test ./deploy/...` fails the build if the code reads one that file
does not mention, or if that file documents one nothing reads any more.

`podium-server init` writes the credentials. The Quickstart compose file fails closed without
them — a deployment other machines can reach is not a place for a default password. What you
set beyond that falls into three tiers —
[`deploy/README.md`](deploy/README.md#what-you-have-to-configure) has the full version with
consequences:

| tier | | |
|---|---|---|
| **Required to start** | minted by `init` | `PODIUM_LOCAL_TOKEN`, `PODIUM_PG_PASSWORD`, `PODIUM_S3_SECRET_KEY`, `PODIUM_AGENT_TOKEN`. Fill `PODIUM_SERVER` yourself — this machine's address on your network |
| **Unlocks a feature** | one variable each, and without it only that feature is off | `PODIUM_MEMORY_LLM_API_KEY` (shared memory), `PODIUM_NODE_ENROLL_TOKEN` (a worker's first run), `PODIUM_AGENT_SLACK_*` / `PODIUM_AGENT_LINEAR_API_KEY` (those sources), `TS_AUTHKEY` + `PODIUM_TAILNET` (Tailscale) |
| **Just config** | ports, intervals, models, poll rates, labels, base URLs — all defaulted | `PODIUM_IMAGE_TAG` is the one to pin regardless: `latest` moves, and a control plane and worker from different releases can disagree about the wire |

Running the binaries by hand is the one case with genuinely required variables — seven, which
the compose file supplies and `make stack-up` mostly derives. `podium-server init` writes the
credentials with fresh random values and a master key beside them.

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
- **Resolved secret values cross unencrypted HTTP.** The Quickstart's wire is plain HTTP; the
  network you put the control plane on (WireGuard, a VPN, a LAN) is what encrypts it, if
  anything does. Tailscale's path is WireGuard end to end.
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
