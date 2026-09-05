---
name: run-dev-stack
description: Bring up a Podium development stack from nothing — Postgres, podium-server, a worker, and optionally the conductor — and drive a task through it. Use when asked to run, start, or smoke-test Podium locally, to reproduce a bug against a real stack, or to attach a remote worker. Covers both the loopback-only dev transport and the tailnet transport, which is the only supported way to reach a worker on another machine. To put new code on a stack that is already up, see update-live-stack.
---

# Running a Podium development stack

Podium is a control plane (`podium-server`), one or more workers (`podium-node`), and
optionally a conductor (`podium-agent`) with a memory service. In development you run the
binaries natively and only the dependencies in Docker, so a rebuild is seconds rather than an
image build.

**Decide one thing first: does this stack need a worker on another machine?**

| | Transport | Use when |
|---|---|---|
| Local only | `dev` | Everything on one box. Simplest. |
| Remote worker | `tailnet` | A worker on another machine. **The only supported way.** |

The `dev` transport **refuses to listen on anything but loopback**, so a remote node cannot
reach it. That is a deliberate check, not a bug: the dev transport authenticates with one shared
static token, and binding that to a real interface publishes the whole API. Do not work around
it with a TCP relay.

---

## Prerequisites

Go 1.26+, Docker with cgroup v2, Node 22+, pnpm.

```sh
make build                                   # bin/podium{,-server,-node,-agent}
```

Two compose services sit behind profiles, because most work needs neither:

| Profile | What | Add it when |
|---|---|---|
| `artifacts` | RustFS, an S3 object store on :9000 | you care about task logs and artifacts surviving the task |
| `memory` | Hindsight, a ~6 GB image | you are working on agent memory; needs `PODIUM_MEMORY_LLM_API_KEY` |

Integration tests do **not** use that compose file — they boot their own Postgres via
testcontainers, so they never collide with your dev database.

---

## The one command that writes the configuration

```sh
./bin/podium-server init --dir deploy                    # dev transport
./bin/podium-server init --dir deploy --transport tailnet --tailnet <suffix>
```

It writes `deploy/master.key` and `deploy/.env`, both 0600, and never overwrites either. The
`.env` carries freshly generated credentials — the Postgres password, the object store's keys,
and every token the stack needs: `PODIUM_DEV_TOKEN`, `PODIUM_AGENT_TOKEN` and
**`PODIUM_NODE_ENROLL_TOKEN`**. There is no separate `podium node enroll-token` step for a
fresh stack; the node in the launcher below picks that value up on its own.

Everything after this reads that one file, so anything you need to differ — a port, a profile
directory, a state directory — is a line you add to it rather than a variable you remember to
export.

> **Back up `master.key`.** It is the key every stored secret is encrypted under, and there is
> no recovery path. `secrets.key_id` records which key a secret was written with — the first
> eight bytes of its SHA-256 — so a mismatch shows up as secrets that list fine and fail to
> resolve into a task.

---

## Local only — the `dev` transport

```sh
docker compose -f deploy/docker-compose.dev.yml up -d --wait postgres
make stack-up
```

`make stack-up` runs `deploy/run-host.sh`, which reads `deploy/.env`, derives what the compose
files derive in YAML, starts each service, and waits for it to answer its health check.
`make stack-down` stops them and waits for each to exit; `make stack-status` says what is
running and what it answers. Name services to act on a subset: `make stack-up S="server node"`
for a stack with no conductor.

Then talk to it:

```sh
export PODIUM_SERVER=http://127.0.0.1:8080
export PODIUM_TOKEN=$(sed -n 's/^PODIUM_DEV_TOKEN=//p' deploy/.env)
./bin/podium nodes
./bin/podium run --image alpine:3 -- echo hello
```

For UI work run `pnpm dev` in `web/` instead of opening the embedded build: Vite hot-reloads and
its proxy injects the token, so the UI never prompts for one.

---

## With a remote worker — the `tailnet` transport

**Only do this if a remote machine is actually available.** It needs admin-console setup, adds
devices to your tailnet, and buys nothing on a single box.

Where it is worth it: the remote worker is usually **real Linux**, and Linux takes different code
paths from macOS — sidecar readiness dials the container directly instead of `exec`ing inside it,
cgroup v2 limits are real, and file permissions behave differently. Bugs hide on macOS that this
finds. It is also the transport production uses, so developing on it means the path gets
exercised before it ships.

### One-time setup, in the Tailscale admin console

`init --transport tailnet` prints this as a checklist and marks off what it can see from here.
Three things it cannot check for you:

1. **MagicDNS** and **HTTPS Certificates** both enabled, under DNS. The server refuses to start
   without them and says which is missing.
2. **Tags defined** in Access Controls — paste `deploy/tailscale-acl.example.json`, merged with
   your existing policy. If your policy has a blanket allow-all rule, that file's `tests` block
   will fail, correctly: it asserts an outbound-only shape your open rule contradicts. Either
   narrow the rule deliberately or drop the `tests` block, and know that dropping it means the
   guarantee is enforced by the code rather than the network.
3. **Two auth keys**, both **Reusable** and **Pre-approved**, Ephemeral **off** — one tagged
   `tag:podium-server`, one tagged `tag:podium-node`. Tags must exist before a key can carry one.
   The node key is reusable on purpose: every worker uses the same one.

Put both keys in the `.env` `init` wrote, as `TS_AUTHKEY` and `PODIUM_NODE_TS_AUTHKEY`. That is
the only hand-editing this needs.

### The stack

```sh
docker compose -f deploy/docker-compose.dev.yml up -d --wait postgres
make stack-up
```

The server joins as its own device and serves HTTPS on `https://<hostname>.<tailnet>.ts.net`.
There is **no token**: identity comes from Tailscale's WhoIs, so
`./bin/podium --server https://… nodes` just works and the UI does not prompt.

> **The tsnet state directory must persist.** It holds the device's node key. Lose it and the
> process registers as a brand new device, the name drifts to `podium-1`, `podium-2`, and the
> admin console fills with ghosts. It defaults to `.podium/tsnet` under the checkout; set
> `PODIUM_TS_STATE_DIR` in the `.env` to put it somewhere you keep. The same goes for
> `PODIUM_NODE_DATA_DIR`, which holds the node's identity.

### The remote worker

The node is a Linux binary, so cross-compile and copy it. The runner is embedded in it, so
there is nothing else to ship.

```sh
make dist-node GOOS=linux GOARCH=amd64          # check the target's arch with `uname -m`
scp bin/podium-node-linux-amd64 user@host:~/podium/podium-node
```

Give it its own `.env` beside the binary — the same `PODIUM_NODE_*` values from yours, plus
`PODIUM_NODE_SERVER` and `PODIUM_NODE_ENROLL_TOKEN` — and a two-line script that sources it and
execs the binary, so a restart is one command and the environment is not something you retype.
Label your workers (`PODIUM_NODE_LABELS=linux,amd64`) and a task picks one with a matching
`labels:` in its spec. Labels are sent at enrollment and stored server-side, so swapping the
binary later cannot lose them.

Replacing that binary later is [update-live-stack](../update-live-stack/SKILL.md), not this.

---

## The conductor, when you need it

Only if you are working on the conductor, Slack, Linear or chat. `deploy/postgres/init.sql`
creates the `podium_agent` database on a fresh volume, so there is nothing to create by hand.

It is the third service `make stack-up` starts, and `deploy/.env` already has its token. What it
still needs is a profile directory — `PODIUM_AGENT_PROFILE_DIR`, default `examples/agent` — and
a model credential, set in the UI under Agent → Settings:

- **Anthropic** — an API key, for skills on the `claude` backend.
- **xAI** — an API key, *or* a subscription sign-in (SuperGrok, X Premium+) if
  `PODIUM_AGENT_XAI_OAUTH_CLIENT_ID` is set. Both end up as the same bearer against the same
  endpoint; the sign-in additionally stores a refresh token that no turn is ever handed.

A skill names its backend with `agent:`, and optionally `model:` and `effort:`. Unset means the
profile's, and the profile's default is `claude`.

**The profile directory is read once, at boot.** The periodic reload rebuilds from that snapshot
plus the skills the database holds, so editing a file under `PODIUM_AGENT_PROFILE_DIR` reaches a
running conductor never — restart it with `make stack-up S=agent`. Skills created in the web UI
live in the database and do reach the next turn.

Agent turns run the runtime images, and those are **architecture-specific**: an image built on an
arm64 Mac cannot run on an amd64 worker, and a locally built tag is invisible to any other
machine. Building and shipping them is [update-live-stack](../update-live-stack/SKILL.md).

---

## Things that will cost you an hour

- **The dev token is stored per browser origin.** It lives in `localStorage` under
  `podium.devToken`, so `127.0.0.1:8080` and `localhost:8080` are different origins with
  different copies. Pick one address and stay on it. The tailnet transport removes the token
  entirely.
- **A secret is only readable under the master key it was written with.** Point the server at a
  different `PODIUM_MASTER_KEY_FILE` and listing still works, because that reads metadata only,
  but resolving a secret into a task fails. `podium secret set` re-encrypts under the current key.
- **Task history belongs to the database, not the server.** A fresh `PODIUM_DATABASE_URL` gives
  an empty UI; the old database is untouched and switching back restores it.
- **Never run a second `podium-node` against the same Docker engine.** A node claims *every*
  container labelled `podium.task` on its engine, so two of them tear down each other's work —
  including a live stack's tasks while you run the test suite. Different machines are fine.
- **The first HTTPS request after starting a tailnet server can take 30 s** while the certificate
  is issued, and logs as `TLS handshake error … i/o timeout`. It is not a failure; retry. A
  `curl: (28)` from the launcher's first health probe is the same thing.
- **Ports.** If 5432 or 8080 are taken, put `PODIUM_PG_PORT` and `PODIUM_DEV_LISTEN` in the
  `.env` — the launcher derives the database URL and the server URL from them, so nothing else
  has to change.
- **`ps aux | grep podium` can match nothing while the stack is plainly up.** Use `pgrep -fl`.

## Tearing down

```sh
make stack-down                                              # waits for each to exit
docker compose -f deploy/docker-compose.dev.yml down -v      # destroys every database
```

`stack-down` matches on the full binary path, so another checkout's stack is left alone. Never
`pkill -f podium` next to a live stack.
