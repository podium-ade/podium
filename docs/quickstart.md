# Quickstart

A control plane, a worker and a task you can watch run — in about ten commands.

This uses the **`dev` transport**: server and worker on one machine, over loopback, with one
shared token. It is the tested path and the right one for a first look. When you want workers on
other machines, read [`networking.md`](networking.md); the transport is the only thing that
changes.

> **There is no released binary yet.** Podium has never been tagged, so there is nothing to
> `curl` and no image to pull, and `deploy/docker-compose.yml` cannot start `podium-server`.
> Building from source is the only way in today. The [Deploying with compose](#deploying-with-compose)
> section below is what it will look like once there is a release.

---

## Prerequisites

| | |
|---|---|
| Go | 1.26 or newer (`go.mod` sets the floor; the tsnet dependency raised it) |
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

## 2. Start Postgres

```sh
docker compose -f deploy/docker-compose.dev.yml up -d --wait postgres
```

`--wait` matters: a bare port probe races the server's first connection.

> If something already owns `5432` on your machine, set `PODIUM_PG_PORT=55432` here and match
> the DSN below. The same goes for `8080`: `PODIUM_DEV_LISTEN=127.0.0.1:18080`, and pass
> `--server http://127.0.0.1:18080` to the CLI.

The image is `pgvector/pgvector:pg16` — Postgres 16 with the `pgvector` extension available.
Podium's own schema does not use it; the agents' shared memory does. An existing
`podium-pgdata` volume from an earlier release keeps working: it is the same major version.

If you are going to run the conductor (`podium-agent`, see [`agent.md`](agent.md)), it needs its
own database beside Podium's — and the shared memory a third. The compose file creates both, but
Postgres runs init scripts only on an **empty** data directory, so on a volume that already
exists do it by hand, once:

```sh
docker exec podium-dev-postgres createdb -U podium podium_agent
docker exec podium-dev-postgres createdb -U podium podium_memory
docker exec podium-dev-postgres psql -U podium -d podium_memory \
  -c 'create extension if not exists vector'
```

## 3. Make a master key

Secrets are optional — Podium is a task runner without them — but making the key now costs one
command and saves rediscovering it later.

```sh
./bin/podium-server gen-master-key --out /tmp/podium-master.key
```

Mode 0600, refuses to overwrite, and **there is no recovery path**: whatever encrypts your
secrets is only in that file. On a real deployment put it somewhere backed up, off the control
plane.

## 4. Start the control plane

```sh
PODIUM_TRANSPORT=dev \
PODIUM_DEV_TOKEN=devtoken \
PODIUM_DATABASE_URL=postgres://podium:podium@127.0.0.1:5432/podium \
PODIUM_MASTER_KEY_FILE=/tmp/podium-master.key \
  ./bin/podium-server &
```

It migrates the schema on start. `PODIUM_DEV_LISTEN` defaults to `127.0.0.1:8080` and **must**
resolve to loopback — the server refuses to start otherwise, because the shared token is the
only credential there is.

## 5. Tell the CLI where to look

```sh
export PODIUM_SERVER=http://127.0.0.1:8080
export PODIUM_TOKEN=devtoken

./bin/podium version
```

```
podium dev (none)
server dev (none)
```

Two lines, and a warning on the third if the CLI and the control plane are different builds.

## 6. Mint an enrollment token

```sh
TOKEN=$(./bin/podium node enroll-token --label demo)
```

Single use, one hour, and the plaintext is printed once and never stored — the database keeps
only its SHA-256. The token goes on stdout and nothing else does, so `$(...)` works.

## 7. Start a worker

```sh
PODIUM_NODE_SERVER=http://127.0.0.1:8080 \
PODIUM_NODE_TRANSPORT=dev \
PODIUM_NODE_DEV_TOKEN=devtoken \
PODIUM_NODE_ENROLL_TOKEN=$TOKEN \
PODIUM_NODE_DATA_DIR=/tmp/podium-node \
PODIUM_NODE_LABELS=demo \
  ./bin/podium-node &
```

The enrollment token is needed on the **first run only**. After that
`/tmp/podium-node/identity.json` is the node's identity — a credential the server issued once
and cannot reissue.

```sh
./bin/podium nodes
```

```
NAME       ID                              STATUS   LABELS   RUNNING/MAX   HEARTBEAT
my-laptop  node_01m1j889e944prdn29s2x2d6pa  online   demo     0/4           1s ago
```

## 8. Run something

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

## 9. Run something with a database beside it

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

## 10. Open the UI

```sh
open http://127.0.0.1:8080
```

It asks once for the bearer token (`devtoken`) and keeps it in `localStorage`. On a tailnet that
prompt never appears, because Tailscale has already said who you are.

The UI runs the fleet rather than just watching it: submit a task from a form or from the same
YAML `--spec` takes, re-run a finished one, follow live logs with a per-sidecar filter, cancel,
download artifacts, drain / undrain / delete / rekey a node, and set or delete secrets. A task
still `queued` says which of the scheduler's reasons is keeping it there.

---

## Tearing it down

```sh
pkill -f bin/podium-node
pkill -f bin/podium-server
pkill -f bin/podium-agent
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
services below cannot start. `postgres` and `minio` can.

Once there is a release, a host is:

```sh
scp -r deploy/ host:podium/
ssh host
cd podium
podium-server init             # writes master.key and a .env with fresh credentials
docker compose up -d --wait    # postgres, minio, server
```

`podium-server init` generates the master key, generates the Postgres password, the dev token
and the object-store credentials, and writes them into a `0600` `.env`. Every variable it does
not set is documented in [`deploy/.env.example`](../deploy/.env.example) — and a test fails the
build if the code ever reads one that file does not mention.

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
