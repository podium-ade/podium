# Quickstart

A control plane, a worker and a task you can watch run — with Docker and nothing else. No Go
toolchain, no Node, and no Podium binary on the host: `podium-server init` and the `podium`
CLI both run from the published images.

This uses the **`local` transport**: server and worker on one machine, over loopback, with one
shared token. It is the right thing for a first look, and it is loopback-only. A worker on
another machine is either the tailnet transport, or host network if you already have WireGuard
or a corporate VPN: [`networking.md`](networking.md).

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

## 1. One file

Save [`deploy/docker-compose.yml`](../deploy/docker-compose.yml) into an empty directory. That
is the only file this deployment has.

```sh
mkdir podium && cd podium
curl -fsSLO https://raw.githubusercontent.com/podium-ade/podium/main/deploy/docker-compose.yml
```

Nothing else needs to be beside it. The Postgres bootstrap script that creates the conductor's
database and the memory service's is inline at the bottom of that file; the conductor's profile
directory ships inside its image; and the master key is generated on first `up` by a one-shot
`init` service — meaning a container that runs a single command and exits rather than staying
up.

That key is worth one decision before you start. By default it is generated *inside* a Docker
volume, which means copying it out is a command you have to remember and `docker compose
down -v` destroys it. Point `PODIUM_STATE_DIR` at a path instead and it is an ordinary file you
can see:

```sh
echo 'PODIUM_STATE_DIR=./state' >> .env      # then master.key is ./state/master.key
```

Compose reads a bare name as a Docker volume and a path as a bind mount, so that one variable
switches between them. Do it **before the first `up`** — afterwards you are copying a key
between two places rather than choosing where it goes.

## 2. Up

```sh
docker compose up -d --wait
```

```
 Container podium-postgres-1     Healthy
 Container podium-objectstore-1  Healthy
 Container podium-init-1         Exited
 Container podium-server-1       Healthy
 Container podium-agent-1        Healthy
```

Six containers: Postgres, the agents' shared memory, the object store, the control plane, the
conductor, and `init` — which is a **one-shot**, meaning it runs a single command and exits
rather than staying up. It generated the master key, and the server waited for it to finish
before starting. `--wait` matters — a bare port probe
races the server's first connection.

Hindsight is the exception, and `--wait` will say so: it exits without an LLM key of its own
and keeps restarting until you give it one. Everything else is up and working meanwhile — see
[the agent layer](#the-agent-layer).

```sh
curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:8080/readyz    # 200
```

`/readyz` is `503` while Postgres or the object store is unreachable. The server has no compose
healthcheck because the image is distroless — there is no shell for one to exec — so `--wait`
treats it as ready once it is running, and `/readyz` is the real answer.

The server migrates its schema on start, so there is no migration step. It publishes on
**loopback of the host only**; inside the container it binds every interface, because loopback
in a container is the container's own and nothing could reach it. The published port is the
boundary. Set `PODIUM_PORT` if something already owns 8080.

### About those credentials

Nothing above asked you for a password, because the compose file ships defaults. That is what
makes one command possible, and it has a precise limit:

| | reachable from | if you change nothing |
|---|---|---|
| `PODIUM_PG_PASSWORD` | the compose network | no published port goes near Postgres |
| `PODIUM_S3_SECRET_KEY` | the compose network | no published port goes near the object store |
| `PODIUM_AGENT_TOKEN` | the compose network | the conductor is not published either |
| `PODIUM_LOCAL_TOKEN` | **`127.0.0.1:8080`** | it is the only thing between a caller and the whole API |

Only the last one is exposed at all, and only to whoever is on that machine. For anything you
would miss, write a `.env` beside the compose file and override them:

```sh
printf 'PODIUM_LOCAL_TOKEN=%s\nPODIUM_PG_PASSWORD=%s\nPODIUM_S3_SECRET_KEY=%s\n' \
  "$(openssl rand -hex 32)" "$(openssl rand -hex 16)" "$(openssl rand -hex 16)" > .env
docker compose up -d --wait
```

Do it **before the first `up`**: `PODIUM_PG_PASSWORD` is baked into the Postgres volume when it
is initialised, so changing it afterwards means `docker compose down -v` or an `ALTER ROLE`.

Then pin the release, because `latest` moves under you and a control plane and a worker from
different releases can disagree about the wire:

```sh
echo "PODIUM_IMAGE_TAG=v0.1.0" >> .env
```

## 3. The CLI, without installing it

The `cli` profile is the CLI, and it is a **one-shot** rather than a daemon: the container
runs a single command and exits, instead of staying up like the other five. `docker compose
run` turns the profile on by itself, so there is nothing to pass:

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

Each invocation is a **new container**: `run` creates one, it executes that command, exits
with the command's own exit code, and `--rm` deletes it. Nothing persists in between, which
explains three things that are otherwise puzzling — `run` rather than `exec` (there is no
running container to exec into), a `--spec` having to be mounted (step 6), and why this is
usable as a CI step at all.

It reaches the server over the compose network, so it needs neither the published port nor the
token on your shell: the compose file wires `PODIUM_SERVER` and the bearer from the same `.env`
the server reads. The CLI never talks to Docker, so it gets no socket and no privileges.

> Installing `podium` on the host is still supported and nicer for day-to-day use — it is one
> static binary from the release archive. Then `set -a; . .env; set +a` configures it from the
> same file, and every `docker compose run --rm cli` below becomes plain `podium`. See
> [`cli.md`](cli.md).

## 4. Enrol a worker

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

## 5. Run something

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

## 6. Run something with a database beside it

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

## 7. Open the UI

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

The conductor came up in step 2, and the UI's **Agent** screen is already its front end —
reverse-proxied behind the server's identity middleware, so one origin and one login. It is an
ordinary API client of `podium-server`: its own database, its own token, and it never touches
Docker.

The playbook a turn runs is the worked example baked into the agent image at
`/etc/podium/agent`, which is where `PODIUM_AGENT_PROFILE_DIR` points. To run your own bot,
mount a profile directory over it — read-only, because a playbook names the image, the tools
and the secrets its turns get:

```yaml
    volumes:
      - ./my-profile:/etc/podium/agent:ro
```

Two things the conductor does not have out of the box, both credentials:

**A model key.** A turn needs an Anthropic key, set on the UI's Agent screen rather than in
`.env` — that way it lands in the encrypted secret store instead of in `docker inspect`.

**Shared memory.** Hindsight came up in step 2 too — or rather it tried. It wants an LLM key of
its own for fact extraction and exits at boot without one, so until you set it that is the one
container in `docker compose ps` that keeps restarting. Nothing else depends on it, and the
conductor runs with no memory quite happily: briefs carry no memory block and nothing is
retained. One line:

```sh
echo 'PODIUM_MEMORY_LLM_API_KEY=...' >> .env
docker compose up -d
```

**The key does not have to be Anthropic's.** Hindsight puts LiteLLM underneath and takes
[any of ~25 providers](https://hindsight.vectorize.io/developer/models): OpenAI, Gemini, Groq,
Bedrock, Vertex AI, DeepSeek, a gateway, an existing ChatGPT or Claude subscription, or a local
`ollama` / `lmstudio` / `llamacpp` — which keeps fact extraction off the network altogether.
Set the provider and a matching model beside the key:

```sh
printf 'PODIUM_MEMORY_LLM_PROVIDER=ollama\nPODIUM_MEMORY_LLM_MODEL=llama3.1\n' >> .env
```

The defaults are `anthropic` and `claude-opus-5`. Its
[configuration reference](https://hindsight.vectorize.io/developer/configuration) is the
authority on which variables each provider wants.

Read this before you turn it on, because it is the one place the defaults are deliberately
inconvenient: **Hindsight has no authentication beyond `PODIUM_AGENT_MEMORY_API_KEY`**, which
defaults to `podium`, so its port is published on loopback only. That is fine for the
conductor, which reaches it by service name over the compose network — but an agent *turn*
runs in a task container that reaches the host through the bridge gateway
(`PODIUM_AGENT_MEMORY_TASK_URL`), and loopback is not reachable from there. Widening it means
setting `PODIUM_MEMORY_BIND`, and setting a real key at the same moment. See
[`agent.md`](agent.md) and [`security.md`](security.md).

> **The shared memory path is not covered.** Bringing Hindsight up needs a paid LLM key, so
> nothing here exercises it. Everything above it is.

## Workers on other machines

**The tailnet transport is how Podium names the caller on another machine.** The `local`
transport used above refuses to bind anything but loopback, because that one token is all that
stands between a caller and the whole API. If the machines already share a network Podium did
not create — WireGuard, a corporate VPN, a LAN — that is the host-network path in
[`networking.md`](networking.md#host-network-bring-your-own-routing), still the local token,
bound to this machine's interfaces.

That is a different compose file, [`docker-compose.tailnet.yml`](../deploy/docker-compose.tailnet.yml),
and the next section walks one through end to end. Two things about it are worth knowing before
you get there:

- **It fails closed on every credential**, unlike the file above. Six variables have no
  default and `up` names any you leave out. `podium-server init --transport tailnet --tailnet
  <suffix>` writes a `.env` with the ones it can generate and reports which Tailscale
  prerequisites it can see.
- **There is no `cli` profile in it**, and that is not an oversight: the server listens on port
  443 of its own Tailscale device and has no address on the compose network, so a sibling
  container cannot reach it. Run the CLI from any device on the tailnet instead, where it needs
  no token at all.

For the worker at the other end, [`node-setup.md`](node-setup.md) has both paths: a container
like the one in step 4, or `install-node.sh`, which verifies the download against the release's
`checksums.txt` and installs a hardened systemd unit.

---

## A production stack, concretely

A worked example rather than a checklist: one Linux host as the control plane, workers on other
machines, Tailscale between them. Everything below is a real command; substitute your own names
where they are obviously yours.

**Why the tailnet transport.** The `local` transport used above refuses to bind anything but
loopback, because its one shared token is all that stands between a caller and the whole API.
So it cannot reach a worker on another machine — not as a matter of preference, as a matter of
what it will do. On a tailnet there is no token at all: Tailscale's `WhoIs` names every caller,
the server serves HTTPS on its own device, and nothing is published to the internet.

### 1. Tailscale first — Podium cannot do any of this for you

In the admin console, four things:

| | |
|---|---|
| **DNS → MagicDNS** | on. Names like `podium.tail0a1b2c.ts.net` do not exist without it |
| **DNS → HTTPS Certificates** | on. The server fetches a real certificate for its own name and **refuses to start** without this, with a message naming it |
| **Access Controls** | merge [`tailscale-acl.example.json`](../deploy/tailscale-acl.example.json) into your policy |
| **Settings → Keys** | two auth keys, both **Reusable** and **Pre-approved** — one tagged `tag:podium-server`, one `tag:podium-node` |

A Tailscale auth key and a Podium enrollment token are different things and everyone confuses
them. A new worker needs both. Get your suffix and this host's tailnet address:

```sh
tailscale status --json | jq -r '.MagicDNSSuffix, .Self.TailscaleIPs[0]'
```

```
tail0a1b2c
100.101.102.103
```

### 2. The control plane host

Docker Engine 24+ and cgroup v2. One file:

```sh
sudo install -d -m 0750 /srv/podium && cd /srv/podium
sudo curl -fsSLO https://raw.githubusercontent.com/podium-ade/podium/v0.1.0/deploy/docker-compose.tailnet.yml
```

Fetch it at the **tag**, not `main`: the compose file and the images it names should come from
the same release.

### 3. The `.env`, in full

This file is the whole configuration. `umask 077` first — it holds every credential:

```sh
umask 077
cat > /srv/podium/.env <<'EOF'
# --- pin the release. `latest` moves, and a control plane and a worker from different
# --- releases can disagree about the wire.
PODIUM_IMAGE_TAG=v0.1.0

# --- Tailscale. Both keys reusable + pre-approved; read on first run only.
PODIUM_TAILNET=tail0a1b2c
TS_AUTHKEY=tskey-auth-REPLACE-ME
PODIUM_NODE_TS_AUTHKEY=tskey-auth-REPLACE-ME

# --- credentials. This file fails closed on all four: `up` names any you leave out.
PODIUM_PG_PASSWORD=REPLACE-ME
PODIUM_S3_SECRET_KEY=REPLACE-ME
PODIUM_AGENT_TOKEN=REPLACE-ME
PODIUM_AGENT_MEMORY_API_KEY=REPLACE-ME

# --- shared memory. The key can be from any of ~25 providers; a local ollama keeps fact
# --- extraction off the network entirely.
PODIUM_MEMORY_LLM_API_KEY=REPLACE-ME

# --- memory placement: workers must reach 8888, and they are not on this machine. Both of
# --- these are THIS HOST'S TAILNET ADDRESS, not loopback and not host.docker.internal.
PODIUM_MEMORY_BIND=100.101.102.103
PODIUM_AGENT_MEMORY_TASK_URL=http://100.101.102.103:8888
EOF
```

Generate the four credentials in place rather than inventing them:

```sh
for v in PODIUM_PG_PASSWORD PODIUM_S3_SECRET_KEY PODIUM_AGENT_TOKEN PODIUM_AGENT_MEMORY_API_KEY; do
  sudo sed -i "s|^$v=REPLACE-ME$|$v=$(openssl rand -hex 32)|" /srv/podium/.env
done
```

**`PODIUM_PG_PASSWORD` has to be right before the first `up`**, because Postgres bakes it into
the data volume when it initialises. Changing it afterwards means `down -v` or an `ALTER ROLE`.

The two memory lines are the ones people get wrong. `PODIUM_MEMORY_BIND` is which interface
8888 is published on, and it cannot be loopback here because the containers that need it are on
other machines. `PODIUM_AGENT_MEMORY_TASK_URL` is that same service **as a task container sees
it** — a worker elsewhere on the tailnet, so this host's tailnet address. Pair it with
`tag:podium-node -> 8888` in the ACL, and note that `PODIUM_AGENT_MEMORY_API_KEY` is the only
thing guarding it.

### 4. Up

```sh
cd /srv/podium
docker compose -f docker-compose.tailnet.yml up -d --wait
```

Then confirm it from another device on the tailnet — which also proves the certificate, the
ACL and MagicDNS in one go:

```sh
curl -fsS https://podium.tail0a1b2c.ts.net/readyz     # ok
podium --server https://podium.tail0a1b2c.ts.net nodes   # no --token: WhoIs names you
```

### 5. Back up the master key, now, before you store a secret

There is **no recovery path** — losing it loses every secret encrypted under it, and a database
backup without it is a backup of unreadable ciphertext.

On a host you intend to keep, put it on the host filesystem from the start rather than inside a
volume. Add this to the `.env` above **before the first `up`**:

```ini
PODIUM_STATE_DIR=/srv/podium/state
```

`master.key` is then `/srv/podium/state/master.key`, mode `0600` and owned by uid 65532 on a
Linux engine, so reading it takes `sudo`. It also survives `docker compose down -v`, which is
the failure this avoids. Under this file the same directory holds the tsnet device identity
too, so it is two unrecoverable credentials and one backup job.

If you left it in the volume, copy it out:

```sh
docker compose -f docker-compose.tailnet.yml cp \
  server:/var/lib/podium/master.key /srv/podium/master.key
```

Then get that file **off this host**, and not into the same place as the database dumps.

### 6. Workers, on other machines

On each worker, mint a single-use token from the control plane first:

```sh
# on any device on the tailnet
podium --server https://podium.tail0a1b2c.ts.net node enroll-token --label linux/amd64
```

Then on the worker itself:

```sh
curl -fsSL https://raw.githubusercontent.com/podium-ade/podium/v0.1.0/deploy/install-node.sh \
  | sudo PODIUM_SERVER=https://podium.tail0a1b2c.ts.net \
         PODIUM_ENROLL_TOKEN=<the token> \
         TS_AUTHKEY=tskey-auth-...          \
         PODIUM_LABELS=linux/amd64          \
         PODIUM_VERSION=v0.1.0              \
         bash
```

That verifies the download against the release's `checksums.txt` before unpacking, writes
`/etc/podium/node.yaml` mode 0600, installs a hardened systemd unit from the same archive, and
waits for the node's own `/readyz`. A worker is **a machine you are willing to let arbitrary
containers run on** — it drives its own Docker daemon as root-equivalent. Read
[`security.md`](security.md) before choosing which machines those are.

### 7. What to watch

| | |
|---|---|
| `https://podium.<suffix>.ts.net/readyz` | `503` while Postgres or the object store is unreachable. This is the liveness signal, because the image is distroless and has no compose healthcheck |
| `/metrics` on the server | queue depth, task outcomes, and `podium_agent_memory_extraction_failed`, which is non-zero when the memory key is wrong — the failure that otherwise reports success |
| `127.0.0.1:9091/readyz` on each worker | the node's own, and what `install-node.sh` polls |
| `docker compose ps` | `hindsight` restarting means its LLM key is missing or rejected |

### 8. Back up, in this order of consequence

| | why |
|---|---|
| `master.key` | no recovery path. Everything else is replaceable; this is not |
| the `pgdata` volume | tasks, nodes, secrets, audit. `pg_dump` or a volume snapshot |
| the `objectstore-data` volume | the only copy of a finished task's artifacts and rolled-up logs once the hot rows are pruned. Not a cache |
| the `server-state` volume | holds the tsnet device identity as well as the key. Losing it means the server registers a new device and its name drifts to `podium-1` |

A worker's data directory is deliberately **not** on that list: `identity.json` cannot be
reissued, so a lost worker re-enrols rather than restores.

### 9. Upgrading

Change `PODIUM_IMAGE_TAG`, then pull and recreate. The server migrates its schema on start:

```sh
sudo sed -i 's/^PODIUM_IMAGE_TAG=.*/PODIUM_IMAGE_TAG=v0.2.0/' /srv/podium/.env
docker compose -f docker-compose.tailnet.yml pull
docker compose -f docker-compose.tailnet.yml up -d --wait
```

Workers are separate, and drain rather than restart — a node with `--exit-on-drain` exits 0
when its last task finishes, and systemd brings it back on the new binary:

```sh
podium node drain <node-id>     # wait for `podium nodes` to show 0 running
```

> **What is proven here and what is not.** The tailnet transport itself is exercised — a
> server device and workers on other machines, reconnecting across restarts. The compose file
> above is not: bringing it up needs a real Tailscale auth key and a tailnet to join, which no
> test can supply, so treat it as the recipe and `docker-compose.yml` as the proven one.
>
> The `agent` service is a known gap rather than an untested one. Under this transport the
> server listens only on its own Tailscale device and has no address on the compose network, so
> a sibling container cannot reach it. Run the conductor on the host, or on the machine's own
> `tailscaled`.

---

## Tearing it down

**Back the master key up first, if you have stored any secret**, because `-v` destroys it and
there is no recovery path:

```sh
docker compose cp server:/var/lib/podium/master.key ./master.key
```

Unless you set `PODIUM_STATE_DIR` to a path back in step 1 — in which case the key is already a
file on the host and `-v` cannot reach it. That is the whole reason to do it.

```sh
docker compose --profile node --profile cli down -v
```

`-v` takes the Postgres, object-store and server volumes with it — including the master key the
`init` service generated. Check:

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
ARCH=arm64                                # match the machines that will run these
mkdir -p "/tmp/ctx/linux/$ARCH"
for b in podium podium-server podium-node podium-agent; do
  CGO_ENABLED=0 GOOS=linux GOARCH="$ARCH" go build -o "/tmp/ctx/linux/$ARCH/$b" "./cmd/$b"
done
cp -r examples/agent /tmp/ctx/examples/agent            # the conductor's default profile

for p in server:podium-server node:podium-node agent:podium-agent cli:podium; do
  docker build --platform "linux/$ARCH" \
    -f "deploy/docker/${p%%:*}.Dockerfile" -t "$REG/${p##*:}:dev" /tmp/ctx
  docker push "$REG/${p##*:}:dev"
done
```

**The `linux/$ARCH/` directory is not decoration.** The Dockerfiles copy
`$TARGETPLATFORM/<binary>`, because a release builds one multi-architecture image and cannot
put two architectures' binaries at the same path. A flat context fails with
`"/podium-server": not found`. `--platform` is what sets `$TARGETPLATFORM`, so it is required
too. `examples/agent` sits at the context root instead, unprefixed, because the conductor's
profile is the same for every architecture.

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
