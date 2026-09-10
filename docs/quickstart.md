# Quickstart

A control plane, a worker and a task you can watch run — in about ten commands.

This uses the **`local` transport**: server and worker on one machine, over loopback, with one
shared token. It is the right thing for a first look, and it is loopback-only — it cannot reach
a worker on another machine at all. For that you need the tailnet transport, which is the only
supported way to do it: [`networking.md`](networking.md).

**Everything is configured by one file, `deploy/.env`.** Nothing below exports a variable: each
daemon, the compose files and your own shell all read that file, so there is only ever one
place to change something and nothing to keep in step by hand.

> **There is no released binary yet.** Podium has never been tagged, so there is nothing to
> `curl` and no image to pull, and `deploy/docker-compose.yml` cannot start `podium-server`.
> Building from source is the only way in today. The [Deploying with compose](#deploying-with-compose)
> section below is what it will look like once there is a release.

---

## Prerequisites

| | |
|---|---|
| Go | 1.27 or newer (`go.mod` sets the floor, and says why) |
| Docker | Engine 24+, **cgroup v2**, and a daemon you can reach |
| Node + pnpm | 22+ and 11.x, to build the web UI. `go build -tags noui ./...` skips it entirely |
| make, git | |

Check the two that actually bite:

```sh
docker info --format 'engine {{.ServerVersion}}, cgroup v{{.CgroupVersion}}'
```

cgroup v1 will not work. Memory limits, PID limits and the OOM report all read the unified
hierarchy, and the node refuses to start rather than silently ignoring every limit a task asks
for.

---

## 1. Build

```sh
git clone https://github.com/alvaroibarguen/podium.git
cd podium
make build
```

That builds the web UI, cross-compiles the two Linux `podium-runner` binaries that
`podium-node` embeds, and writes `podium`, `podium-server`, `podium-node` and `podium-agent`
into `bin/`.

## 2. Configure it

One file holds everything. Copy the template and set three lines:

```sh
cp deploy/.env.example deploy/.env
$EDITOR deploy/.env
```

```ini
PODIUM_LOCAL_TOKEN=devtoken             # the one shared secret; pick any string
PODIUM_SERVER=http://127.0.0.1:8080     # where the CLI looks for the control plane
PODIUM_TRANSPORT=local                  # the default, and what this walkthrough uses
```

That is the whole of it. `PODIUM_LOCAL_TOKEN` is the `local` transport's only credential —
the server, the worker, the web UI and the CLI all present the same string, and each reads
it from that one line. Everything else in `.env.example` has a working default and is
commented with what it does.

> `./bin/podium-server init --dir deploy` writes the same file with fresh random credentials
> instead of ones you chose, plus the master key, and refuses to overwrite either. Use it for
> anything real; type your own for a first look.

**Make a master key.** Secrets are optional — Podium is a task runner without them — but it
costs one command now and saves rediscovering it later:

```sh
./bin/podium-server gen-master-key --out deploy/master.key
```

Mode 0600, refuses to overwrite, and **there is no recovery path**: whatever encrypts your
secrets is only in that file. `make stack-up` looks for it beside the `.env`, which is why no
variable names it here. On a real deployment put a copy somewhere off the control plane.

## 3. Start Postgres

```sh
docker compose -f deploy/docker-compose.dev.yml up -d --wait postgres
```

`--wait` matters: a bare port probe races the server's first connection.

The image is `pgvector/pgvector:pg16` — Postgres 16 with the `pgvector` extension available.
Podium's own schema does not use it; the agents' shared memory does. An existing
`podium-pgdata` volume from an earlier release keeps working: it is the same major version.

The compose file creates the conductor's database and the memory service's alongside Podium's
own, but Postgres runs init scripts only on an **empty** data directory. On a volume that
already exists, do it by hand once:

```sh
docker exec podium-dev-postgres createdb -U podium podium_agent
docker exec podium-dev-postgres createdb -U podium podium_memory
docker exec podium-dev-postgres psql -U podium -d podium_memory \
  -c 'create extension if not exists vector'
```

## 4. Start the control plane

```sh
make stack-up S=server
```

`make stack-up` runs [`deploy/run-host.sh`](../deploy/run-host.sh), which sources `deploy/.env`
and starts the binary `make build` produced. It derives what it can rather than making you
write it down: `PODIUM_DATABASE_URL` from `PODIUM_PG_PASSWORD` and `PODIUM_PG_PORT`,
`PODIUM_MASTER_KEY_FILE` from the directory the `.env` is in, and the node's and conductor's
copies of the shared token from `PODIUM_LOCAL_TOKEN`. Anything already in your environment
wins over the file, so a one-off `PODIUM_AGENT_PROFILE_DIR=… make stack-up` still works.

The server migrates the schema on start. `PODIUM_LOCAL_LISTEN` defaults to `127.0.0.1:8080`
and **must** resolve to loopback — the server refuses to start otherwise, because the shared
token is the only credential there is.

Logs are in `.podium/log/`, and `make stack-status` says what is up.

## 5. Point your shell at it

The same file configures the CLI:

```sh
set -a; . deploy/.env; set +a

./bin/podium version
```

```
podium dev (none)
server dev (none)
```

Two lines, and a warning on the third if the CLI and the control plane are different builds.
(`dev` there is the build version of an untagged binary, not the transport.)

The CLI reads `PODIUM_SERVER` for the address and `PODIUM_LOCAL_TOKEN` for the credential —
the same line the server reads, so there is no second copy to drift. `PODIUM_TOKEN` exists
only to point the CLI at some *other* stack, and `--server` / `--token` beat both.

## 6. Enrol a worker

The enrollment token is the one value that cannot be written ahead of time: only a running
control plane can mint one.

```sh
echo "PODIUM_NODE_ENROLL_TOKEN=$(./bin/podium node enroll-token --label demo)" >> deploy/.env
make stack-up S=node
```

Single use, one hour, and the plaintext is printed once and never stored — the database keeps
only its SHA-256. It goes to stdout and nothing else does, so `$(...)` captures just the token.

It is needed on the **first run only**. After that `identity.json` in the node's data directory
is its identity — a credential the server issued once and cannot reissue. A Tailscale auth key
and a Podium enrollment token are different things, and everyone confuses them; on the `local`
transport there is no Tailscale at all.

```sh
./bin/podium nodes
```

```
NAME       ID                              STATUS   LABELS   RUNNING/MAX   HEARTBEAT
my-laptop  node_01m1j889e944prdn29s2x2d6pa  online   demo     0/4           1s ago
```

This step is optional in the sense that it is a choice, not a formality: it is the machine you
are already on volunteering to run tasks. Skip it and you have a control plane and a UI with
nothing to schedule onto, and a submitted task sits in `queued` saying why.

## 7. Run something

```sh
./bin/podium run --image alpine:3 -- \
  sh -c 'for i in 1 2 3; do echo tick $i; sleep 1; done; exit 3'
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

**The CLI exits with the task's exit code** — 3 here. That is what makes `podium run` usable as
a CI step. Podium's own commentary goes to stderr with a `→`, so
`podium run ... > out.txt` captures exactly the task's stdout.

## 8. Run something with a database beside it

```sh
./bin/podium run --spec examples/postgres-sidecar.yaml
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

A sidecar is a sibling container on the task's private network, reachable by the name it is
keyed under — `psql -h db` — started before the task and waited for. See
[`task-spec.md`](task-spec.md#sidecars) and the other files in [`examples/`](../examples).

## 9. Open the UI

```sh
open http://127.0.0.1:8080
```

It asks once for the bearer token — whatever you put in `PODIUM_LOCAL_TOKEN` — and keeps it in
`localStorage`, which is per browser origin: `127.0.0.1:8080` and `localhost:8080` each hold
their own copy, so pick one address and stay on it. On a tailnet that prompt never appears,
because Tailscale has already said who you are.

The UI runs the fleet rather than just watching it: submit a task from a form or from the same
YAML `--spec` takes, re-run a finished one, follow live logs with a per-sidecar filter, cancel,
download artifacts, drain / undrain / delete / rekey a node, and set or delete secrets. A task
still `queued` says which of the scheduler's reasons is keeping it there.

---

## Tearing it down

```sh
make stack-down
docker compose -f deploy/docker-compose.dev.yml down -v
```

A clean run leaves nothing behind. Check:

```sh
docker ps -a --filter label=podium.task    # empty
```

---

## Deploying with compose

**This needs a published release and there has not been one.** The images
`ghcr.io/alvaroibarguen/podium-server` and `-node` do not exist yet, so the `server` and `node`
services below cannot start. `postgres` and `objectstore` can.

Once there is a release, a host is:

```sh
scp -r deploy/ host:podium/
ssh host
cd podium
podium-server init             # writes master.key and a .env with fresh credentials
docker compose up -d --wait    # postgres, objectstore, server
```

`podium-server init` generates the master key, the Postgres password, the shared token and the
object-store credentials, and writes them into a `0600` `.env`. Every variable it does not set
is documented in [`deploy/.env.example`](../deploy/.env.example) — and a test fails the build if
anything in the tree reads one that file does not mention. Which of them you actually have to
decide is [`deploy/README.md`](../deploy/README.md#what-you-have-to-configure).

For a tailnet deployment, two values are yours to supply:

```sh
podium-server init --transport tailnet --tailnet <your MagicDNS suffix>
# then fill TS_AUTHKEY and PODIUM_NODE_TS_AUTHKEY in .env
docker compose -f docker-compose.tailnet.yml up -d --wait
```

`init` reports which Tailscale prerequisites it can see and names the two it cannot check from
a shell — MagicDNS and HTTPS Certificates both have to be on. Read
[`networking.md`](networking.md) before you run it.

A worker on another machine is one command, once there is a release:

```sh
curl -fsSL https://raw.githubusercontent.com/alvaroibarguen/podium/main/deploy/install-node.sh \
  | sudo PODIUM_SERVER=https://podium.<tailnet>.ts.net \
         PODIUM_ENROLL_TOKEN=$TOKEN \
         TS_AUTHKEY=tskey-auth-... \
         PODIUM_LABELS=linux/amd64 \
         bash
```

It checks Docker and cgroup v2, downloads the binary for the machine's architecture, verifies it
against the release's `checksums.txt`, writes `/etc/podium/node.yaml`, installs the systemd unit
and waits for the node to come online. Until there is a release, build the binary yourself and
follow [`node-setup.md`](node-setup.md) instead.

---

## Where to go next

- [`concepts.md`](concepts.md) — what a task is, and what it is not
- [`task-spec.md`](task-spec.md) — secrets, sidecars, limits, hardening, artifacts
- [`cli.md`](cli.md) — every command, its exit codes and its streams
- [`security.md`](security.md) — the trust model. Read it before putting a node anywhere real
- [`operations.md`](operations.md) — backups, upgrades, draining, metrics
