# Deploying Podium

> **No `ghcr.io` image has been published yet**, because there has been no `v*` tag. The
> compose files here work — they were run end to end on 2026-09-10 against images built from
> `docker/*.Dockerfile` and pulled from a registry: `podium-server init` in a container,
> `up --wait`, node enrolment through the `cli` profile, and a task exiting 0 with its own
> output. Until there is a release, build the four images and point `PODIUM_IMAGE_REPO` at a
> registry your machines can reach — [the recipe is in the
> quickstart](../docs/quickstart.md#building-the-images-yourself).
>
> `install-node.sh` needs a release archive to download, so it has still never run.

---

## What is in here

| | |
|---|---|
| `docker-compose.yml` | **the whole deployment in one file** — Postgres, the object store, the control plane and the conductor, with the Postgres bootstrap script inline and nothing to fetch beside it. A plain `up` is the whole control plane, the conductor and the agents' shared memory; the `cli` and `node` compose profiles add the CLI as a one-shot and a worker on this machine |
| `docker-compose.tailnet.yml` | the same deployment on a tailnet, so workers can be on other machines: no published ports at all, no token anywhere, and it **fails closed** on every credential rather than shipping defaults. Also one file — the Postgres script is inline in it too |
| `docker-compose.dev.yml` | Postgres, with Hindsight and the object store behind profiles, for running the binaries by hand |
| `run-host.sh` | runs `podium-server`, `podium-agent` and `podium-node` as host binaries from the same `.env`. `make stack-up` |
| `.env.example` | **every** `PODIUM_*` variable, commented. A test fails the build if the code reads one this file does not mention |
| `install-node.sh` | turns a Linux machine into a worker: checks, downloads, verifies, configures, starts, waits |
| `systemd/podium-node.service` | the hardened unit `install-node.sh` installs |
| `docker/*.Dockerfile` | the four service images — `server`, `node`, `agent`, `cli` — base pinned by digest. None has ever been built |
| `tailscale-acl.example.json` | the ACL policy from the networking design |

---

### Which compose file

Three, and they are not variations on one theme — they answer different questions.

| | `docker-compose.yml` | `docker-compose.tailnet.yml` | `docker-compose.dev.yml` |
|---|---|---|---|
| **for** | running Podium on one machine | running it across machines | working **on** Podium |
| **Podium itself** | in containers | in containers | **on your host**, as binaries you built |
| **transport** | `local` — loopback, one shared token | `tailnet` — HTTPS on a MagicDNS name, no token at all | whichever you configure the binary for |
| **services** | postgres, hindsight, objectstore, init, server, agent, +`node`/`cli` profiles | the same, +`node` profile, no `cli` (see below) | postgres, +hindsight/objectstore behind `memory`/`artifacts` |
| **published ports** | server on `127.0.0.1:8080` | **none**; the server is on :443 of its own Tailscale device | postgres, objectstore, hindsight — all loopback, so host binaries can reach them |
| **credentials** | **defaults**, so `up` needs nothing | **fails closed** — six variables with no default | dev values |
| **workers** | this machine only | anywhere on your tailnet | your own `make stack-up` node |
| **files needed** | one | one | the repo you are working in |
| **verified** | end to end, from registry images | transport yes, this file no | daily |

The differences that look like inconsistencies and are not:

- **Postgres is published in `dev.yml` and nowhere else.** A host binary has to reach it; a
  container reaches it by service name over the compose network.
- **Only the tailnet file fails closed on credentials.** A single machine on loopback can
  afford a default token; a deployment that workers on other machines can reach cannot.
- **There is no `cli` profile in the tailnet file.** Under that transport the server has no
  address on the compose network at all, so a sibling container could not reach it. Run the
  CLI from any device on the tailnet, where it needs no token.
- **`dev.yml` mounts `postgres/init.sql`; the other two inline it.** Those two are meant to be
  saved on their own; `dev.yml` is only ever used inside a clone. A test keeps all three
  copies identical.

## What you have to configure

**Which variables are required depends on how you run Podium**, and the two answers are very
different. [`.env.example`](.env.example) documents every variable there is, with its default,
and a test fails the build if the two drift apart.

### Required to start: nothing

Under [`docker-compose.yml`](docker-compose.yml) every variable has a working default, so
`docker compose up -d --wait` needs no `.env` at all. A test enforces that — the compose file
may not interpolate anything with `:?`, because one such variable turns `up` into an error
message.

That is a starting point, not a finishing one. The next two tables are the things you will
actually want to set.

### Required for a feature to work at all

Each of these switches something on. Without it that one thing does not work, and nothing else
is affected.

| | what it unlocks | without it |
|---|---|---|
| `PODIUM_MEMORY_LLM_API_KEY` | the agents' shared memory | the `hindsight` container exits at boot and keeps restarting. It is the one container a bare `up` leaves broken. Any of [~25 providers](https://hindsight.vectorize.io/developer/models), chosen with `PODIUM_MEMORY_LLM_PROVIDER` |
| `PODIUM_NODE_ENROLL_TOKEN` | a worker's **first** run | the node cannot enrol. Single-use, one hour, and only a running control plane can mint one: `docker compose run --rm cli node enroll-token --label demo`. After enrolling, `identity.json` is the identity and this is never read again |
| `PODIUM_AGENT_SLACK_APP_TOKEN` + `PODIUM_AGENT_SLACK_BOT_TOKEN` | the Slack source | no Slack bot. **Both or neither** — one alone is a startup error naming the other |
| `PODIUM_AGENT_LINEAR_API_KEY` | the Linear source | no Linear source. A key that is set and does not work stops the conductor at boot |
| `TS_AUTHKEY` + `PODIUM_TAILNET` | the tailnet transport | `docker-compose.tailnet.yml` refuses to interpolate. Nobody can default these, and failing closed is correct. `PODIUM_NODE_TS_AUTHKEY` is the worker's equivalent |
| `PODIUM_AGENT_UI_URL` | correct links in Slack and Linear | links point at `http://server:8080`, which is a name only the compose network can resolve. Cosmetic, and immediately visible |

### Defaults that are credentials

These have values, so nothing forces you to choose. Replace them before anything you would
miss — and note **`PODIUM_PG_PASSWORD` has to be set before the first `up`**, because it is
baked into the Postgres volume when it is initialised; afterwards it takes a `down -v` or an
`ALTER ROLE`.

| | default | reachable from |
|---|---|---|
| `PODIUM_LOCAL_TOKEN` | `podium` | **`127.0.0.1:8080`.** The only one of these that leaves the compose network, and the only thing between a caller and the whole API — there is no per-user identity under this transport and no RBAC anywhere |
| `PODIUM_AGENT_MEMORY_API_KEY` | `podium` | the memory port, which is loopback by default *because* this has a default. Hindsight has no authentication beyond it |
| `PODIUM_PG_PASSWORD` | `podium` | the compose network only — no published port goes near Postgres |
| `PODIUM_S3_SECRET_KEY` | `podiumpodium` | the compose network only |
| `PODIUM_AGENT_TOKEN` | `podium` | the compose network only |

```sh
printf 'PODIUM_LOCAL_TOKEN=%s\nPODIUM_PG_PASSWORD=%s\nPODIUM_S3_SECRET_KEY=%s\nPODIUM_AGENT_TOKEN=%s\n' \
  "$(openssl rand -hex 32)" "$(openssl rand -hex 16)" "$(openssl rand -hex 16)" "$(openssl rand -hex 32)" > .env
```

### Required only if you run the binaries yourself

`.env.example` marks seven variables as required, and every one of them is about running
`podium-server` and `podium-agent` by hand — the compose files supply all seven. If you are
using compose, ignore them:

`PODIUM_DATABASE_URL`, `PODIUM_LOCAL_TOKEN`, `PODIUM_PG_PASSWORD`, `PODIUM_AGENT_SERVER`,
`PODIUM_AGENT_API_TOKEN`, `PODIUM_AGENT_DATABASE_URL`, `PODIUM_AGENT_TOKEN`.

`make stack-up` derives most of them anyway — `PODIUM_DATABASE_URL` from `PODIUM_PG_PASSWORD`,
the node's and conductor's copies of the shared token from `PODIUM_LOCAL_TOKEN` — and
`podium-server init` writes the credentials with fresh random values. See
[From a clone](#from-a-clone-to-work-on-podium).

### Everything else is config

Ports, intervals, model names, poll rates, labels, slot counts, base URLs. All of them have
defaults that work, and all of them are documented with their default in
[`.env.example`](.env.example). Two worth knowing about because they bite rather than break:

| | |
|---|---|
| `PODIUM_IMAGE_TAG` | **pin it.** `latest` moves under you, and a control plane and a worker from different releases can disagree about the wire |
| `PODIUM_PORT`, `PODIUM_PG_PORT`, `PODIUM_S3_PORT`, `PODIUM_MEMORY_PORT` | move a published port when something on the machine already owns it |

---

The rest of this section is the same ground by component, with the per-variable detail.
`podium-server init` writes the ones marked ⚙ with fresh random values.

### Always

| | |
|---|---|
| `PODIUM_DATABASE_URL` | the Postgres DSN. The server migrates on start. The compose files and `run-host.sh` both build it from ⚙ `PODIUM_PG_PASSWORD` instead, so you set one or the other, never both |
| `PODIUM_TRANSPORT` | `local` or `tailnet`. Decides everything in the next table |
| `PODIUM_MASTER_KEY_FILE` | the file holding the AES-256 key every secret is encrypted under. Leaving it unset is supported and means secrets are unavailable — the server starts and every secret call answers `FailedPrecondition`. **There is no recovery path.** Back the file up somewhere that is not the control plane |
| `PODIUM_SERVER` | where the **CLI** looks for the control plane. Sourcing this file is what configures a shell: `set -a; . deploy/.env; set +a`. The CLI's token is `PODIUM_LOCAL_TOKEN`, which it reads directly — `PODIUM_TOKEN` exists only to point it at some other stack |

### The transport

| `PODIUM_TRANSPORT=local` | |
|---|---|
| ⚙ `PODIUM_LOCAL_TOKEN` | the one shared bearer. The server, every node, the conductor, the web UI and the CLI all present it. Whoever holds it can do everything |
| `PODIUM_LOCAL_LISTEN` | defaults to `127.0.0.1:8080` and **must** be loopback — the token is the only credential there is |
| `PODIUM_LOCAL_ALLOW_UNSAFE_LISTEN` | waives that rule. Set by `docker-compose.yml`, because loopback inside a container is the container's own. **Never set it on a host** |

| `PODIUM_TRANSPORT=tailnet` | |
|---|---|
| `TS_AUTHKEY` | the server's Tailscale auth key, Reusable and Pre-approved, tagged `tag:podium-server`. Read on the **first run only** — after that the state directory is the identity |
| `PODIUM_NODE_TS_AUTHKEY` | the same for a worker, tagged `tag:podium-node` |
| `PODIUM_TS_HOSTNAME` | the device name, and so the first label of the MagicDNS name. Defaults to `podium` |
| `PODIUM_TS_STATE_DIR` | where tsnet keeps the node key. **Must persist**, or the server registers a new device every restart and the name drifts to `podium-1`, `podium-2` |
| `PODIUM_TAILNET` | your MagicDNS suffix without `.ts.net`. Only `run-host.sh` reads it, to build `PODIUM_SERVER` |

There is no token under `tailnet`. Tailscale's `WhoIs` names every caller, so
`PODIUM_LOCAL_TOKEN`, `PODIUM_TOKEN` and `PODIUM_AGENT_API_TOKEN` are all unset there.

### Artifacts and rolled-up logs, if you want them

All or nothing: setting `PODIUM_S3_ENDPOINT` without credentials is a startup error, and
setting none of them turns artifacts off — which is supported and says so at startup.

| | |
|---|---|
| `PODIUM_S3_ENDPOINT` `PODIUM_S3_BUCKET` `PODIUM_S3_ACCESS_KEY` ⚙ `PODIUM_S3_SECRET_KEY` | the object store. `PODIUM_S3_USE_SSL` defaults to `false` |

### The conductor, if you want the Agent tab

The server refuses to start with `PODIUM_AGENT_URL` set and no token, because a proxy that
forwards an unauthenticated request into the conductor is worse than no proxy.

| on `podium-server` | |
|---|---|
| `PODIUM_AGENT_URL` | where the conductor listens. Setting it is what makes the Agent tab appear |
| ⚙ `PODIUM_AGENT_TOKEN` | the bearer the server presents to it |

| on `podium-agent` | |
|---|---|
| `PODIUM_AGENT_SERVER` | the Podium API base URL |
| `PODIUM_AGENT_API_TOKEN` | required whenever that URL is `http://`. Unset on a tailnet |
| `PODIUM_AGENT_DATABASE_URL` | its **own** database, `podium_agent`. It never opens the server's |
| ⚙ `PODIUM_AGENT_TOKEN` | the same value as above. One line, both sides |
| `PODIUM_AGENT_PROFILE_DIR` | must contain `profile.yaml`. Defaults to `/etc/podium/agent`, so in a checkout point it at `examples/agent` |

Then, each switching on one feature and each optional:

| | |
|---|---|
| `PODIUM_AGENT_SLACK_APP_TOKEN` + `PODIUM_AGENT_SLACK_BOT_TOKEN` | Socket Mode. **Both or neither** — one alone is a startup error naming the other |
| `PODIUM_AGENT_LINEAR_API_KEY` | the Linear source |
| `PODIUM_AGENT_MEMORY_URL` + `PODIUM_AGENT_MEMORY_API_KEY` | the agents' shared memory. The key is required once the URL is set. `PODIUM_AGENT_MEMORY_TASK_URL` is the same service as a **task container** must address it, which is not loopback, and `PODIUM_AGENT_MEMORY_BANK` names the bank |
| `PODIUM_AGENT_SKILLS_DIR` | travels with `PODIUM_AGENT_PROFILE_DIR`: a playbook naming a skill that is not there fails every turn |
| `PODIUM_AGENT_HOST_RUNTIME` + `PODIUM_AGENT_RUNNER_BIN` | answer a turn as a child process instead of a container. Opt-in, and a security decision — read [`../docs/security.md`](../docs/security.md) first. `auto` under `make stack-up` means this checkout's own build |

The memory service is a container of its own, and its key is **not** the one above:
`PODIUM_MEMORY_LLM_API_KEY` is read by Hindsight at start-up and is visible in
`docker inspect`. Give it its own scoped key. Reads need no key at all; only writing does.

### The worker

| | |
|---|---|
| `PODIUM_NODE_SERVER` | the control plane. `http://` under `local`, `https://` under `tailnet` — the daemon refuses a mismatch |
| `PODIUM_NODE_TRANSPORT` | matches the server's |
| `PODIUM_NODE_LOCAL_TOKEN` | under `local`: the server's `PODIUM_LOCAL_TOKEN` |
| `PODIUM_NODE_ENROLL_TOKEN` | from `podium node enroll-token`. **Single use, first run only** — after that `identity.json` in the data directory is the identity. Not the same thing as `TS_AUTHKEY` |
| `PODIUM_NODE_DATA_DIR` | holds that `identity.json`. Losing it means re-enrolling |
| `PODIUM_NODE_LABELS` | what task specs match on |
| `PODIUM_NODE_ALLOW_PRIVILEGED_SIDECARS` | only on a worker dedicated to it, paired with a label so nothing else lands there |

### Read by the compose files, not by any binary

⚙ `PODIUM_PG_PASSWORD`, and `PODIUM_IMAGE_TAG` — **pin it**, `latest` moves, and a control
plane and a worker from different releases can disagree about the wire. `PODIUM_IMAGE_REPO`
says where the four images come from, and defaults to `ghcr.io/podium-ade`; set it if you
mirror them, which an air-gapped or pull-through deployment has to, or if you built them
yourself. `PODIUM_PORT`, `PODIUM_PG_PORT` and `PODIUM_S3_PORT` move a published port when
something already owns it.

`run-host.sh` and `install-node.sh` have a handful of their own, all documented at the bottom
of `.env.example`. The installer's are deliberately **not** the daemon's names — it takes
`PODIUM_LABELS` and writes `labels:` into `/etc/podium/node.yaml`, which the daemon then
reads as `PODIUM_NODE_LABELS`.

---

## A single machine, with Docker

One file, one command, and no Go toolchain or binary on the host:

```sh
mkdir podium && cd podium
curl -fsSLO https://raw.githubusercontent.com/podium-ade/podium/main/deploy/docker-compose.yml
docker compose up -d --wait
open http://127.0.0.1:8080          # the token is `podium`
```

Six containers: Postgres, the agents' shared memory, the object store, a one-shot that
generates the master key into the `server-state` volume, the control plane, and the conductor.
Three things that used to need a file on disk no longer do — the Postgres bootstrap script is
inline in the compose file, the conductor's profile directory ships in its image, and the
master key is generated rather than carried.

Hindsight wants an LLM key of its own for fact extraction (`PODIUM_MEMORY_LLM_API_KEY`) and
exits at boot without one, so it is the single container that will be restarting after a bare
`up`. Nothing else depends on it. The key can be from
[any of its ~25 providers](https://hindsight.vectorize.io/developer/models) — set
`PODIUM_MEMORY_LLM_PROVIDER` and `PODIUM_MEMORY_LLM_MODEL` to match; a local `ollama` keeps
extraction off the network.

**It ships default credentials**, which is the trade that makes that one command possible.
`PODIUM_PG_PASSWORD`, `PODIUM_S3_SECRET_KEY` and `PODIUM_AGENT_TOKEN` are reachable only from
inside the compose network. `PODIUM_LOCAL_TOKEN` is the exception and the one that matters: it
is the only thing between a caller and the whole API, and 8080 is published — on `127.0.0.1`
alone, so the exposure is anyone on that machine. Override them in a `.env` beside the compose
file **before the first `up`**, because `PODIUM_PG_PASSWORD` is baked into the Postgres volume
when it is initialised:

```sh
printf 'PODIUM_LOCAL_TOKEN=%s\nPODIUM_PG_PASSWORD=%s\nPODIUM_S3_SECRET_KEY=%s\n' \
  "$(openssl rand -hex 32)" "$(openssl rand -hex 16)" "$(openssl rand -hex 16)" > .env
echo "PODIUM_IMAGE_TAG=v0.1.0" >> .env          # pin it; `latest` moves under you
```

**Back up the `server-state` volume.** It holds the master key every stored secret is
encrypted under, and there is no recovery path:

```sh
docker compose cp server:/var/lib/podium/master.key ./master.key
```

Three things stay behind a compose profile:

| profile | what it adds |
|---|---|
| `cli` | the `podium` CLI as a one-shot. `docker compose run` turns it on by itself: `docker compose run --rm cli nodes`. A `--spec` has to be mounted where the container can see it |
| `node` | a worker on **this** machine. Needs a `PODIUM_NODE_ENROLL_TOKEN` in `.env` first, and mounts the host's Docker socket — root-equivalent on that host |


Then a worker, on this machine:

```sh
echo "PODIUM_NODE_ENROLL_TOKEN=$(docker compose run --rm cli \
  node enroll-token --label linux/amd64)" >> .env
docker compose --profile node up -d
```

or on another machine — for which the `local` transport is the wrong tool, being loopback-only.
Use [a tailnet](#a-tailnet-host) and then [`../docs/node-setup.md`](../docs/node-setup.md).

Two things worth knowing before you rely on the worker:

- **Its data directory is a host path**, `/var/lib/podium-node`, at the same absolute path
  inside the container and out. It has to be: the node drives the *host's* daemon, so every
  bind mount it asks for — including the `podium-runner` that is PID 1 in every task
  container — is resolved by that daemon against the host filesystem. From a named volume,
  every task dies at creation with `bind source path does not exist`.
- **`docker compose down -v` does not remove it.** Bring a fresh stack up against an old data
  directory and the node loops on `unauthenticated: unknown node key` forever rather than
  failing, because `identity.json` is still there so it never reads the new enrollment token.
  Starting genuinely from scratch means emptying that directory too.

## From a clone, to work on Podium

This is the path for changing Podium rather than running it: host binaries you just built,
with only the dependencies in containers. It needs Docker, Go and Node.

```sh
make build                                             # bin/podium-{server,agent,node,podium}
docker compose -f deploy/docker-compose.dev.yml \
  --profile memory --profile artifacts up -d --wait    # Postgres, Hindsight, object store
./bin/podium-server init --dir deploy                  # master.key + deploy/.env, mode 0600
set -a; . deploy/.env; set +a                          # the same file configures your CLI

make stack-up                                          # server, then conductor, then node
echo "PODIUM_NODE_ENROLL_TOKEN=$(./bin/podium node enroll-token --label demo)" >> deploy/.env
make stack-up S=node                                   # once the worker has a token to enrol with
make stack-status
```

`init` fills in everything the stack needs except the enrollment token, which only a running
control plane can mint and which is single-use. `make stack-down` stops everything, and
`S=` names one service to act on. Logs and pids are under `.podium/`, which is gitignored.

`run-host.sh` reads the same `deploy/.env` the compose files read — the one `init` writes,
holding `PODIUM_PG_PASSWORD` rather than a whole `PODIUM_DATABASE_URL` — and does the
derivations the compose files do in YAML. Anything already exported wins over the file, so
`PODIUM_AGENT_PROFILE_DIR=… make stack-up` works for a one-off.

The conductor comes up on [`../examples/agent`](../examples/agent), the worked example, which
loads and runs on any node. To run **this repository's own bot** instead, name its profile and
its skills in `.env` — the two travel together, because its `podium` playbook names a skill
and a playbook whose skill is missing fails its turns:

```sh
PODIUM_AGENT_PROFILE_DIR=/srv/podium/playbooks
PODIUM_AGENT_SKILLS_DIR=/srv/podium/skills
```

That bot wants a node started with `--allow-privileged-sidecars` and labelled `privileged`, a
`podium.agent.github_token` secret, and roughly 9 GB free for the turn and its two sidecars.
See [`../playbooks/README.md`](../playbooks/README.md).

Under `PODIUM_TRANSPORT=tailnet` this is the **only** way to run the conductor: the server
listens on :443 of its own Tailscale device and has no port on the compose network, so a
sibling container cannot reach it. `docker-compose.tailnet.yml` says as much where it defines
its `agent` service.

---

## A tailnet host

```sh
docker run --rm -v "$PWD:/out" --user "$(id -u):$(id -g)" \
  ghcr.io/podium-ade/podium-server:latest \
  init --dir /out --transport tailnet --tailnet <your MagicDNS suffix>
# fill TS_AUTHKEY and PODIUM_NODE_TS_AUTHKEY in .env
docker compose -f docker-compose.tailnet.yml up -d --wait
```

There is deliberately no `cli` profile in that file: the server has no address on the compose
network for a sibling container to reach, so run the CLI from a device on the tailnet, where it
needs no token at all.

There are no published ports in that file at all. The server listens on port 443 of its own
Tailscale device; workers dial out and listen for nothing.

Read [`../docs/networking.md`](../docs/networking.md) **first**. Four things have to be set up in
the Tailscale admin console and Podium cannot do any of them for you:

1. **DNS → MagicDNS**: on.
2. **DNS → HTTPS Certificates**: on. Without it the server cannot get a certificate and refuses
   to start.
3. **Access Controls**: merge `tailscale-acl.example.json` into your policy.
4. **Settings → Keys**: two auth keys, both **Reusable** and **Pre-approved** — one tagged
   `tag:podium-server`, one tagged `tag:podium-node`.

A Tailscale auth key and a Podium enrollment token are different things and everyone confuses
them. A new worker needs both.

## The node installer

`install-node.sh` is meant to be piped into `bash` as root. It:

1. refuses anything that is not Linux, and anything that is not amd64 or arm64;
2. refuses Docker older than 24 (the node negotiates Engine API 1.43) and any machine on cgroup
   v1 (every resource limit and the OOM report read the unified hierarchy);
3. resolves `latest` to a real tag, or takes `PODIUM_VERSION`;
4. downloads the archive **and `checksums.txt`**, and verifies the SHA-256 before unpacking —
   a download it cannot verify is refused, not installed;
5. writes `/etc/podium/node.yaml` atomically, mode 0600 (it holds tokens);
6. installs the systemd unit **from the same verified archive**, so the unit and the binary are
   always the same release;
7. starts the service and polls the node's own `/readyz` until it is online, printing the last 40
   journal lines if it is not.

| variable | |
|---|---|
| `PODIUM_SERVER` | **required.** `https://…` selects the tailnet transport, `http://…` the dev one |
| `PODIUM_ENROLL_TOKEN` | required unless this machine has already enrolled |
| `TS_AUTHKEY` | tailnet only, first run only |
| `PODIUM_LOCAL_TOKEN` | local transport only |
| `PODIUM_LABELS` | comma separated; what scheduling matches on |
| `PODIUM_MAX_TASKS` | default 4 |
| `PODIUM_VERSION` | default `latest` |
| `PODIUM_DATA_DIR` | default `/var/lib/podium-node`. Never touched by a re-run |
| `PODIUM_METRICS_LISTEN` | default `127.0.0.1:9091`. Written into `node.yaml`, and the address step 7 polls |

Re-running it upgrades the binary and rewrites the config. It never touches the data directory,
which holds the node's identity. It does **not** drain first — `podium-node upgrade` does that;
see [`../docs/cli.md`](../docs/cli.md#podium-node-subcommands).

## The systemd unit

`systemd/podium-node.service` runs the daemon as **root**, deliberately: its whole job is
`/var/run/docker.sock`, and anything that can talk to that socket can start a privileged
container and own the machine. A dedicated user in the `docker` group is the same power with a
longer name. `SupplementaryGroups=docker` is kept so that changing `User=` works anyway.

`ProtectSystem=strict`, `ProtectHome`, `NoNewPrivileges` and the rest protect the host from the
daemon's *mistakes*. They do not protect the host from the daemon, and they cannot.

`PrivateTmp` is deliberately **not** set, and there is no configuration where it may be: when
`data_dir` is deep enough that a task's event socket would exceed the 108-byte `sun_path` limit,
the node falls back to creating it under `/tmp` and bind-mounting it into the container — and a
private `/tmp` is invisible to `dockerd`. It is `/tmp` being the *host's* `/tmp` that the
fallback depends on. `ReadWritePaths` names `/tmp` for the same reason: `ProtectSystem=strict`
would otherwise make it read-only and the fallback would fail with `EROFS`. Keep `data_dir` short
(under 42 characters) and the fallback never fires.

`systemd-analyze verify` has **not** been run on this unit: the build machine is macOS. The
directives were checked by inspection against systemd's documentation.
