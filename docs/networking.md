# Networking: the tailnet transport

Podium's control plane has no public address, no login page and no API token. The server joins
your Tailscale network as a device, serves HTTPS on its MagicDNS name, and asks Tailscale who is
on the other end of every connection. Workers dial out and never listen.

This document covers the two keys you need, the ACL, the two ways to join the tailnet, and what
to do when it does not work.

## This is not only the production option

**The tailnet transport is the only supported way to reach a worker on another machine — in
development as much as in production.** There is no "use the local transport across the LAN while
I try this out" path. `local.CheckListen` refuses any listen address that is not unambiguously
loopback, and its one waiver, `PODIUM_LOCAL_ALLOW_UNSAFE_LISTEN`, is for **a container**, where
loopback is the container's own and the published port is the boundary.

Set that waiver on a host and you publish the whole API — task submission, which is code as root
on every worker, plus secrets and node admin — to anything that can route to the address, behind
one static token. A TCP relay in front of the loopback listener (`socat`, an SSH forward, a
proxy) is the same exposure by another route. **Neither is a sanctioned workaround.** If a
worker is on another machine, put it on the tailnet.

## The two keys, which are not the same thing

This is the single most common source of confusion, so it comes first.

| | **Tailscale auth key** | **Podium enrollment token** |
|---|---|---|
| What it is | A Tailscale credential that lets a process join your tailnet as a device | A Podium credential that lets a daemon become a specific node |
| Who issues it | You, in the Tailscale admin console | Your control plane: `podium node enroll-token` |
| Looks like | `tskey-auth-…` | 43 URL-safe base64 characters |
| Reusable? | **Yes** — one key enrolls your whole fleet | **No** — single use, and expires in 1h by default |
| Where it goes | `TS_AUTHKEY` (server), `PODIUM_NODE_TS_AUTHKEY` (node) | `PODIUM_NODE_ENROLL_TOKEN` |
| Read when | First run only; after that the state directory is the identity | First run only; after that `identity.json` is the identity |
| Podium sees it? | Never stores it, never logs it | Stores only its SHA-256 |

They authenticate different layers and both are required for a new worker. The Tailscale key gets
the process onto the network and gives it the `tag:podium-node` tag; the enrollment token tells
Podium *which* node this is and what labels it has. Neither substitutes for the other:

- Right key, no token → the daemon joins the tailnet, then stops with
  `no identity … and no enrollment token`.
- Right token, no key → the daemon never reaches the tailnet at all, and says so.
- Right token, key that is not tagged → the server answers `this device is not tagged as a
  Podium node`.

## What you must create in the Tailscale admin console

1. **Enable MagicDNS** — Admin console → **DNS**. Without it there is no name to put in a
   certificate.
2. **Enable HTTPS Certificates** — Admin console → **DNS** → *HTTPS Certificates*. Podium fails
   to start with a message linking to
   [the Tailscale HTTPS docs](https://tailscale.com/kb/1153/enabling-https) if this is off.
3. **Define the tags** — Admin console → **Access Controls**. Start from
   [`deploy/tailscale-acl.example.json`](../deploy/tailscale-acl.example.json).
4. **Mint two auth keys** — Admin console → **Settings** → **Keys** → *Generate auth key*. Both
   must be **Reusable** and **Pre-approved**; if your tailnet has device approval on, a key that
   is not pre-approved leaves the process hanging until a human clicks Approve.
   - one tagged `tag:podium-server` → the control plane's `TS_AUTHKEY`
   - one tagged `tag:podium-node` → every worker's `PODIUM_NODE_TS_AUTHKEY`

Auth keys expire (90 days by default). They are only read on a device's *first* run, so an
expired key does not disconnect anything that is already running — it only stops you adding new
workers. Mint a fresh one when that happens.

### Device key expiry is a different clock, and it is the one that takes a node offline

Each **device** also has its own key expiry, shown per device in the admin console and separate
from the auth key's. Unlike an auth key, this one *does* drop a running device off the tailnet.
There is no Podium error for it: the control plane just becomes unreachable, or a worker goes
`unreachable` and then `offline`, with nothing in either log saying why.

**A long-lived control plane and its workers want *Disable key expiry* on each device.** Do it
when you add them, not after.

The server warns about its own device only: `/readyz` turns 503 once its key is within 30 days
(`internal/transport/tailnet/server.go`). A worker's `/readyz` tracks only the control-plane
stream (`internal/node/health.go`), so a worker's key lapsing looks exactly like a network
outage. Watch the dates in the admin console.

## The ACL

[`deploy/tailscale-acl.example.json`](../deploy/tailscale-acl.example.json) is the policy from
the design, ready to paste. In one line: workers may reach the server on 443, people may reach
the server on 443, and **nothing grants server → node or node → node**.

That last part is the point. Podium's design has no server-initiated connection to a worker: the
node holds one outbound bidirectional stream and the server answers on it. The ACL turns that
from a property of the code into a property of the network, so a bug in Podium cannot reach a
worker either.

Verify it by hand after applying:

```sh
# From the machine running the server. This must FAIL.
tailscale ping podiumbot1

# From a worker. This must SUCCEED.
tailscale ping podium
curl -sf https://podium.<tailnet>.ts.net/healthz
```

The example policy also carries `tests`, which the admin console evaluates before it lets you
save — so a policy edit that accidentally opens server → node is rejected at the source.

> **This policy has never been applied intact, and its guarantee has never been enforced.** The
> tailnet Podium ran on already had a blanket allow-all rule, which made the `tests` block fail.
> The block was **dropped rather than the rule narrowed**, so nothing at the network layer has
> ever stopped a control plane dialling a worker. Identity and enrollment are proved; this part
> is still a design statement.
>
> Keep the `tests` block and narrow whatever conflicts with it. A `tests` block you had to delete
> to save has told you something.

## Identity: WhoIs replaces login

Every connection into the control plane arrives from a WireGuard peer that Tailscale can name.
The server calls `WhoIs(remoteAddr)` once per request and decides:

| WhoIs says | Podium concludes | On failure |
|---|---|---|
| device carries `tag:podium-node` | a worker (`Kind: node`), bound to that device's stable ID | — |
| device carries `tag:podium-server` | refused: control planes do not call control planes | **403** |
| device carries some other tag | refused | **403** |
| device is untagged and has a login name | a person (`Kind: user`), login recorded in `users` | — |
| nothing usable | — | **401** |

Consequences worth spelling out:

- **The web UI has no login step.** Open `https://podium.<tailnet>.ts.net` and the header already
  shows your name. The UI probes `IdentityService.WhoAmI` before deciding whether to ask for
  anything; over the tailnet that call succeeds with no credential, so the prompt never appears.
- **The CLI needs no token.** `podium --server https://podium.<tailnet>.ts.net nodes` works as
  it stands. `--token` still exists and is for the local transport only.
- **`requested_by` on a task is the real person's login**, because that is who Tailscale said
  submitted it.
- **A tagged device never counts as its owner.** Tailscale reports the tag owner's profile for a
  tagged device; treating that as a human login would let any tagged machine act as whoever
  created its tag. Podium checks tags first, and a tagged device that is not a Podium node is a
  403 rather than a fallback to the user path.

The first time a login is seen it is written to the `users` table (`login`, `display_name`,
`roles`, `first_seen_at`). There is no password column and never will be: `users` exists to hang
roles off later, not to authenticate anybody.

**How much of this has been observed.** Proved on a real tailnet: `WhoAmI` over HTTPS with no
bearer token returns a login and `IDENTITY_KIND_USER`, the UI header fills in with no prompt,
and a `tag:podium-node` device enrolled and ran work. Not proved: a **second identity** — one
login has ever authenticated, so `users` has never held two rows and the three refusal rows
above exist only in tests — and **device approval**, which is off on that tailnet.

### Knobs

| Variable | Default | Meaning |
|---|---|---|
| `PODIUM_TS_REQUIRED_NODE_TAG` | `tag:podium-node` | Which tag makes a device a worker |
| `PODIUM_TS_ALLOW_UNTAGGED_NODES` | `false` | Let an *untagged* device enroll as a node |

`PODIUM_TS_ALLOW_UNTAGGED_NODES` is an escape hatch for a tailnet that has no ACL tags yet. With
it on, the ACL tag stops being the network-level proof that a caller is an authorised worker, and
only the enrollment token stands between the tailnet and a running container. The server logs a
loud warning at startup whenever it is on. Only `1`, `true`, `t` (and their case variants) turn
it on — a knob that weakens authentication should not enable itself because someone wrote `yes`.

## Enrollment, and what binds a node to a machine

```
operator:  podium node enroll-token --label linux/amd64   -> single-use token
worker:    Enroll{token, hostname, arch, …}               -> {node_id, node_key}
worker:    Hello{node_id, node_key}                        (every reconnect, forever)
```

Three things are checked, in this order:

1. **The device tag.** `Enroll` requires `Kind: node`, i.e. a device carrying
   `tag:podium-node`. An untagged device is refused (unless the escape hatch above is on).
2. **A rate limit.** Five `Enroll` attempts per minute per remote IP, applied before the token is
   even looked at, so an unlimited endpoint is not an unlimited guessing run at a 32-byte token.
3. **The enrollment token.** Single use, enforced by the database rather than by a
   read-then-write, so eight concurrent redemptions produce exactly one winner.

The node's Tailscale **stable device ID** is recorded on the `nodes` row at enrollment
(`nodes.ts_stable_id`), and every later `Hello` must arrive from the same device. This is what
makes a copied `identity.json` useless: the node key is a bearer credential, so without the
binding whoever holds the file owns the node. A mismatch is refused with

```
node node_01j… is bound to another Tailscale device; if this machine really
replaces it, run `podium node rekey node_01j…` and reconnect
```

`podium node rekey NODE_ID` clears the binding. The node keeps its ID, labels and history, and
the next `Hello` binds it to whatever device it arrives from. Between the rekey and that
reconnect the node key alone is enough, so rekey immediately before moving a worker, not as a
matter of routine. A node that enrolled over the local transport has no binding and picks one up on
its first tailnet connection.

## tsnet or host mode

Podium can join the tailnet two ways. `PODIUM_TRANSPORT` (server) and `transport:` (node) choose.

### `tailnet` — embedded device (tsnet), the default and the tested path

Each **process** is its own Tailscale device with its own identity and its own tags. The host
does not need `tailscaled` installed, and containers work exactly like bare metal.

This is why the host machine's own Tailscale tags do not matter: `podium-server` and
`podium-node` take their tags from the auth key each one uses, not from the machine they run on.
An untagged laptop can run a `tag:podium-server` control plane.

| | Server | Node |
|---|---|---|
| Device name | `PODIUM_TS_HOSTNAME` (default `podium`) | `PODIUM_NODE_TS_HOSTNAME`, else `podium-node-<short hostname>` |
| State | `PODIUM_TS_STATE_DIR` (default `/var/lib/podium/tsnet`) | `<data_dir>/ts` |
| Listens | 443 (HTTPS) and 80 (redirect), **inside the tailnet only** | **nothing** |

**The state directory must persist.** It holds the device's node key. Lose it and the process
registers as a brand new device on the next start: the MagicDNS name drifts to `podium-1`,
`podium-2`, … and the admin console fills with ghosts. In Docker that means a named volume, which
is what `deploy/docker-compose.tailnet.yml` does.

### `host` — borrow the machine's `tailscaled`

`PODIUM_TRANSPORT=host` / `transport: host` uses the machine's existing `tailscaled` through its
local API instead of embedding a device: same identity model, same `WhoIs`, one fewer device in
the admin console. The server binds the machine's own tailnet IP on 443 and gets its certificate
from `tailscaled`.

Two caveats. The machine's own device must carry `tag:podium-server` (or `tag:podium-node` for a
worker), because there is no separate device to tag. And 443 is a privileged port, so the server
needs root or `CAP_NET_BIND_SERVICE`.

> **Host mode is unverified.** It is implemented and it compiles, but it has not been run against
> a real `tailscaled`. Use `tailnet` unless you have a specific reason not to.

## Reaching the shared memory from a worker

The agents' shared memory ([`agent.md`](agent.md#memory)) runs beside the control plane and is
the one thing in Podium a **task container** has to reach over the network. There are two URLs
because there are two vantage points, and they are almost never the same string:

| variable | who reads it | typical value |
|---|---|---|
| `PODIUM_AGENT_MEMORY_URL` | the conductor | `http://hindsight:8888` in compose, `http://127.0.0.1:8888` for a binary |
| `PODIUM_AGENT_MEMORY_TASK_URL` | goes into every turn's brief | `http://host.docker.internal:8888` |

**Same host.** The default is right. Every task container is created with
`host.docker.internal` mapped to the engine's bridge gateway — Docker Desktop provides the name
anyway; a native Linux engine needs the mapping, which the node adds — so a turn reaches the
host without knowing its address. Port 8888 must be published on an interface the bridge can
see, which is why `PODIUM_MEMORY_BIND` defaults to `0.0.0.0` and why the API key is mandatory.

**Workers on other machines.** `host.docker.internal` then points at the *worker's* host, where
there is no memory service. The task URL has to be an address every node's containers can route
to, and on a tailnet that is the control plane's tailnet IP:

```sh
PODIUM_MEMORY_BIND=100.x.y.z                                   # the control plane's tailnet IP
PODIUM_AGENT_MEMORY_TASK_URL=http://100.x.y.z:8888
```

A tailnet IP rather than the MagicDNS name, because a task container does not use the host's
resolver. Then add the rule to the ACL:

```json
{ "action": "accept", "src": ["tag:podium-node"], "dst": ["tag:podium-server:8888"] }
```

That is a **widening**: it is the first rule in
[`deploy/tailscale-acl.example.json`](../deploy/tailscale-acl.example.json)'s shape that lets a
worker reach the control plane on anything but 443, and what it grants is full read/write of
every memory the organisation has, to anything running on a worker. The example policy does not
include it; add it deliberately, and read
[`security.md`](security.md#5-the-conductor-and-the-bot) first.

**Neither.** Leave `PODIUM_AGENT_MEMORY_URL` empty. Turns then run with no memory at all, which
is a supported configuration and costs nothing but recall.

## Reaching the control plane from a task

A task that has to *use* Podium — drive a browser over the UI, read the server's own database —
reaches it the same way any other tailnet client does, and the shared-memory table above is not
the shape to copy. Two addresses, and the first one surprises people:

| target | from the task container | from a sidecar |
|---|---|---|
| the control plane, over the tailnet | `https://<host>.<suffix>.ts.net` | same |
| a port published on the node's own host | `host.docker.internal:<port>` | not reachable |

**The tailnet name works, and so does the tailnet IP.** The container inherits the host's
routes, so a task on a tailnet node routes to `100.x.y.z` without being a tailnet device
itself, and the MagicDNS name resolves in *public* DNS once the tailnet has HTTPS certificates
enabled. A browser sidecar gets this too — it is on the same bridge — which is what makes a
live-stack UI test from a task possible at all. The confinement here is the ACL
([above](#the-acl)), not the network: to a Tailscale ACL the traffic is the *node* talking, so
whatever `tag:podium-node` may reach, every task on that node may reach.

**`host.docker.internal` is the task container only.** The node adds that mapping to a task and
deliberately not to its sidecars ([`security.md`](security.md#3-a-task-container--untrusted)),
so a browser sidecar cannot open a port that is only published on the node's host — point it at
the tailnet address instead. And on a native Linux engine a service bound to `127.0.0.1` is out
of reach through the bridge gateway even from the task; Docker Desktop proxies it, which makes
this the kind of difference that works on a Mac and fails on a worker.

## Why there is no public ingress

Nothing in Podium needs a public address:

- Workers **dial out**. A worker behind NAT, on a home connection, in a coffee shop, with every
  inbound port closed, works unchanged. `ss -ltnp` on a worker shows nothing listening but the
  metrics port on loopback.
- The control plane listens **only inside the tailnet**, on its own device's 443. There is no
  host port to firewall.
- People reach it because they are on the tailnet, not because it is on the internet.

When webhooks eventually arrive, the documented options are Slack Socket Mode and polling (still
zero ingress) or Tailscale Funnel for a single `/webhooks/*` route on a separate mux — so
exposing a webhook never exposes the API or the UI.

## Architecture: the daemon binary and the task image are different questions

**The control plane and its workers do not have to share an architecture.** A darwin/arm64 server
drives linux/amd64 workers perfectly well; only the wire is shared. **linux/amd64 is the expected
default for workers.**

Check the target, then copy the matching binary:

```sh
uname -m            # x86_64 -> GOARCH=amd64 ; aarch64 -> GOARCH=arm64
```

```sh
make dist-node GOOS=linux GOARCH=amd64      # -> bin/podium-node-linux-amd64, bin/podium-linux-amd64
make dist-node GOOS=linux GOARCH=arm64      # -> bin/podium-node-linux-arm64, bin/podium-linux-arm64
make dist-node-all                          # both
```

These are static (`CGO_ENABLED=0`) and run on any glibc or musl Linux. The matrix matches
`.goreleaser.yaml`, which builds the same set for releases.

**A task container's image architecture is a separate matter.** A task running `alpine:3` on a
linux/amd64 node pulls the amd64 image; on an arm64 node, the arm64 one. Multi-arch images make
that invisible, but an image pinned to one architecture — or one you built yourself — only works
on matching nodes. That is exactly what labels are for: label your workers `linux/amd64` and put
`labels: [linux/amd64]` in specs that need it, and the scheduler will only place them there.

## Placement and storage — read before you deploy

- **Postgres is not disposable.** Every task, event and log chunk lives there. Put it on real
  storage with real backups, not on the same disposable machine as a worker.
- **A worker's `data_dir` and image cache are I/O heavy.** Local disk, never a network share.
  `identity.json` (the node key) and `ts/` (the Tailscale device) both live there and are both
  credentials: `0600`, never in a git repository, never copied between machines. The device
  binding above will refuse a copied `identity.json`, but do not rely on that as your only
  control.
- **The server's tsnet state directory is a credential too**, and must persist. See above.
- **A node host is root-equivalent to whoever can submit tasks.** The daemon needs the Docker
  socket, and a task is an arbitrary container. Run workers on machines that do nothing else.

## Troubleshooting

Work from the bottom of the stack up. Almost every report of "the node will not connect" is one
of four things: HTTPS is off, the key is not tagged, the key is not pre-approved, or the state
directory is not persisting.

```sh
# 1. Is this machine on the tailnet at all, and what is the MagicDNS suffix?
tailscale status --json | grep -i magicdnssuffix

# 2. Does the control plane's name resolve, and does anything answer?
curl -sS -o /dev/null -w '%{http_code}\n' https://podium.<tailnet>.ts.net/healthz

# 3. What does the control plane think this caller is? There is no `podium whoami`; the web
#    UI's header shows it (it calls IdentityService.WhoAmI), and the server logs it.
podium --server https://podium.<tailnet>.ts.net nodes    # 200 = it named you, 401/403 = it did not

# 4. The node's own view.
curl -sS http://127.0.0.1:9091/readyz
journalctl -u podium-node -n 50
```

`/healthz` needs no credential under either transport, so a 200 there and a 401/403 on an RPC
separates "cannot reach it" from "not allowed".

**Reading a status code:**

| | |
|---|---|
| connection refused / DNS failure | not a Podium problem. MagicDNS, the ACL, or the server is not running |
| **401** with `WWW-Authenticate: Bearer` | the transport does not know who you are. Under `dev`: no token or a wrong one. Under `tailnet`: WhoIs returned nothing, which usually means the request did not arrive over the tailnet at all |
| **403**, no challenge | it knows who you are and says no. A tagged device that is not `tag:podium-node`, or the control plane's own device calling itself |
| 200 on `/healthz`, 401 on everything else | the server is up and the credential is the problem |

| Symptom | Cause and fix |
|---|---|
| `HTTPS certificates are not enabled on this tailnet` | Admin console → DNS → HTTPS Certificates |
| `MagicDNS is not enabled on this tailnet` | Admin console → DNS → MagicDNS |
| `holds no Tailscale device yet and TS_AUTHKEY is not set` | First run needs an auth key; later runs read the state dir |
| Process hangs printing a login URL | The auth key was rejected or is missing — check it is reusable and not expired |
| Device appears but stays "needs approval" | The key was not **pre-approved**, or device approval is on. Approve it, or mint a pre-approved key |
| `this device is not tagged as a Podium node` | The worker's auth key is not tagged `tag:podium-node` |
| `carries tag:podium-server` (403) | Something is dialling the control plane from the control plane's own device |
| `is bound to another Tailscale device` | A different machine is presenting this node's key. If deliberate: `podium node rekey NODE_ID` |
| `more than 5 attempts in 1m0s from this address` | The enrollment rate limit. Wait a minute |
| The name drifts to `podium-1`, `podium-2`, … | The tsnet state directory is not persisting |
| `/readyz` says `node key expires in Nd` | The **control plane's own device** key is inside 30 days. Tag the device, or give it *Disable key expiry* — the message names both. This is the server's device only; nothing warns you about a worker's |
| The node connects but no task ever runs | Not a networking problem: check `max_tasks` and that the spec's labels are a subset of the node's. `podium task get` prints `queued_reason` |
| `podium` asks for a token against an `https://` server | The CLI decides on the URL scheme. An `https://` server needs none; if one is configured it is sent anyway, which keeps a mixed setup working |
| A user gets 403 where you expected 401 | Their device carries an ACL tag. A tagged device is never treated as a person, because Tailscale reports the *tag owner's* profile for one |
| The **first** HTTPS request after a start hangs or times out, and the server logs `TLS handshake error … i/o timeout` | Not a failure. The certificate is issued **during that first handshake** — retry, and the second request answers in a fraction of a second. See below |
| Certificate errors from a browser or `curl` | HTTPS Certificates were enabled after the server started. Restart it: whether the tailnet permits certificates at all is checked once, at listen time, and a server that started before you turned them on will never look again |
| `cannot get a certificate for podium.<tailnet>.ts.net` | The device's name is not what you think. Check `PODIUM_TS_HOSTNAME`, and that no other device already owns that name |
| Everything worked for months, then a device silently left the tailnet | Its **device key** expired — a different clock from the auth key's, one per device, shown in the admin console. Disable key expiry on it, tag it, or re-authenticate it. A worker's `/readyz` will not have warned you; see [Device key expiry](#device-key-expiry-is-a-different-clock-and-it-is-the-one-that-takes-a-node-offline) |
| An ACL change takes effect for new connections only | Tailscale evaluates the policy at connection time. Restart the node to pick up a widened ACL |
| The ACL denies the node and you cannot tell why | `tailscale ping podium` from the worker, and use the admin console's ACL preview. Podium sees only "the connection did not arrive" |

### The first HTTPS request after a start is slow, and its log line looks like a failure

Two things happen at two different times:

- **At listen time** the server checks whether the tailnet permits certificates at all. That is
  the check a restart re-runs, and why turning HTTPS Certificates on *after* the server started
  needs a restart.
- **The certificate itself** is fetched lazily, inside the first TLS handshake. That handshake
  waits for the certificate to be issued, which can outrun a client's default timeout.

So the first request may hang and `curl` may give up, while the server logs:

```
http: TLS handshake error from 100.x.y.z:NNNNN: read tcp …: i/o timeout
```

That is Go's `net/http` saying *the client* left mid-handshake — not a rejection. **Retry.** The
certificate is cached, and the second request answers in a fraction of a second. Worry only if it
persists; then check what the server is actually serving:

```sh
openssl s_client -connect podium.<tailnet>.ts.net:443 \
  -servername podium.<tailnet>.ts.net </dev/null 2>/dev/null \
  | openssl x509 -noout -subject -issuer -dates
```

## What is not built

- `mtls` transport (server-issued client certificates for teams that cannot adopt Tailscale) is
  deliberately deferred and has no step.
- Per-task tailnet attachment — giving a task container its own tailnet identity — is a later
  add-on. **There is no egress policy for task containers.** A task reaches its own sidecars and
  the internet; whether it can also reach the *host's* tailnet or LAN depends entirely on the
  host's routing and firewall, and Docker's default bridge setup forwards it. This has never
  been tested and nothing in Podium restricts it — **assume a task can reach whatever its worker
  can reach**, and firewall the host if that matters. See
  [`security.md`](security.md#3-a-task-container--untrusted).
- Tailscale Funnel for webhooks, as above.
