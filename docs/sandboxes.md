# Sandbox nodes

A sandbox node runs every task in a **cloud sandbox of its own**, made by a provider when the
task is assigned and deleted when it ends. The node itself needs no Docker: it can run on any
machine that reaches the control plane and the provider's API.

```
podium-server ──stream──► podium-node --backend sandbox ──provider API──► sandbox (one per task)
                                         │                                   │
                                         └──── HTTPS, the provider's ────────► podium-sandbox-agent
                                               exposed port + a bearer token     └─ the task
```

Supported providers:

| | boat.dev (`boat`) | Modal (`modal`) |
|---|---|---|
| What a sandbox is | a full VM: Ubuntu 24.04, Docker, systemd | a gVisor container booted from the task's image; `runtime: vm` for a VM |
| `image` tasks run | under Docker inside the VM | as the sandbox itself |
| `sandbox.snapshot` | a boat named snapshot | a Modal filesystem-snapshot image (`im-…`) |
| Sidecars | yes | only with `runtime: vm` |
| Secret files on tmpfs | yes | only with `runtime: vm`; otherwise a 0700 directory removed at the end |
| Resource limits | `resources` picks the machine size; limits apply with systemd | `resources` is the sandbox's CPU and memory |
| Longest task | no limit | 24 h |

## How a task runs

1. The node asks the provider for a sandbox. The lease ID is the idempotency key, so a node
   that dies mid-create finds the same sandbox again.
2. It uploads `podium-sandbox-agent` (embedded in the node) and a one-time bearer token, starts
   the agent, and exposes the agent's port through the provider.
3. It posts the run — spec, resolved secrets, registry logins — to the agent, and streams the
   agent's events back exactly as a Docker node streams its own. Artifacts and the workspace
   tar are pulled and pushed by the node, so **nodes still never touch the object store**.
4. When the task ends, the node deletes the sandbox. A held preview keeps it until released.

The agent runs the task one of two ways:

- **docker** — the task's image, under the sandbox's own Docker engine, with everything a
  Docker node does: sidecars, readiness, hardening, the runner, the gateway. This is how an
  `image` task runs on boat.
- **process** — the task's command runs directly in the sandbox, under `podium-runner`, as a
  dedicated `podium` account with no sudo, no docker group and no supplementary groups
  (`setpriv --init-groups --no-new-privs`). With systemd, `resources` become the limits of a
  scope of its own. This is how a `sandbox.snapshot` task runs, and every task on Modal.

A node that restarts re-adopts its tasks: each sandbox's ID and the agent's address are in the
task's state directory, and the agent keeps a journal of the run that the new daemon replays
from where the control plane says it stopped.

## Setting up a node

**Configure the provider in the UI**, under **Sandboxes**: its key, and settings such as boat's
template and default size or Modal's app and runtime. The key is encrypted under the master
key like a secret, never shown again, and reaches only that provider's sandbox nodes, over
their stream. A node then needs only:

```yaml
# /etc/podium/node.yaml
server: https://podium.<tailnet>.ts.net
transport: tailnet
backend: sandbox
max_tasks: 10                  # no more than the provider lets you run at once
sandbox:
  provider: boat
```

The node takes no work until the provider is configured, picks up a change the moment it is
saved, and stops taking work when the provider is removed. Only admins can set or remove one.

### How many run at once

A provider's sandboxes are one pool, however many nodes drive it, so the cap is the
provider's, not a node's. **Max concurrent sandboxes**, set in the same dialog, caps every node
of the provider together, and the scheduler holds each provider to the lower of that and the
account limit its nodes read from the provider. A held preview and a lost task's sandbox still
waiting to be deleted both count: they are still up.
A task past the cap waits in the queue with `sandbox provider <name> is running as many
sandboxes as it may (N)`.

- **boat** reports the account's limit, so the cap is optional; the dialog shows the limit and
  warns when the cap is set above it.
- **Modal** reports none, so the cap is required: it is what bounds the bill.

`max_tasks` still caps each node on its own.

A node can instead carry the provider's settings in its own file, and then ignores the UI —
useful where the control plane holds no master key:

```yaml
# /etc/podium/node.yaml
server: https://podium.<tailnet>.ts.net
transport: tailnet
backend: sandbox
max_tasks: 10                  # no more than the provider lets you run at once
sandbox:
  provider: boat
  template: ""                 # boat: a named snapshot image tasks start from; empty is boat's base
  boat:
    api_key_file: /etc/podium/boat.key
    default_type: small        # small | default | large | xlarge, when a task asks for no resources
  modal:
    app: podium-sandbox        # created if missing
    runtime: gvisor            # or vm, for sidecars and tmpfs
    token_id: ak-…             # or MODAL_TOKEN_ID / MODAL_TOKEN_SECRET / ~/.modal.toml
    token_secret_file: /etc/podium/modal.secret
```

`podium-node --backend sandbox` (or `PODIUM_NODE_BACKEND=sandbox`) with
`PODIUM_NODE_SANDBOX_PROVIDER` works too. The node labels itself `backend=sandbox` and
`sandbox.provider=<name>` when it enrolls; a task pins a provider with
`labels: [sandbox.provider=boat]`.

The node binary must carry the agent: `make build` and `make dist-node` run
`make sandbox-agent-embed`, and a node started without one says so and exits.

### boat API key

Give the node its own key with only what it uses, and rotate it before it expires:

```sh
boat api-key create podium-node --ttl 90d \
  --actions sandbox.create,sandbox.read,sandbox.update,sandbox.delete-own,exec,file.read,file.write,host,account.read,snapshot.read
```

`snapshot.read` is what creating a sandbox from a named snapshot needs. Never give a node an
admin key: it can mint more keys.

Every boat sandbox is created with `noEnv`, `snapshots: false` and `monitoring: false`: it gets
none of the account's secrets or GitHub access, boat keeps no disk image of it, and boat
stores none of its command output.

### Snapshots

Build a snapshot from a sandbox that was itself created with `noEnv` (`boat new --no-env`), so
no account credential is baked into its disk, then `boat snapshot <id> <name>`. A task names it
with `sandbox: {snapshot: <name>}`.

## What the Nodes screen shows

One row per sandbox node, not per sandbox: its running tasks against `max_tasks`, its labels,
and `sandbox/<provider>` where a Docker node shows its engine version. CPU and memory show as
unmeasured — the capacity is the provider's; the Sandboxes screen shows how many of each
provider's sandboxes are up. The sandbox a task runs in is on the task's page and in
`podium task get`, as soon as the node has made it.

## A node that goes away

A node that restarts finds its sandboxes again in its state directory and carries on. One that
does not come back leaves its sandboxes running: the control plane requeues or loses the task,
and queues the sandbox it recorded for deletion. The next node of that provider to connect
deletes it, and the control plane asks again every minute until one has. A retried task runs in
a new sandbox, never the old one. The provider's TTL remains the last backstop.

## Limits worth knowing

- **boat** — starts are rate-limited per minute, hour and day, and concurrent sandboxes are
  capped per plan (2 on the trial, 100 on the $20 plan). A refused start is retried; keep
  `max_tasks` within the concurrency cap. Registry images are pulled again in every sandbox:
  bake big ones into the template with `docker create --name keep-… <image>`.
- **Modal** — 24 hours is the longest a sandbox lives, so a longer `timeout` is refused.
  Images need `sh`, `date` and `sleep`, which a distroless image does not have.

See [security.md](security.md#a-sandbox-node) for what crosses which boundary.
