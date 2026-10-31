# Cloud sandboxes

A cloud sandbox runs a task in a sandbox of its own, which a provider makes when the task is
assigned and deletes when it ends. **podium-server runs them itself.** There is nothing to
install: configure the provider on the **Sandboxes** screen and tasks that ask for it run. Nodes
are machines of your own; a cloud provider is not one, and never appears among them.

```
podium-server ──provider API──► sandbox (one per task)
      │                            │
      └── HTTPS, the provider's ───► podium-sandbox-agent
          exposed port + a bearer token  └─ the task
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

1. The server asks the provider for a sandbox. The lease ID is the idempotency key, so a server
   that dies mid-create finds the same sandbox again.
2. It uploads `podium-sandbox-agent` (embedded in podium-server) and a one-time bearer token,
   starts the agent, and exposes the agent's port through the provider.
3. It posts the run — spec, resolved secrets, registry logins — to the agent, and streams the
   agent's events into the task exactly as a node streams its own. Artifacts and the workspace
   tar go through the server, as they do for a node.
4. When the task ends, the server deletes the sandbox. A held preview keeps it until released.

The agent runs the task one of two ways:

- **docker** — the task's image, under the sandbox's own Docker engine, with everything a
  Docker node does: sidecars, readiness, hardening, the runner, the gateway. This is how an
  `image` task runs on boat.
- **process** — the task's command runs directly in the sandbox, under `podium-runner`, as a
  dedicated `podium` account with no sudo, no docker group and no supplementary groups
  (`setpriv --init-groups --no-new-privs`). With systemd, `resources` become the limits of a
  scope of its own. This is how a `sandbox.snapshot` task runs, and every task on Modal.

Inside the server this is the node machinery — the same executor, relay, event buffering,
previews and adoption — run once per configured provider and connected to the server in
memory. That is why placement, capacity and leases work exactly as they do for nodes.

## Setting up a provider

On the **Sandboxes** screen, add the provider: its key, and settings such as boat's template and
default size or Modal's app and runtime. The key is encrypted under the master key like a
secret and never shown again. Only admins can set or remove a provider.

That is the whole setup. Saving takes effect at once; a task that asks for a provider nobody has
set up waits with `sandbox provider <name> is not set up: add it on the Sandboxes screen`.

A task asks for a sandbox with `sandbox:`, `expose.via: sandbox` or the `backend=sandbox`
label, and pins a provider with `sandbox.provider: boat`. It may override the provider's
machine type, template, organization (boat) or runtime and environment (Modal) for its own
sandbox: see [task-spec.md](task-spec.md#sandbox). Agent sessions run on a provider when
**Compute → Backend** or the playbook's `runs_on:` says so: see
[agent.md](agent.md#runs_on-and-sandbox--where-a-turn-runs).

The server binary must carry the agent: `make build` and the release build run
`make sandbox-agent-embed`.

### Where the server keeps its sandboxes

Each provider's sandboxes are recorded under `PODIUM_SANDBOX_DIR`, by default `sandboxes/`
beside the master key (`/var/lib/podium/sandboxes` in the compose deployment). A restarted
server finds its sandboxes there and carries on with their tasks, so keep it on the same
persistent volume as the master key.

### How many run at once

A provider's sandboxes are one pool, so **Max concurrent sandboxes**, set in the same dialog,
caps them all, and the scheduler holds each provider to the lower of that and the account limit
it reads from the provider. A sandbox counts until it is deleted: a held preview, one being torn
down after its task, and a lost task's still waiting to be deleted all count. A task past the
cap waits in the queue with `sandbox provider <name> is running as many sandboxes as it may (N)`.

- **boat** reports the account's limit, so the cap is optional; the dialog shows the limit and
  warns when the cap is set above it.
- **Modal** reports none, so the cap is required: it is what bounds the bill.

Several podium-servers on one database each run every provider, and the cap holds across them.

### boat API key

Give Podium its own key with only what it uses, and rotate it before it expires:

```sh
boat api-key create podium --ttl 90d \
  --actions sandbox.create,sandbox.read,sandbox.update,sandbox.delete-own,exec,file.read,file.write,host,account.read,snapshot.read
```

`snapshot.read` is what creating a sandbox from a named snapshot needs. Never give Podium an
admin key: it can mint more keys.

Every boat sandbox is created with `noEnv`, `snapshots: false` and `monitoring: false`: it gets
none of the account's secrets or GitHub access, boat keeps no disk image of it, and boat
stores none of its command output.

### Snapshots

Build a snapshot from a sandbox that was itself created with `noEnv` (`boat new --no-env`), so
no account credential is baked into its disk, then `boat snapshot <id> <name>`. A task names it
with `sandbox: {snapshot: <name>}`.

### A pool of ready sandboxes

Making a sandbox, waiting for it, starting the agent and exposing its port is most of what a
short task waits for. **Ready sandboxes** (`pool_size`, up to 20) in the provider's dialog keeps
that many waiting, made from **Ready sandbox image** (`pool_image`).

A task whose sandbox would be exactly the pool's shape — the pool's image (or the template, on a
provider that boots none), the default size, no per-task overrides, no exposed ports — takes a
ready one and goes straight to its run; the pool makes a replacement. Anything else is made the
ordinary way. Point the image at the assistant's image and conversations placed on this provider
skip the sandbox's boot.

A ready sandbox costs what any sandbox costs while it waits, and counts against the cap: the
scheduler leaves room for the pool. Changing the pool restarts the provider's runner with the new
settings, and running tasks carry on. The pool is deleted when the server stops, and a server
that died with one deletes it when it starts again.

## Removing a provider

Removing a provider stops new tasks from reaching it. Tasks already running in its sandboxes
finish, and then the server stops driving it. A task that asks for it afterwards waits as
*not set up*.

## Where to see them

The **Sandboxes** screen shows how many of each provider's sandboxes are up. The sandbox a task
runs in is on the task's page and in `podium task get`, as soon as it has been made.

## A server that goes away

A server that restarts finds its sandboxes again under `PODIUM_SANDBOX_DIR` and carries on. One
that does not come back leaves its sandboxes running: the control plane requeues or loses the
task, and queues the sandbox it recorded for deletion. Any server running that provider deletes
it, and the control plane asks again every minute until one has. A retried task runs in a new
sandbox, never the old one. The provider's TTL remains the last backstop.

## Limits worth knowing

- **boat** — starts are rate-limited per minute, hour and day, and concurrent sandboxes are
  capped per plan (2 on the trial, 100 on the $20 plan). A refused start is retried; keep
  **Max concurrent sandboxes** within the concurrency cap. Registry images are pulled again in every sandbox:
  bake big ones into the template with `docker create --name keep-… <image>`.
- **Modal** — 24 hours is the longest a sandbox lives, so a longer `timeout` is refused.
  Images need `sh`, `date` and `sleep`, which a distroless image does not have.

See [security.md](security.md#cloud-sandboxes) for what crosses which boundary.
