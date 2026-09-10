# Quickstart

A control plane, a worker and a task you can watch run — with Docker and nothing else. No Go
toolchain, no Node, and no Podium binary on the host: `podium-server init` and the `podium`
CLI both run from the published images.

This uses the **`local` transport**: server and worker on one machine, over loopback, with one
shared token. It is the right thing for a first look, and it is loopback-only — it cannot reach
a worker on another machine at all. For that you need the tailnet transport, which is the only
supported way to do it: [`networking.md`](networking.md).

**Everything is configured by one file, `.env`.** `init` writes it with fresh credentials, the
compose file interpolates it, and there is only ever one place to change something.

> **The `ghcr.io/podium-ade/*` tags do not exist until a `v*` tag is pushed.** Until then,
> build the four images yourself and set `PODIUM_IMAGE_REPO` to a registry you can reach —
> every command below is otherwise unchanged. [Building the images](#building-the-images-yourself)
> is at the bottom.

---

## Prerequisites

| | |
|---|---|
| Docker | Engine 24+, **cgroup v2**, and a daemon you can reach. Compose v2 |
| curl | to fetch two files |

That is the whole list. Go, Node and `make` are for [working on Podium](../CONTRIBUTING.md),
not for running it.

Check the two that actually bite:

```sh
docker info --format 'engine {{.ServerVersion}}, cgroup v{{.CgroupVersion}}'
```

cgroup v1 will not work. Memory limits, PID limits and the OOM report all read the unified
hierarchy, and the node refuses to start rather than silently ignoring every limit a task asks
for.

---

## 1. Two files

Podium's control plane is a compose file and a Postgres init script. From an empty directory:

```sh
mkdir podium && cd podium
curl -fsSLO https://raw.githubusercontent.com/podium-ade/podium/main/deploy/docker-compose.yml
curl -fsSL --create-dirs -o postgres/init.sql \
  https://raw.githubusercontent.com/podium-ade/podium/main/deploy/postgres/init.sql
```

The second one creates the conductor's database and the memory service's beside Podium's own.
Postgres runs it only when the data directory is empty, so it costs nothing now and saves
[a recipe](operations.md) later.

## 2. Credentials

```sh
docker run --rm -v "$PWD:/out" --user "$(id -u):$(id -g)" \
  ghcr.io/podium-ade/podium-server:latest init --dir /out
```

```
wrote /out/master.key (mode 0600, key dec994505d6842ef)
wrote /out/.env (mode 0600)

Back up /out/master.key. Losing it loses every secret encrypted under it.
```

That writes two files and never overwrites either: `master.key`, the AES-256 key every stored
secret is encrypted under, and a `0600` `.env` holding a fresh Postgres password, the shared
bearer token, the object-store secret and the conductor's token.

`--user` is not decoration. The image runs as uid 65532, so without it both files land owned by
a user you are not and the next command cannot read them.

**There is no recovery path for `master.key`.** Put a copy somewhere that is not this machine.

Then pin the release, because `latest` moves under you and a control plane and a worker from
different releases can disagree about the wire:

```sh
echo "PODIUM_IMAGE_TAG=v0.1.0" >> .env
```

## 3. The control plane

```sh
docker compose up -d --wait
```

Three containers — Postgres, the object store, the server — and nothing else. Everything past
the control plane is behind a compose profile, so this command needs no other file and no
further decisions.

```sh
curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:8080/readyz    # 200
```

`--wait` matters: a bare port probe races the server's first connection. `/readyz` is `503`
while Postgres or the object store is unreachable, and the server has no compose healthcheck
because the image is distroless — there is no shell for one to exec.

The server migrates its schema on start, so there is no migration step. It publishes on
**loopback of the host only**; inside the container it binds every interface, because loopback
in a container is the container's own and nothing could reach it. The published port is the
boundary. Set `PODIUM_PORT` if something already owns 8080.

## 4. The CLI, without installing it

The `cli` profile is the CLI as a one-shot. `docker compose run` turns the profile on by
itself, so there is nothing to pass:

```sh
docker compose run --rm cli version
docker compose run --rm cli nodes
```

```
podium v0.1.0 (5234ad7)
server v0.1.0 (5234ad7)

NAME   ID   STATUS   LABELS   RUNNING/MAX   HEARTBEAT
```

Two lines from `version`, and a warning on the third if the CLI and the control plane are
different builds. An empty node table is correct — nothing has enrolled yet.

It reaches the server over the compose network, so it needs neither the published port nor the
token on your shell: the compose file wires `PODIUM_SERVER` and the bearer from the same `.env`
the server reads. The CLI never talks to Docker, so it gets no socket and no privileges.

> Installing `podium` on the host is still supported and nicer for day-to-day use — it is one
> static binary from the release archive. Then `set -a; . .env; set +a` configures it from the
> same file, and every `docker compose run --rm cli` below becomes plain `podium`. See
> [`cli.md`](cli.md).

## 5. Enrol a worker

The enrollment token is the one value that cannot be written ahead of time: only a running
control plane can mint one.

```sh
echo "PODIUM_NODE_ENROLL_TOKEN=$(docker compose run --rm cli \
  node enroll-token --label demo)" >> .env

docker compose --profile node up -d
```

Single use, one hour, and the plaintext is printed once and never stored — the database keeps
only its SHA-256. It goes to stdout and nothing else does, so `$(...)` captures just the token.

It is needed on the **first run only**. After that `identity.json` in the node's data directory
is its identity — a credential the server issued once and cannot reissue.

```sh
docker compose run --rm cli nodes
```

```
NAME           ID                                STATUS   LABELS   RUNNING/MAX   HEARTBEAT
1962a708d804   node_01m265842dfz14pn3ypvn3bswt   online   demo     0/4           5s ago
```

This step is a choice, not a formality: it is the machine you are already on volunteering to
run tasks. Skip it and you have a control plane and a UI with nothing to schedule onto, and a
submitted task sits in `queued` saying why.

Two things about this worker are worth knowing before you put one anywhere real:

- **It mounts the host's Docker socket, which is root-equivalent on that host.** Anything that
  can talk to that socket can start a privileged container and own the machine. A node is a
  machine you are willing to let arbitrary containers run on. Read [`security.md`](security.md).
- **Its data directory is a host path**, `/var/lib/podium-node`, at the same absolute path
  inside the container and out. It has to be: the node drives the *host's* daemon, so every
  bind mount it asks for — including the `podium-runner` it puts into every task container as
  PID 1 — is resolved by that daemon against the host filesystem. From a named volume, every
  task dies at creation with `bind source path does not exist`.

## 6. Run something

```sh
docker compose run --rm cli run --image alpine:3 -- \
  sh -c 'for i in 1 2 3; do echo tick $i; sleep 1; done; exit 3'
```

```
→ task task_01m1j88gv0sg8xaxawdh1fqm0z
→ scheduled on node_01m265842dfz14pn3ypvn3bswt
→ running
tick 1
tick 2
tick 3
→ finished exit 3 in 3.2s
```

**The CLI exits with the task's exit code** — 3 here. That is what makes `run` usable as a CI
step. Podium's own commentary goes to stderr with a `→`, so `run ... > out.txt` captures
exactly the task's stdout.

## 7. Run something with a database beside it

A `--spec` is read by the CLI, so under `docker compose run` it has to be reachable *inside*
the container. Mount it:

```sh
curl -fsSL --create-dirs -o specs/postgres-sidecar.yaml \
  https://raw.githubusercontent.com/podium-ade/podium/main/examples/postgres-sidecar.yaml

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

A sidecar is a sibling container on the task's private network, reachable by the name it is
keyed under — `psql -h db` — started before the task and waited for. See
[`task-spec.md`](task-spec.md#sidecars) and the other files in [`examples/`](../examples).

## 8. Open the UI

```sh
open http://127.0.0.1:8080
```

It asks once for the bearer token — `PODIUM_LOCAL_TOKEN` from your `.env` — and keeps it in
`localStorage`, which is per browser origin: `127.0.0.1:8080` and `localhost:8080` each hold
their own copy, so pick one address and stay on it. On a tailnet that prompt never appears,
because Tailscale has already said who you are.

The UI runs the fleet rather than just watching it: submit a task from a form or from the same
YAML `--spec` takes, re-run a finished one, follow live logs with a per-sidecar filter, cancel,
download artifacts, drain / undrain / delete / rekey a node, and set or delete secrets. A task
still `queued` says which of the scheduler's reasons is keeping it there.

---

## The agent layer

The conductor and the agents' shared memory are behind the `conductor` compose profile. It
needs an **agent profile directory**, which is an unrelated thing that unluckily shares the
word — a tree of `profile.yaml`, playbooks and prompts naming what a turn may do, pointed at
by `PODIUM_AGENT_PROFILE_DIR`. It has no default content, and you cannot `curl` a directory,
so this is the one step that wants a clone:

```sh
git clone --depth 1 https://github.com/podium-ade/podium.git /tmp/podium
cp -r /tmp/podium/examples/agent ./agent      # then edit ./agent/playbooks/*.yaml
echo "PODIUM_AGENT_URL=http://agent:8090" >> .env
docker compose --profile conductor up -d
```

A profile directory is a tree of YAML rather than one file, so this is the one step that wants
a clone. `./agent` is mounted read-only: a playbook names the image, the tools and the secrets
its turns get, and it is what the bot hands a turn — not a boundary around the secret store.

Setting `PODIUM_AGENT_URL` is what mounts the conductor's API behind the server's identity
middleware and makes the UI's **Agent** screen appear — one origin, one login. Left unset, the
prefix is not mounted and the screen is hidden, which is a supported way to run.

A turn needs an Anthropic key, which is set in the UI rather than in `.env`, and a playbook
naming a runtime image. That is a longer story: [`agent.md`](agent.md).

> **Not verified this way.** Everything above the agent layer has been run end to end from
> images; the `conductor` compose profile has not.

## Workers on other machines

**The tailnet transport is the only supported way to reach a worker on another machine** — in
development as much as in production. The `local` transport above refuses to bind anything but
loopback, because that one token is all that stands between a caller and the whole API.

```sh
docker run --rm -v "$PWD:/out" --user "$(id -u):$(id -g)" \
  ghcr.io/podium-ade/podium-server:latest \
  init --dir /out --transport tailnet --tailnet <your MagicDNS suffix>
# then fill TS_AUTHKEY and PODIUM_NODE_TS_AUTHKEY in .env
docker compose -f docker-compose.tailnet.yml up -d --wait
```

`init` reports which Tailscale prerequisites it can see and names the two it cannot check from
a shell — MagicDNS and HTTPS Certificates both have to be on. Read
[`networking.md`](networking.md) **before** you run it, and note that a Tailscale auth key and
a Podium enrollment token are different things that everyone confuses.

There is no `cli` profile in the tailnet compose file, and that is not an oversight: the server
listens on port 443 of its own Tailscale device and has no address on the compose network, so a
sibling container cannot reach it. Run the CLI from a device on the tailnet instead, where it
needs no token at all.

Then the worker itself, on the other machine — [`node-setup.md`](node-setup.md) has both paths:
a container like the one in step 5, or `install-node.sh`, which verifies the download against
the release's `checksums.txt` and installs a hardened systemd unit.

---

## Tearing it down

```sh
docker compose --profile node --profile conductor down -v
```

`-v` takes the Postgres, object-store and server volumes with it. Check:

```sh
docker ps -a --filter label=podium.task    # empty
```

**It does not take the worker's data directory**, because that is a host path rather than a
volume. This surprises you exactly once: bring a fresh stack up against an old
`/var/lib/podium-node` and the node loops on `unauthenticated: unknown node key` forever rather
than failing, because `identity.json` is still there so it never reads the new enrollment
token. Starting genuinely from scratch means emptying it too:

```sh
sudo rm -rf /var/lib/podium-node/*        # on Docker Desktop, do it in a container:
# docker run --rm -v /var/lib/podium-node:/d alpine:3 sh -c 'rm -rf /d/*'
```

---

## Building the images yourself

Until a release is tagged there is nothing on `ghcr.io` to pull. The four images are a
statically linked binary each, on a distroless base, so building them is quick — but the
binaries have to exist first, which is what needs Go and Node:

```sh
git clone https://github.com/podium-ade/podium.git && cd podium
make web runner-embed                     # the embedded UI, and the two Linux runners

REG=registry.example.com:5000             # a registry your machines can reach
mkdir -p /tmp/ctx
for b in podium podium-server podium-node podium-agent; do
  CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o "/tmp/ctx/$b" "./cmd/$b"
done
for p in server:podium-server node:podium-node agent:podium-agent cli:podium; do
  docker build -f "deploy/docker/${p%%:*}.Dockerfile" -t "$REG/${p##*:}:dev" /tmp/ctx
  docker push "$REG/${p##*:}:dev"
done
```

Set `GOARCH` to match the machines that will run them, then point the compose file at your
registry:

```sh
printf 'PODIUM_IMAGE_REPO=%s\nPODIUM_IMAGE_TAG=dev\n' "$REG" >> .env
```

A local `:dev` tag is not enough if a worker is on another machine — it cannot see your
daemon's images — which is the whole reason for a registry here.

To work *on* Podium rather than run it, [`CONTRIBUTING.md`](../CONTRIBUTING.md) has the
source-built stack: `make build`, `make stack-up`, host binaries and `docker-compose.dev.yml`.

---

## Where to go next

- [`concepts.md`](concepts.md) — what a task is, and what it is not
- [`task-spec.md`](task-spec.md) — secrets, sidecars, limits, hardening, artifacts
- [`cli.md`](cli.md) — every command, its exit codes and its streams
- [`security.md`](security.md) — the trust model. Read it before putting a node anywhere real
- [`operations.md`](operations.md) — backups, upgrades, draining, metrics
- [`../deploy/README.md`](../deploy/README.md) — every variable you might have to set
