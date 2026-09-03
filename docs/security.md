# Security

What Podium protects, what it does not, and where the boundaries actually are. Read the trust
model before you decide which machines run a node.

**Podium is pre-alpha and has never had a security review.** Nothing here has been tested by
anyone trying to break it. Treat the whole system as inside your perimeter, not as part of it.

---

## Trust model

Four things, in decreasing order of trust.

### 1. The control plane — trusted

`podium-server` holds the database, the secrets master key and the object store credentials.
Everything that can compromise a Podium deployment starts here. Run it on a machine you
administer, back up its Postgres and its master key, and give nothing else a login on it.

### 2. The node daemon — **root-equivalent on its host**

`podium-node` drives `/var/run/docker.sock`. Anything that can talk to that socket can start a
container with `--privileged` and `-v /:/host` and own the machine. That is true of the daemon,
and it is true of anyone who can make the daemon start a container.

Three consequences, and they are not negotiable:

- **Running the daemon as a non-root user in the `docker` group buys nothing.** It is the same
  power with a longer name. The shipped systemd unit runs as root and says so.
- **A node host is root-equivalent to whoever can submit tasks.** There is no RBAC (below), so
  in practice: everyone who can reach the API can run arbitrary code as root on every worker.
- **Run workers on machines that do nothing else.** A node is a machine you are willing to let
  arbitrary containers run on. Do not put one on a machine that also holds production data, a
  CI signing key, or somebody's laptop session.

The systemd unit's hardening (`ProtectSystem=strict`, `NoNewPrivileges`, and the rest) protects
the host from the daemon's *mistakes*. It does not protect the host from the daemon, and it
cannot: the socket is the whole job.

### 3. A task container — **untrusted**

A task is somebody else's code. Podium sandboxes it:

| | |
|---|---|
| Capabilities | every one dropped (`CapDrop: ALL`); `hardening.capabilities` adds back from a seven-entry allow-list |
| Privilege escalation | `no-new-privileges:true`, always |
| Seccomp | the engine's default profile. `unconfined` is deliberately not a spec field |
| Docker socket | never mounted into a task. Asserted by a test |
| `--privileged` | never set. Asserted by a test |
| Root filesystem | read-only on request (`hardening.read_only_rootfs`), with a 1 GB tmpfs on `/tmp` so images still work |
| Memory | `memory_mb`, with swap pinned to the same number, so exceeding it is an OOM kill and not a swapped-out machine |
| PIDs | `resources.pids`, default 4096 |
| Network | its own bridge network per task, shared only with its own sidecars |

What that does **not** buy you:

- **The container boundary is not a security boundary.** A kernel exploit from inside a
  container is a compromise of the node, and the node is root-equivalent on its host.
- **There is no egress policy.** A task reaches its own sidecars by name, and it reaches the
  internet. Whether it can also reach the *host's* other networks — including a tailnet, or a
  database on the host's LAN — depends on the host's routing and firewall, and Docker's default
  bridge setup forwards it. **Assume it can.** This has never been tested and nothing in Podium
  restricts it. If a worker sits on a network that matters, firewall it at the host.
- **Sidecars are not hardened like the task container.** They keep their capabilities and their
  writable root filesystem, because a stock database image chowns files and drops privileges on
  the way up and breaks under `CapDrop: ALL`. A sidecar image is as trusted as the task.
- **The runner event socket is world-writable inside the container** (mode 0666, so that a task
  running as a non-root user can report). Any process in the task container can therefore forge
  `step`, `artifact` and `message` events. Today that only produces cosmetic log entries and an
  artifact upload the task could have made anyway; it becomes a real problem the moment those
  events drive server-side state.
- **A `message` event's text is untrusted content, and it is not redacted.** A task can forge a
  `message` of any type, with any text, naming any attachment. None of it drives server state —
  no status transition, no storage beyond the `task_events` row — but the text is written by an
  untrusted process, and secret redaction does not apply to it: the node's redactor is a
  log-chunk pipeline and never sees a message payload. **Every relay that posts a message
  somewhere — Slack, Linear, a web chat — must treat the text as untrusted content from an
  untrusted process, exactly as it would treat a log line.** Relay it; never interpret it, never
  execute it, never let it name the channel it is posted to.

### 4. Anyone who can reach the API — **fully trusted, because there is no RBAC**

There are no roles, no per-user permissions and no read-only mode. Under the tailnet transport
the server records who visited in a `users` table and then lets them do everything. Whoever can
reach the API can submit tasks (and therefore run code as root on every worker), drain nodes,
delete secrets and delete nodes.

---

## Transports, and what crosses the wire

### `dev` — loopback, shared bearer token

- The listen address **must resolve to loopback**; the server refuses to start otherwise. That
  check is what makes the rest of this acceptable.
- Every RPC carries `Authorization: Bearer <PODIUM_DEV_TOKEN>`, compared in constant time.
  There is one token for everything and everyone. It has no identity: audit rows say `dev`.
- **The connection is unencrypted HTTP.** Everything crosses it in the clear, and that includes
  **resolved secret values**, which travel inside `Assign` from the server to the node. There is
  no TLS and no per-node key on the HTTP layer.
- The server logs a warning at startup when the dev transport is in use and any secret exists,
  for exactly that reason.
- The web UI keeps the token in `localStorage`.

Loopback is doing all the work. Do not publish a dev-transport port to anything but
`127.0.0.1`, and do not use the dev transport across a network under any circumstances.

### `tailnet` — the one to use for real workers

- The server embeds its own Tailscale device (tsnet) and serves HTTPS on its MagicDNS name.
  Traffic is WireGuard end to end; the certificate comes from Tailscale.
- **There is no token and no login page.** Identity comes from the connection: Tailscale's
  `WhoIs` names the caller, and the ACL decides who can open a connection at all.
- **There is no public ingress.** The server listens on port 443 of its own tailnet device.
  Workers dial out and listen for nothing.
- A device with `tag:podium-node` is a node. A device with `tag:podium-server` is refused (403)
  — control planes do not call control planes. Any *other* tag is also refused, because
  Tailscale reports the tag owner's profile for a tagged device and honouring it would let any
  tagged machine act as whoever created its tag. An untagged device is a user.
- `PODIUM_TS_ALLOW_UNTAGGED_NODES=true` removes the network-level proof that a caller is an
  authorised worker. It exists for a tailnet with no ACL tags yet. The server warns loudly.

**Unverified.** The tailnet transport has never been run against a real tailnet — that needs
tagged auth keys and HTTPS enabled, neither of which the build machine had. Everything above is
what the code does; none of it has been observed in the wild. `host` mode is likewise
implemented and never run.

### Ports

| Port | Who | Authentication |
|---|---|---|
| `127.0.0.1:8080` | server, `dev` transport | bearer token, except `/healthz`, `/readyz`, `/metrics` |
| `:443` on the server's tailnet device | server, `tailnet` transport | Tailscale identity |
| `:80` on the server's tailnet device | redirect to 443 | none |
| `127.0.0.1:9091` | node | **none.** `/healthz`, `/readyz`, `/metrics` |

`/healthz`, `/readyz` and `/metrics` are open on both daemons. They leak liveness, a Postgres
reachability bit, and Go runtime and process metrics — no task content, no identities, no
secret names. Keep them on loopback anyway; the node's default already is.

---

## Node identity

Two credentials, and people confuse them constantly. See
[`networking.md`](networking.md#the-two-keys-which-are-not-the-same-thing).

**Enrollment token** — Podium's own. 32 random bytes as URL-safe base64, **single use**, 1 hour
default TTL, only its SHA-256 reaches the database, rate-limited to 5 attempts per minute per
source IP *before* the token is looked at. Minted with `podium node enroll-token`.

**Node key** — what `Enroll` returns. 32 random bytes, **returned exactly once**; the database
holds only `sha256(key)`. It lands in `<data_dir>/identity.json`, mode 0600. There is no
recovery path and no reissue: a lost key means a fresh enrollment token.

`identity.json` is a bearer credential — whoever copies the file owns the node. Under the
tailnet transport that is backstopped by a **device binding**: `Enroll` records the Tailscale
`StableID` on the node row, and every later `Hello` is checked against it. A different device
presenting the same key is refused with a message naming `podium node rekey`. Do not rely on
the binding as your only control; protect the file.

`podium node rm` forgets a node but **does not stop its daemon**. A removed node whose
`identity.json` survives reconnects forever and is told its key is unknown, once per backoff.
There is no revocation push. Stop the daemon yourself.

---

## Secrets

### At rest

One 32-byte AES-256 key encrypts every stored secret. `podium-server gen-master-key` mints it.

- **The file's mode is enforced.** Anything a group or another account can read (`perm&0o077`)
  is refused, before the store is even opened, so the process exits with an actionable message
  and never touches Postgres. `0600` and `0400` pass.
- `PODIUM_MASTER_KEY` takes the key inline for development. An environment variable is visible
  in `/proc` and in `docker inspect`; the server warns loudly. The file wins if both are set.
- **No key is a legitimate configuration.** Every secret call answers `FailedPrecondition` and a
  task naming a secret is failed at admission with a message saying which name. Podium is still
  a task runner without secrets.
- `key_id` is `hex(sha256(key)[:8])` and is stored on every row, so a half-finished rotation is
  visible in `podium secret ls`.
- **Loss is final.** There is no escrow, no second key and no recovery path. `gen-master-key
  --out` uses `O_EXCL` so it can never silently overwrite a live key. **Back the file up
  somewhere that is not the control plane.**
- Rotation is offline and atomic: `podium-server rotate-master-key --old FILE --new FILE` takes
  `select ... for update` over the whole table in one transaction, so it is never half under one
  key. Afterwards the old key decrypts nothing.

**There is no read endpoint and there must never be one.** `SecretService` is
`SetSecret`/`ListSecrets`/`DeleteSecret`. `ListSecrets` returns names, versions and key ids —
no value, no ciphertext, in the message at all. A "reveal" button is not a feature that could be
added later without changing the threat model.

### In flight

```
podium secret set NAME  ──►  server: AES-256-GCM under the master key  ──►  Postgres
                                                │
task is dispatched to a node                    │  resolved once per dispatch,
                                                ▼  values in server memory for one call
                                    Assign{resolved_secrets: [{name, target, key, value}]}
                                                │
                                                ▼  ** plaintext on the wire **
                                         podium-node
                                                │
                        ┌───────────────────────┴──────────────────────┐
                        ▼                                              ▼
        target: env  →  container environment              target: file  →  0400 file on a
        (after the spec's own env:, so a                    tmpfs at /podium/secrets,
        secret wins over a plaintext entry                  bind-mounted read-only at the
        of the same name)                                   path the ref names
```

- **Under the `dev` transport that `Assign` crosses an unencrypted loopback socket.** Under
  `tailnet` it is inside WireGuard.
- A value is in server memory for the length of one dispatch and is zeroed afterwards, on both
  the resolver's slice and the node's. Go may have copied it during the proto marshal; nothing
  short of a locked-memory allocator fixes that.
- An `env` value has to become a Go `string` to reach the Docker API, which takes `[]string`.
  Every other path keeps it as `[]byte` and zeroes it.
- File secrets are shredded at teardown: chmod writable, overwritten with zeroes, `fsync`ed,
  unlinked, directory removed.
- `Assign` is re-resolved on every dispatch, so a node that reconnects and is re-assigned gets a
  fresh resolve — and a `secret.resolve` audit row exists per dispatch, not per task.
- **A sidecar cannot reference a secret.** A sidecar that needs a credential takes it from a
  plaintext `env:` entry. Known gap.

### Redaction — what it does and does not guarantee

Every log chunk leaving a node is passed through a redactor built from the values of that task's
own resolved secrets. A match is replaced with `[redacted:NAME]`. Because it happens on the
node, **the value never crosses the wire**, so `podium logs`, the web UI and the archived log
all show the same redacted text. There is one log path.

It matches:

- any secret value of **8 bytes or more** (shorter values are too collision-prone to be worth
  the false positives),
- and that value re-encoded as standard base64, raw standard base64, URL base64, raw URL
  base64, `url.QueryEscape` and `url.PathEscape`,
- longest pattern first, across a chunk boundary (a partial trailing match is held back rather
  than emitted).

**It is string matching, and string matching is not a guarantee.** It does not catch:

- a value the task **transforms** — hex, gzip, a hash, a fragment, a different encoding;
- a value **split across two writes** by something other than the coalescer, or interleaved
  with other output within one write;
- a value **shorter than 8 bytes**;
- anything the task sends somewhere that is not its stdout or stderr — a network call, an
  artifact, a file it writes;
- anything at all after a **node restart**. An adopted task has no redactor: the values were in
  the previous incarnation's memory. The container keeps running; only the filter is gone. The
  adopted run announces itself with a `step{name: "node/reattached"}` event, which is the seam.

Treat redaction as defence in depth against an accidental `echo $PASSWORD`, never as the
control that keeps a secret out of a log. **The control is not printing it.**

---

## Artifacts and the object store

- **Nodes never talk to the object store.** An artifact travels node → server → S3 over
  `NodeService.UploadArtifact`. A presigned PUT from the node was deliberately not taken: it
  would need every worker to hold a route and a credential to the store, which is the invariant
  the networking design exists to avoid.
- `UploadArtifact` is its own HTTP request, not part of the node stream, so it re-presents
  `node_id` + `node_key` exactly as `Hello` does. The server also checks the task is on that
  node.
- **512 MB per artifact**, enforced twice: the node refuses an oversized one from the tar header
  before it crosses the wire, and the server enforces the real size on the way past and removes
  the object if a producer lied.
- Object keys are sanitised to `[A-Za-z0-9._-]`, at most 128 characters, leading dot trimmed —
  `../../etc/passwd` becomes `etc_passwd`. The original name is kept in the database and is what
  the UI shows.
- `GetArtifactURL` mints a **presigned GET good for 15 minutes**. Anyone holding that URL can
  read the object without any Podium credential, which is the point and also the risk: do not
  render one into a page that outlives it, and do not paste one anywhere durable. The download
  button in the UI and `podium artifact get` both proxy through the server instead.
- **Nothing ever deletes an artifact.** There is no retention policy and no lifecycle rule, so a
  bucket grows without bound and a task's output outlives the task indefinitely.

---

## Reporting a vulnerability

See [`SECURITY.md`](../SECURITY.md) at the repository root. Do not open a public issue.

---

## Known gaps, in one list

Everything below is a real hole, not a hypothetical:

- **No RBAC.** Anyone who can reach the API can do everything, including running code as root on
  every worker.
- **No egress policy for tasks.** Whether a task can reach the host's other networks is up to
  the host, untested, and probably yes.
- **The dev transport is plaintext**, secret values included.
- **The tailnet transport has never been run against a real tailnet.**
- **Redaction does not survive a node restart** and is best-effort at the best of times.
- **The runner event socket is world-writable inside the container**, so a task can forge `step`
  and `artifact` events.
- **A sidecar cannot use a secret**, so credentials for one end up in plaintext `env:`.
- **`podium node rm` does not revoke anything** — it forgets a node whose daemon keeps dialling.
- **`/metrics` and `/healthz` are unauthenticated** on both daemons.
- **No audit for reads.** `audit_log` records secret set/delete/resolve/rotate. It does not
  record who listed nodes, read a task's log, or downloaded an artifact.
- **Single server process.** Sessions are in memory; a second replica would see every node as
  sessionless and start expiring leases. This is an availability problem, not a confidentiality
  one, but it is the reason there is no HA story.
- **No security review, no fuzzing, no dependency scanning beyond the SBOM the release
  produces.**
