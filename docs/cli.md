# `podium` — command-line client

`podium` talks to `podium-server` over Connect and to nothing else. It never needs Docker,
and it never needs to be on the same machine as a node.

## Configuration

Three sources, in increasing order of precedence:

| Source | Server | Token |
|---|---|---|
| `~/.config/podium/config.yaml` (or `$XDG_CONFIG_HOME/podium/config.yaml`) | `server:` | `token:` |
| Environment | `PODIUM_SERVER` | `PODIUM_TOKEN` |
| Flags | `--server` | `--token` |

```yaml
# ~/.config/podium/config.yaml
server: http://127.0.0.1:8080
token: devtoken
```

The default server is `http://127.0.0.1:8080`.

**A token is a dev-transport artefact.** Over the tailnet, Tailscale names the caller at the
connection level, so there is nothing to present and none is asked for:

```sh
podium --server https://podium.taila79bf6.ts.net nodes     # no --token
```

The rule is the URL scheme: an `https://` server is a tailnet control plane and needs no token; a
`http://` one is the dev transport and the CLI refuses to run without one, because every RPC
would 401. See [networking.md](networking.md).

## Exit codes — contractual

Scripts depend on these. They do not change without a version bump.

| Code | Meaning |
|---|---|
| `0` | The command succeeded. For `podium run`, the task exited 0. |
| `1..124`, `126..127` | `podium run` only: the task's own exit code. |
| `125` | Infrastructure failure — the server was unreachable, the event stream was lost for longer than 90s, the image would not pull, or the task ended without an exit code. |
| `128+N` | `podium run` only: the task's own exit code when it was killed by signal N (`137` = SIGKILL, `143` = SIGTERM). |
| `130` | The task was cancelled. |
| `1` | Bad invocation, or a request the server rejected. |

`125` and `130` are reserved by this table, so a task that genuinely exits 125 or 130 is
indistinguishable from an infrastructure failure or a cancellation. Use `podium task get
--json` when the difference matters.

## Streams — contractual

| Stream | Carries |
|---|---|
| stdout | the task's stdout for `run` and `logs`; command output (tables, JSON, IDs) for everything else |
| stderr | the task's stderr for `run` and `logs`; every sidecar's output, prefixed `[name]`; Podium's own progress lines, prefixed `→` and dimmed on a terminal |

So `podium run … > out.txt` captures exactly the task's stdout, and
`TOKEN=$(podium node enroll-token)` captures exactly the token. A sidecar's output is not
the task's, so it goes to stderr: a database's startup chatter never lands in a redirect
meant for the task's own results.

## Commands

### `podium run [flags] -- COMMAND [ARG...]`

Creates a task and, unless `--detach`, follows it to completion.

```sh
podium run --image alpine:3 -- sh -c 'echo hi; exit 3'; echo "exit=$?"   # exit=3
podium run --spec task.yaml --detach                                     # prints the task ID
```

| Flag | Meaning |
|---|---|
| `--image` | container image |
| `--label` | node label the task requires (repeatable) |
| `--env KEY=VALUE` | environment variable for the task (repeatable) |
| `--secret REF` | secret to inject: `NAME`, `NAME:env:KEY` or `NAME:file:/abs/path` (repeatable) |
| `--timeout` | task timeout (default 1h) |
| `--working-dir` | working directory inside the container (default `/workspace`) |
| `--spec FILE` | task spec YAML; flags override its fields |
| `--detach` | print the task ID and return 0 |
| `--max-attempts N` | how many times the task may be assigned (default 1) |
| `--retry-on-node-loss` | re-run the task elsewhere if its node goes offline, instead of marking it `lost` |

Everything after `--` is the command. Progress lines on stderr:

```
→ task task_01j…
→ scheduled on node_01j…
→ running
→ finished exit 3 in 5.2s
```

A task with sidecars reports each one's lifecycle and streams its output, both on stderr:

```
→ task task_01j…
→ scheduled on node_01j…
→ sidecar/db started
[db] PostgreSQL init process complete; ready for start up.
[db] LOG:  database system is ready to accept connections
→ sidecar/db ready
→ running
```

Sidecars, resource limits and hardening are spec-file fields with no flag equivalent; see
[task-spec.md](task-spec.md) and `examples/postgres-sidecar.yaml`.

`--secret NAME` is the common case and means "put it in an environment variable of the same
name". A task that names a secret which does not exist fails before it reaches a node, and
`→ failed: …` says which one.

**Secret values are redacted from the task's output**, on the node, before anything is sent.
So `podium run --secret GREETING -- sh -c 'echo $GREETING'` prints `[redacted:GREETING]`,
not the value — there is one log path and the value does not travel it. See
[task-spec.md](task-spec.md#redaction).

`run` reconnects to the event stream on its own if the control plane restarts mid-task,
resuming from the last sequence number it printed, so the output has no gap.

### `podium tasks [--status s]... [--limit n] [--node ID]`

Table of tasks, newest first. Statuses: `queued`, `scheduled`, `provisioning`, `running`,
`succeeded`, `failed`, `cancelled`, `lost`.

### `podium task get TASK_ID [--json]`

One task. `--json` prints the wire representation, which is what scripts should parse.

Two fields answer "why has nothing happened?":

- `queued_reason` — why the scheduler has not placed the task, in one sentence: nothing is
  connected, no node carries a label it asks for, every candidate is draining or full, or
  nothing has enough free CPU or memory. `last_schedule_attempt_at` says when it last looked.
- `failure_reason` — why a task that will never run ended: a missing secret, `timeout`, `oom`,
  `node did not accept assignment`, or the name of the node that went offline under it.

`lost` is not `failed`. A task is `lost` when the machine running it went away and the spec
did not say it could be re-run; nothing about the task itself went wrong, and re-running it is
the operator's call. `retry_on_node_loss: true` (or `--retry-on-node-loss`) makes that call in
advance, and the task comes back as a new attempt instead — up to `max_attempts`.

### `podium task cancel TASK_ID [--reason TEXT]`

Asks the node to stop the task and returns immediately. Cancellation is asynchronous: the
node sends SIGTERM through `podium-runner`, which forwards it to the task, and SIGKILLs 30
seconds later. A task that handles SIGTERM stops in a second or two and reports exit 143.

The intent is recorded on the task row, not in the server's memory, so a control plane that
restarts between the cancel and the container dying still lands the task `cancelled`. If the
node never comes back at all, the server writes the terminal status itself 60 seconds later
rather than leaving the task `running` forever.

A task that outruns its spec's `timeout` is cancelled the same way, and ends `failed` with
`failure_reason: timeout` — a stop the operator did not ask for is not a cancellation.

### `podium logs [-f] [--from-seq N] TASK_ID`

Prints a task's output, stdout and stderr on the matching local streams, and each sidecar's
output on stderr prefixed with `[name]`. `--from-seq` is
**exclusive**: the output resumes at `N+1`, so passing the last sequence number you saw
gives you the next one and no repeat. Without `-f` the command stops once it has caught up;
with `-f` it follows until the task is terminal, reconnecting on its own if the stream
breaks.

### `podium nodes`

Table of enrolled nodes: name, ID, status, labels, running/max slots and heartbeat age.
`RUNNING/MAX` is filled in only for nodes holding a live stream on the server you asked.

Status is derived from the heartbeat: `online`, `unreachable` after 30 seconds of silence,
`offline` after 120 — at which point the node's tasks are requeued or marked `lost` —
and `draining` for a node an operator has taken out of the pool. A node that is drained but
not connected reads `offline (draining)`, because the instruction outlives the connection.

### `podium node drain NODE` · `podium node undrain NODE`

`NODE` is a node's name or its ID, whichever is easier to type.

Draining stops a node being given new work. Whatever it is already running finishes
normally — that is the whole point: a drain takes a machine out of service without killing
the jobs on it. The instruction is stored, so it survives both daemons restarting and can be
set on a node that is offline right now.

```sh
podium node drain worker-3          # finishes what it has, takes nothing new
podium nodes                        # worker-3 … draining
podium node undrain worker-3        # back in the pool immediately
```

A node started with `--exit-on-drain` (or `PODIUM_NODE_EXIT_ON_DRAIN=1`) exits 0 once its
last task finishes, which is how a supervisor replaces the binary. Without that flag a
drained node stays connected and idle until it is undrained, which is what you want when the
machine is being worked on rather than upgraded.

### `podium node rm NODE [--force]`

Forgets a node. It is refused while the node holds a live stream and has not been drained,
and refused outright while any task is still running on it — pass `--force` only for the
first of those.

```sh
podium node drain worker-3 && podium node rm worker-3
```

Removing a node does not stop its daemon. A worker whose `identity.json` still exists keeps
trying to reconnect and is told its key is unknown; delete the data dir to re-enroll it.
Finished tasks keep the node ID they ran on, so the history stays readable.

### `podium node enroll-token [--label L]... [--ttl 1h]`

Mints a single-use enrollment token and prints it to stdout, so it can be captured:

```sh
TOKEN=$(podium node enroll-token --label linux/arm64)
```

The token is shown once. The server keeps only its SHA-256.

This is Podium's own enrollment token, not a Tailscale auth key — a worker needs both, and they
are different things. See [networking.md](networking.md#the-two-keys-which-are-not-the-same-thing).

### `podium secret set NAME [--value V | --from-file FILE]`

Creates or replaces a secret. The value comes from stdin unless a flag names another source.

```sh
printf %s 'hunter2' | podium secret set DB_PASSWORD
podium secret set DEPLOY_KEY --from-file ~/.ssh/id_ed25519
podium secret set GREETING --value hello        # warns: this is in your shell history
```

- Stdin loses **one** trailing newline, because typing or echoing a value adds one.
  `--from-file` and `--value` are taken byte for byte, so a PEM file or a binary key
  survives intact.
- Setting an existing name replaces the value and bumps its version. Tasks already assigned
  keep the value they were given.
- `NAME` must match `^[A-Za-z_][A-Za-z0-9_.-]*$`.
- The server needs a master key (`PODIUM_MASTER_KEY_FILE`) or this fails with
  `failed_precondition`. See [Secrets at rest](#secrets-at-rest).

### `podium secret ls`

Names, versions, the master key each row is encrypted under, who set it and when. **There is
no `podium secret get`, and there will not be.** The only way a value comes back out is
inside an `Assign`, on its way to the node running the task that referenced it.

### `podium secret rm NAME`

Deletes a secret. Tasks already assigned keep the value they were given; the next task that
references the name fails before it reaches a node.

## Secrets at rest

The server encrypts every secret with AES-256-GCM under a 32-byte master key, with the
secret's own name as additional authenticated data — so a ciphertext moved to another row
fails to decrypt rather than quietly becoming a different secret. Postgres holds ciphertext
and nonce and nothing else.

```sh
# Generate a key. --out writes it with mode 0600, which is what the server insists on.
podium-server gen-master-key --out /etc/podium/master.key

# Or to stdout, if you would rather place it yourself:
(umask 077; podium-server gen-master-key > /etc/podium/master.key)

PODIUM_MASTER_KEY_FILE=/etc/podium/master.key podium-server
```

- **The server refuses to start if the key file is readable by any other account.** A stray
  `chmod 644` is the likeliest way for a master key to leak.
- `PODIUM_MASTER_KEY` takes the key inline for development. The server warns loudly: an
  environment variable is visible in `/proc`, in `docker inspect` and in crash dumps.
- Without a key the server starts normally and secrets are simply unavailable — Podium is
  still a task runner without them.
- **There is no recovery path.** A lost master key is every secret encrypted under it lost
  with it. Back the file up somewhere that is not the control-plane machine.

### The object store

Artifacts and rolled-up logs live in an S3-compatible bucket, configured with the
`PODIUM_S3_*` environment (see the README). The bucket is created on start if it is missing.

- **Nodes never talk to it.** An artifact travels node → server → S3 over
  `NodeService.UploadArtifact`, so a worker needs neither a route to the object store nor a
  credential for one. That is the same invariant the whole networking design rests on.
- **The object store being down never stops a task.** `/readyz` reports 503, uploads fail
  with a retryable error event, and task creation, assignment and execution are untouched.
- **No `PODIUM_S3_ENDPOINT` is a supported deployment.** Podium is still a task runner
  without artifacts; uploads answer `FailedPrecondition` and logs simply stay in Postgres
  forever.
- Object keys are `tasks/<task_id>/artifacts/<artifact_id>-<sanitised name>` and
  `tasks/<task_id>/logs/<stream>.log.zst`. The original name is kept in the database; only
  the key is sanitised to `[A-Za-z0-9._-]`, at most 128 characters.

### Rotating the master key

```sh
podium-server gen-master-key --out /etc/podium/master.key.new
PODIUM_DATABASE_URL=… podium-server rotate-master-key \
    --old /etc/podium/master.key --new /etc/podium/master.key.new
# then point PODIUM_MASTER_KEY_FILE at the new file and restart
```

Every row is re-encrypted in one transaction, so the table is never half under one key and
half under the other, and the rows are locked for the duration so a concurrent `secret set`
waits rather than being clobbered. Afterwards the old key decrypts nothing; `podium secret
ls` shows the new key id on every row. Rotation re-encrypts a value, it does not change it,
so versions do not move.

### `podium artifacts TASK_ID`

Lists the files a task produced.

```
ID                              KIND   NAME              SIZE      TYPE              CREATED
art_01k2v…                      file   report.txt        1.2 KB    text/plain        3m ago
art_01k2w…                      file   shots/first.png   84.0 KB   image/png         3m ago
art_01k2x…                      log    stdout            9.1 KB    text/plain+zstd   1m ago
```

`kind` is `file` for anything the task produced and `log` for a rolled-up log stream. A
finished task's log moves out of Postgres and into the object store, so after the prune
horizon that `log` row *is* the task's log — `podium logs` reads it back transparently.

A task produces artifacts two ways, both from inside the container:

```sh
# 1. Anything under /workspace/.podium/artifacts is collected when the task exits. The
#    artifact's name is its path relative to that directory.
mkdir -p /workspace/.podium/artifacts
my-tool --report /workspace/.podium/artifacts/report.txt

# 2. Or hand one over mid-run, from any shell:
/podium/runner artifact add /tmp/shot.png --type image/png
/podium/runner artifact add /tmp/out.csv --name results.csv --type text/csv
```

An artifact is capped at **512 MB**. Anything larger, and anything the object store refuses,
becomes a retryable `error` event in the task's log and **does not fail the task**: a task
does not need artifacts to run, and a screenshot that was too big is not a reason to fail a
run that did what it was asked.

### `podium artifact get ARTIFACT_ID [-o FILE] [--via-server] [--url]`

Downloads one artifact.

```sh
podium artifact get art_01k2v…                 # writes ./report.txt
podium artifact get art_01k2v… -o /tmp/r.txt
podium artifact get art_01k2v… -o -  | less    # to stdout
podium artifact get art_01k2v… --url           # print a presigned URL, download nothing
```

By default the bytes are **proxied through the control plane** (`--via-server`, on), because
that is the one endpoint a client is guaranteed to be able to reach: the object store may
well have no route from your laptop, and under the tailnet transport it certainly does not.
`--via-server=false` asks for a presigned GET and fetches it straight from the object store,
which is faster and needs a route to it. A presigned URL is good for 15 minutes and carries
its own credential — the CLI sends no bearer token with it, because that would hand your API
token to a third party.

A rolled-up log downloads as the zstd-compressed object it is; `zstd -d` it, or use
`podium logs`, which decompresses for you.

### `podium node rekey NODE_ID`

Unbinds a node from the Tailscale device it enrolled from.

A node enrolled over the tailnet is pinned to one device, so a copied `identity.json` is useless
elsewhere. Rekey when the worker is genuinely rebuilt or replaced: the node keeps its ID, labels
and history, and the next connection binds it to whatever device it arrives from. Until it
reconnects the node key alone is enough, so rekey immediately before the move, not routinely.

### `podium version`

Prints the client's build and the control plane's, and warns when they are not the same.

```
$ podium version
podium v0.3.1 (a1b2c3d)
server v0.4.0 (e4f5g6h)
→ version skew: client v0.3.1, control plane v0.4.0. The wire is only guaranteed between
  matching releases; upgrade whichever is older.
```

The server line comes from `IdentityService.WhoAmI`, which is behind the same identity
middleware as everything else — so under the dev transport this command needs the token like any
other. It **degrades rather than failing**: with nothing configured, or a control plane that
cannot be reached, it prints the client's own build and says so on stderr, and still exits 0.
A control plane older than the `server_version` field prints
`server   (does not report a version)`.

The warning is a diagnostic, not a gate. Podium is `v0.x` and the wire is only guaranteed
between matching releases; see [operations.md](operations.md#upgrading) for the upgrade order.

---

## `podium-server` subcommands

`podium-server` is configured entirely by environment (see
[`deploy/.env.example`](../deploy/.env.example)), and has four subcommands. A bare
`podium-server` and `podium-server serve` both serve.

### `podium-server init [--dir DIR] [--transport dev|tailnet] [--tailnet SUFFIX] [--force]`

Turns a copied `deploy/` directory into a deployment. It writes two files and **never**
overwrites either:

| | |
|---|---|
| `master.key` | the AES-256 key every stored secret is encrypted under, mode 0600 |
| `.env` | the compose file's variables, with fresh random credentials, mode 0600 |

It generates the Postgres password, the dev token and the object-store secret. Everything it
does not set is documented in `.env.example`.

```sh
cd deploy
podium-server init
docker compose up -d --wait
```

With `--transport tailnet` the `.env` instead carries `PODIUM_TAILNET`, `TS_AUTHKEY` and
`PODIUM_NODE_TS_AUTHKEY` — the values only you can supply — and the command reports which
Tailscale prerequisites it can see:

```
$ podium-server init --transport tailnet --tailnet taila79bf6
wrote ./master.key (mode 0600, key 3f2a1b0c)
wrote ./.env (mode 0600)

Back up ./master.key. Losing it loses every secret encrypted under it.

Tailscale prerequisites:
  [x] PODIUM_TAILNET=taila79bf6 — the server will be https://podium.taila79bf6.ts.net
  [ ] TS_AUTHKEY is empty in .env. Generate two auth keys …
  [?] MagicDNS and HTTPS Certificates must both be ON for your tailnet …
  [?] Apply deploy/tailscale-acl.example.json to your Access Controls …
```

`[?]` means it cannot be checked from a shell, not that it is optional. Both are required.

### `podium-server gen-master-key [--out FILE]`

See [Secrets at rest](#secrets-at-rest).

### `podium-server rotate-master-key --old FILE --new FILE`

See [Rotating the master key](#rotating-the-master-key).

---

## `podium-node` subcommands

### `podium-node upgrade VERSION [flags]`

Downloads a released `podium-node`, verifies it, drains this node, swaps the binary and
restarts the service.

```sh
sudo podium-node upgrade v0.4.0
```

The order is not negotiable, and it is what makes a failed upgrade harmless:

1. fetch the release archive and `checksums.txt`;
2. **verify the SHA-256 and unpack to a staging file** — nothing on the machine has changed yet;
3. drain this node through the control plane and wait for its running tasks to finish;
4. replace the binary with an atomic `rename`;
5. restart the service;
6. undrain.

A failure at any step before step 4 leaves the running node untouched and removes the staged
file.

| flag | default | |
|---|---|---|
| `--drain` | `true` | `--drain=false` skips steps 3 and 6 |
| `--drain-timeout` | `15m` | give up rather than swap under a running task |
| `--dest` | this binary | which `podium-node` to replace |
| `--restart-command` | `systemctl restart podium-node` | empty skips the restart |
| `--base-url` | GitHub releases | an air-gapped mirror, or a test |
| `--config` | `/etc/podium/node.yaml` | where the server URL and credentials come from |

Draining needs a credential this machine holds: under the dev transport that is the shared
token from the node's own config. A tailnet node with its own embedded Tailscale device has
none to lend, so drain from the control plane instead and pass `--no-drain`:

```sh
podium node drain worker-3        # on the control plane
# wait for `podium nodes` to show 0 running
sudo podium-node upgrade v0.4.0 --drain=false
podium node undrain worker-3
```

**There is no `auto_upgrade`.** A worker that replaces its own binary without an operator
asking is a worker that can take a whole fleet down at 3am.

> **Partly verified.** There has never been a release, so this has never been run against two
> real published versions. What *has* been exercised, against a local release server and a live
> control plane: the download, the checksum verification (including a corrupted manifest being
> refused with the destination left untouched and no staged file left behind), the extraction,
> the atomic swap, and the drain → wait → swap → undrain sequence. What has **not** been
> exercised is `systemctl restart podium-node` — the build machine is macOS — and the tailnet
> path, where this machine has no credential to lend to the drain.
