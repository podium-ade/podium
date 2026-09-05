# Deploying Podium

> ## ⚠️ Podium has no licence, and cannot be published
>
> [`LICENSE`](../LICENSE) at the repository root still reads:
>
> ```
> TODO: choose Apache-2.0 or AGPL-3.0 before first public push
> ```
>
> Until that is a real licence text, **this code may not be published, redistributed, or
> deployed anywhere it is not yours alone.** Nobody — including you, for anything beyond your
> own machines — has been granted any right to use it, and adding a licence later does not
> retract copies that were already handed out.
>
> The release workflow enforces this: `.github/workflows/release.yml` has a first job that
> fails on a `v*` tag while `LICENSE` contains `TODO`, and every other job depends on it. So
> there is no release, no `ghcr.io` image, and nothing to `curl`.
>
> Choosing the licence is Alvaro's call and has been open since step 01.

---

> ## ⚠️ Nothing in this directory has been run end to end
>
> The compose files pass `docker compose config` and pin their images by digest, but no
> deployment has ever been brought up from them. **None of the four service Dockerfiles in
> `docker/` has ever been built**, so the `ghcr.io` images do not exist. `install-node.sh` passes
> `shellcheck` and `bash -n` and has never run on a real machine.
>
> The *transports* are a different question, and both now work: the dev transport
> ([`../docs/quickstart.md`](../docs/quickstart.md)) and the tailnet transport, with a real
> certificate and a Linux worker running tasks over it. It is the packaging in this directory
> that is untested, not the thing it packages. Build from source and start with the quickstart.

---

## What is in here

| | |
|---|---|
| `docker-compose.yml` | Postgres, MinIO, the control plane, and an optional worker behind the `node` profile |
| `docker-compose.tailnet.yml` | the same on a tailnet: no published ports at all |
| `docker-compose.dev.yml` | Postgres only, for running the binaries by hand |
| `.env.example` | **every** `PODIUM_*` variable, commented. A test fails the build if the code reads one this file does not mention |
| `install-node.sh` | turns a Linux machine into a worker: checks, downloads, verifies, configures, starts, waits |
| `systemd/podium-node.service` | the hardened unit `install-node.sh` installs |
| `docker/*.Dockerfile` | the four service images — `server`, `node`, `agent`, `cli` — base pinned by digest. None has ever been built |
| `tailscale-acl.example.json` | the ACL policy from the networking design |

---

## A host, once there is a release

```sh
scp -r deploy/ host:podium/
ssh host
cd podium

podium-server init             # writes master.key and a .env with fresh credentials
docker compose up -d --wait    # postgres, minio, server
```

`init` generates the AES-256 master key, the Postgres password, the dev token and the
object-store secret, and writes `.env` mode 0600. **Back `master.key` up somewhere that is not
this machine** — there is no recovery path, and losing it loses every secret encrypted under it.

Then a worker, on this machine or another:

```sh
TOKEN=$(podium node enroll-token --label linux/amd64)

# here, with compose:
PODIUM_NODE_ENROLL_TOKEN=$TOKEN docker compose --profile node up -d

# or on another machine:
curl -fsSL https://raw.githubusercontent.com/alvaroibarguen/podium/main/deploy/install-node.sh \
  | sudo PODIUM_SERVER=https://podium.<tailnet>.ts.net \
         PODIUM_ENROLL_TOKEN=$TOKEN \
         TS_AUTHKEY=tskey-auth-... \
         PODIUM_LABELS=linux/amd64 \
         bash
```

## A tailnet host

```sh
podium-server init --transport tailnet --tailnet <your MagicDNS suffix>
# fill TS_AUTHKEY and PODIUM_NODE_TS_AUTHKEY in .env
docker compose -f docker-compose.tailnet.yml up -d --wait
```

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

---

## Things that will surprise you

**`docker compose config` needs the variables to exist.** Several are declared `${VAR:?message}`
so compose fails loudly rather than starting with an empty password. Compose reads `.env` from
the directory it runs in, so after `podium-server init` it just works; before that, supply them
on the command line.

**The Podium services have no healthcheck.** Their images are distroless: no shell, no `curl`,
nothing a compose healthcheck can exec. `/healthz` and `/readyz` are served for an external
prober — point your monitoring at `http://127.0.0.1:8080/readyz`, which is 503 while Postgres or
the object store is unreachable. `docker compose up -d --wait` therefore waits for `postgres` and
`minio` to be healthy and for the rest to be *running*.

**`PODIUM_DEV_LISTEN` is `0.0.0.0:8080` inside the container, and that needs a waiver.** Inside
a container loopback is the container's own, so nothing — not even this compose network — could
reach a server bound to it. The dev transport refuses a non-loopback address by itself, because
one static token is the only credential it has, so the compose file also sets
`PODIUM_DEV_ALLOW_UNSAFE_LISTEN=true` to say that this address is reachable only from inside a
container. The server logs a warning naming that variable every time it starts.

Nothing in the process can tell a container's `0.0.0.0` from a public interface on a host, which
is why the operator declares it rather than the code guessing. **Never set that variable on a
host**, and do not publish 8080 on `0.0.0.0`: the boundary is the published port, which is
`127.0.0.1:${PODIUM_PORT:-8080}`.

**MinIO's API port is deliberately not published.** The server reaches it over the compose
network. Publish 9000 as well only if you want `podium artifact get --via-server=false` from your
laptop.

**The node service mounts the Docker socket, which is root on the host.** A node is a machine you
are willing to let arbitrary containers run on. Read
[`../docs/security.md`](../docs/security.md) before you put one anywhere that matters.

**Pin `PODIUM_IMAGE_TAG`.** It defaults to `latest`, which moves. A control plane and its
workers from different releases can disagree about the wire, and `podium version` will tell you
so after the fact.

**Volumes are not caches.** `miniodata` holds the only copy of a finished task's log once its
chunks have been pruned out of Postgres. `node-state` holds a node key the server issued once
and cannot reissue. `server-state` holds the Tailscale device identity. See
[`../docs/storage.md`](../docs/storage.md).

---

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
| `PODIUM_DEV_TOKEN` | dev transport only |
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
