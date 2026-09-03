# Setting up a `podium-node`

A node is any machine with a Docker engine that runs Podium tasks. It dials the control plane,
advertises its capacity, runs containers, and streams logs back. It never listens for inbound
connections.

> **Read this first — current limitation.** In this pre-alpha the only transport is `dev`, and the
> server refuses to listen on anything but loopback. **A node must therefore run on the same
> machine as `podium-server`.** Multi-machine nodes need the Tailscale transport, which is not
> built yet. Everything below is the single-machine setup.

## Requirements

| | |
|---|---|
| Docker Engine | API **1.43+**, **cgroup v2**. The daemon refuses to start on cgroup v1 — resource limits would be unreliable. |
| Disk | `data_dir` and the image cache are I/O heavy. Put them on fast local storage, never a network share. |
| Network | Outbound to the control plane. **No inbound ports.** |
| Privileges | Access to the Docker socket, which is root-equivalent on that host. |

Check the engine before you start:

```sh
docker info --format 'cgroup v{{.CgroupVersion}}  api {{.ServerVersion}}  {{.Architecture}}'
```

## 1. Mint an enrollment token (on the control plane)

Tokens are single-use, expire in 1h by default, and are shown exactly once — the server stores
only their SHA-256.

```sh
TOKEN=$(podium --server http://127.0.0.1:8080 --token "$PODIUM_DEV_TOKEN" \
  node enroll-token --label linux/arm64 --label browser)
```

`enroll-token` prints the token to **stdout and nothing else**, so `$( )` captures it cleanly; the
expiry note goes to stderr. Repeat `--label` for each label. Labels decide what the node is
eligible for: a task whose spec lists `labels: [browser]` only ever goes to a node advertising
`browser`.

You can also mint one from the web UI under **Nodes → Add a node**, which hands you the whole
command with the token filled in.

## 2. Start the node

Environment-only is a supported deployment — no config file needed:

```sh
PODIUM_NODE_SERVER=http://127.0.0.1:8080 \
PODIUM_NODE_TRANSPORT=dev \
PODIUM_NODE_DEV_TOKEN="$PODIUM_DEV_TOKEN" \
PODIUM_NODE_ENROLL_TOKEN="$TOKEN" \
PODIUM_NODE_DATA_DIR=/var/lib/podium-node \
  podium-node
```

On first run the node exchanges the enrollment token for a permanent identity and writes it to
`$PODIUM_NODE_DATA_DIR/identity.json` with mode `0600`. After that the token is spent and no
longer needed — drop it from the environment.

> **`identity.json` contains `node_key`, a real credential.** The server returns it exactly once
> and keeps only its SHA-256, so there is no recovery path: lose it and you need a new enrollment
> token. Do not put `data_dir` inside a git repository (`.gitignore` guards the obvious spellings)
> and do not copy it between machines — two daemons sharing one identity will fight over the same
> session.

## 3. Verify

```sh
podium --server http://127.0.0.1:8080 --token "$PODIUM_DEV_TOKEN" nodes
```

```
NAME              ID                     STATUS   LABELS           RUNNING/MAX   HEARTBEAT
worker-1          node_01m1j889e944...   online   browser          0/4           0s ago
```

`online` means the node holds an open stream. Then run something on it:

```sh
podium --server http://127.0.0.1:8080 --token "$PODIUM_DEV_TOKEN" \
  run --image alpine:3 -- sh -c 'echo hello from $(hostname)'
```

The node also serves, on `127.0.0.1:9091` by default:

- `/healthz` — the process is up
- `/readyz` — the control-plane stream is connected
- `/metrics` — `podium_node_running_tasks`, `podium_node_free_slots`, `podium_node_stream_connected`

## Configuration reference

`/etc/podium/node.yaml` (or `--config PATH`), overlaid by `PODIUM_NODE_*` environment variables. An
unset or empty variable leaves the file's value alone, so a file and a partial environment compose.

| Env | YAML | Default | Meaning |
|---|---|---|---|
| `PODIUM_NODE_SERVER` | `server` | `http://127.0.0.1:8080` | Control plane base URL |
| `PODIUM_NODE_TRANSPORT` | `transport` | `dev` | `dev` only; `tailnet`/`host` error out |
| `PODIUM_NODE_DEV_TOKEN` | `dev_token` | — | Must equal the server's `PODIUM_DEV_TOKEN` |
| `PODIUM_NODE_ENROLL_TOKEN` | `enroll_token` | — | First run only |
| `PODIUM_NODE_DATA_DIR` | `data_dir` | `/var/lib/podium-node` | Identity + per-task state |
| `PODIUM_NODE_LABELS` | `labels` | — | Comma-separated in env, a list in YAML |
| `PODIUM_NODE_MAX_TASKS` | `max_tasks` | `4` | Concurrency budget. **`0` means the node never gets work.** |
| `PODIUM_NODE_METRICS_LISTEN` | `metrics_listen` | `127.0.0.1:9091` | Health and metrics |
| `PODIUM_NODE_DOCKER_HOST` | `docker_host` | — | Engine endpoint; empty uses the normal Docker resolution |
| `PODIUM_NODE_IMAGE_CACHE_HIGH_WATERMARK` | `image_cache_high_watermark` | `0.80` | Recorded for a later step; nothing reads it yet |

Equivalent config file:

```yaml
# /etc/podium/node.yaml
server: http://127.0.0.1:8080
transport: dev
data_dir: /var/lib/podium-node
labels: [linux/arm64, browser]
max_tasks: 4
dev_token: ""        # or leave to PODIUM_NODE_DEV_TOKEN
enroll_token: ""     # first run only
```

`--log-level` takes `debug`, `info`, `warn` or `error`.

## Operating notes

**One node per Docker engine.** The daemon claims containers by the `podium.task` label across the
whole engine, so two `podium-node` processes on one host will adopt each other's containers. This
is not enforced yet — don't do it.

**Restarts are safe.** SIGTERM leaves running containers alone; on restart the node re-adopts them
via their labels, resumes their log streams where the server last acked, and finishes them. A brief
network loss is also safe: events are buffered (8 MB per task) and replayed, so logs have no gap.

**Cancelling is slow right now.** Without the runner as PID 1, the task command *is* PID 1, and the
kernel discards a default-disposition `SIGTERM` sent to a namespace's init. So a plain
`sleep 60` survives the cancel and dies at the 30s `SIGKILL` (exit 137). A command that traps TERM
exits promptly (143). This is a known consequence of the current build, not a bug.

**Task isolation.** Each task gets its own bridge network `podium-<task_id>` and a workspace volume
mounted at `/workspace`, both removed at teardown. Task containers have internet egress but no
access to the Docker socket or to other tasks.

## Troubleshooting

The daemon tries to fail with an actionable message. The common ones:

| Message | Fix |
|---|---|
| `server is empty` | Set `PODIUM_NODE_SERVER` |
| `dev_token is empty` | Set `PODIUM_NODE_DEV_TOKEN` to the server's `PODIUM_DEV_TOKEN` |
| `transport "tailnet" is not implemented yet` | Use `dev`; multi-machine is not built |
| `data_dir ... is not writable` | Fix ownership or point it somewhere writable |
| `max_tasks is 0` | Set it to 1 or more, or the scheduler will never pick this node |
| `no identity ... and no enrollment token` | Mint a token and pass `PODIUM_NODE_ENROLL_TOKEN` |
| `token already used` | Enrollment tokens are single-use — mint a fresh one |
| cgroup v1 / old API error at startup | Upgrade the Docker engine |

Node shows `online` but never gets tasks: check `max_tasks` is not 0, and that the task's
`labels` are a subset of the node's (`podium nodes` prints them).
