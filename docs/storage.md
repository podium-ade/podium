# Storage

Podium keeps state in four places. Two of them are the only copy of something and must be backed
up; two are disposable but will bite you if they are slow or full.

| | what it holds | disposable? | I/O |
|---|---|---|---|
| **Postgres** | every task, event, log chunk, node, secret and audit row | **no** | write-heavy while tasks run |
| **Object store** | artifacts, and finished tasks' logs after roll-up | **no** | bursty |
| **Node `data_dir`** | node identity, per-task state, staged secrets, the extracted runner | mostly — but see below | heavy, small writes |
| **Docker image cache** | pulled images | yes | very heavy on a cold pull |

---

## Postgres

**Not disposable, and not something to run on the same machine as a worker.** Put it on real
storage with real backups.

### What grows, and how fast

| table | grows with | pruned? |
|---|---|---|
| `task_log_chunks` | every byte a task prints | **yes** — after roll-up, `PODIUM_LOG_CHUNK_GRACE` later |
| `task_events` | one row per lifecycle event, plus one per coalesced log batch | **no** |
| `tasks` | one row per submission | **no** |
| `artifacts` | one row per kept file, plus one per rolled-up stream | **no** |
| `nodes`, `secrets`, `users`, `enrollment_tokens` | tiny | no |
| `audit_log` | one row per secret set/delete/resolve/rotate | **no** |

`task_log_chunks` is the one that moves. The node coalesces a task's output into one event per
**100 ms or 64 KB**, whichever comes first, so a chatty task costs one row per 64 KB rather than
one per `write(2)`. Rough arithmetic: a task producing 10 MB of output is about 160 rows and
10 MB of `bytea`.

**Everything else grows for ever.** There is no task retention, no event retention and no
`DELETE` anywhere except the log-chunk prune. A busy deployment will need a retention job
somebody has to write; nobody has.

### Sizing

There is no benchmark. What is known:

- Two Postgres connections per server process are permanently **hijacked** out of the pool for
  `LISTEN` — one for the log fan-out, one for the scheduler's queue wake-up. A pool sized below
  about four will notice.
- The scheduler polls every 500 ms, and a `pg_notify` from a database trigger usually beats the
  poll. The watchdog sweeps every 5 s over every non-terminal task.
- Heartbeats are one small `UPDATE` per node per 10 s.

None of this is heavy. Log ingest is what will hurt first, and it is proportional to how much
your tasks print.

### Backup and restore

```sh
# Backup. Consistent without stopping anything: pg_dump takes a snapshot.
docker compose exec -T postgres pg_dump -U podium -Fc podium > podium-$(date -u +%Y%m%dT%H%M%SZ).dump

# Restore into an empty database.
docker compose exec -T postgres pg_restore -U podium -d podium --clean --if-exists < podium.dump
```

Two things to know before you rely on that:

1. **A dump without the master key is a dump of unreadable secrets.** The `secrets` table is
   ciphertext. Back up `master.key` too, somewhere that is not the control plane, and do not put
   the two in the same place if the point of the backup is surviving that place.
2. **A dump without the object store is a dump with dangling artifact rows.** Rolled-up logs and
   artifacts live in the bucket; the table only names them. Restoring Postgres alone gives you
   `podium artifacts` listings whose downloads 404.

Restoring is only ever a whole-deployment operation. There is no partial restore and no
point-in-time story beyond what your Postgres gives you.

---

## The object store

Any S3-compatible endpoint. `deploy/docker-compose.yml` runs MinIO; the client is `minio-go`
against the S3 API, path-style.

> **Proved against a real MinIO. Never against S3 itself.** Storing and listing ran end to end
> against a real MinIO server, including a zero-byte artifact and a PNG a browser task produced.
> The automated suite still uses an in-process endpoint (`internal/server/artifacts/fakes3`) that
> speaks the same API and verifies presigned signatures for real. **Still unproven:** multipart
> upload, bucket policies, a pre-existing bucket with the wrong permissions, TLS, lifecycle
> rules, AWS S3 proper, and a presign round trip against anything but the fake.

### Layout

```
tasks/<task_id>/artifacts/<artifact_id>-<sanitised name>     kind = file
tasks/<task_id>/logs/<stream>.log.zst                        kind = log
```

`<stream>` is `stdout`, `stderr` or `sidecar-<name>`. The artifact id is in the key so two files
called `report.txt` cannot collide; the name is sanitised to `[A-Za-z0-9._-]` so a bucket
browser is readable. **The original name is kept in the database and is what the UI shows.**

The row is written **after** the object. A row naming a missing object is a download that fails
for no visible reason; an unreferenced object costs only space. A failed row insert removes the
object again.

### Sizing

- **512 MB per artifact**, enforced on the node before it crosses the wire and again on the
  server.
- A rolled-up log is zstd-compressed, so its `size_bytes` is the *compressed* size and is not
  comparable with a file's.
- **Nothing ever deletes an artifact.** There is no `DeleteArtifact`, no retention policy and no
  lifecycle rule. The bucket grows without bound. If you need it not to, set a lifecycle rule on
  the bucket yourself — Podium will then hand out `NotFound` for expired rows, which is ugly but
  not dangerous.

### Backup

```sh
mc alias set podium http://127.0.0.1:9000 "$PODIUM_S3_ACCESS_KEY" "$PODIUM_S3_SECRET_KEY"
mc mirror --overwrite podium/podium ./podium-bucket-backup
```

The `miniodata` volume is **not a cache**. Once a finished task's chunks have been pruned out of
Postgres, the objects in that bucket are the only copy of its log.

### Running without one

Leaving `PODIUM_S3_ENDPOINT` empty is supported and does exactly what it says:

- artifacts are unavailable — a task that tries produces a non-fatal `error` event and still
  succeeds;
- log roll-up never runs, so **nothing is ever pruned** and `task_log_chunks` grows for ever;
- the UI's artifacts panel says the deployment has no object store, once, rather than per row.

The server does **not** refuse to start when the endpoint is unreachable. It probes the bucket
for 5 seconds, logs a loud warning and starts — refusing would make the object store a
dependency of running any task at all. `/readyz` returns 503 while it is unreachable.

---

## Log roll-up, and where the logs actually are

A task's output lives in Postgres while it is fresh and in the object store once it is not.

```
task prints  ──►  task_log_chunks (Postgres)  ──► roll-up ──►  <stream>.log.zst (bucket)
                            │                                          │
                            └──────── pruned, GRACE later ─────────────┘
```

| knob | default | what it does |
|---|---|---|
| `PODIUM_LOG_ROLLUP_INTERVAL` | `1m` | how often the sweep runs |
| `PODIUM_LOG_ROLLUP_SETTLE` | `30s` | how long after a task goes terminal before it is eligible |
| `PODIUM_LOG_PRUNE_INTERVAL` | `1h` | how often rolled-up chunks are deleted |
| `PODIUM_LOG_CHUNK_GRACE` | `24h` | how long chunks survive after roll-up — your "logs stay fast for N" knob |

Three properties worth knowing:

- **Roll-up is terminal-only.** A running task's chunks are never touched, and the prune is
  scoped by task (a join against `tasks.logs_rolled_up_at`), not by chunk age. That is what keeps
  it from truncating a long-running task's log out from under it.
- **Reading a pruned task back is transparent.** `podium logs` on an archived task serves the
  whole stream from the object store. What is lost is the **interleaving between streams**:
  stdout replays as one run and then stderr, because there is one object per stream and nothing
  records how they were braided together. Within a stream the order is exact.
- **A rolled-up log is decompressed on every read.** There is no cache and no range support, so
  `podium logs --from-seq N` on a large archived task reads and discards everything below N. It
  is a cold path for a task older than a day.

An unparseable duration is **ignored** rather than treated as zero: a typo must not silently
delete a day of retention.

---

## A worker's `data_dir`

Default `/var/lib/podium-node`. **Local disk, never a network share.**

```
<data_dir>/
  identity.json                     the node key — a credential, mode 0600, no recovery path
  images.json                       the image-cache LRU record, and the prune allow-list
  ts/                               the node's own Tailscale device state — also a credential
  runner/<arch>/podium-runner       extracted from the binary, bind-mounted into every task
  tasks/<task_id>/
    events.sock                     the runner's event socket
    node-state.json                 the seq/offset bookmark, a fallback for reconciliation
    secrets/NN-<name>               file-target secrets, 0444 inside a 0700 dir,
                                    shredded at teardown
```

It is I/O heavy in the way a socket and a lot of small writes are heavy, not in bytes. What
actually needs the fast disk is the **image cache**, which is Docker's, not Podium's.

Two things make it not-quite-disposable:

- `identity.json` is the node key. Lose it and the node needs a fresh enrollment token; the old
  node row lingers until somebody runs `podium node rm`.
- `ts/` is a Tailscale device identity. Lose it and the worker registers as a *new* device, and
  the admin console fills with ghosts.

Both are `0600`, both are credentials, and neither belongs in a git repository or copied between
machines.

### Path length

The runner's event socket lives at `<data_dir>/tasks/<task_id>/events.sock`. A Unix socket path
is capped at 108 bytes on Linux and 104 on macOS, so a deep `data_dir` makes the node fall back
to a private directory under `/tmp`. That works — but it is why the shipped systemd unit does
**not** set `PrivateTmp`: a private `/tmp` is invisible to `dockerd`, and the bind mount would
fail. Keep `data_dir` short.

---

## The image cache

Docker's, shared with everything else on the machine. Podium's rules about it are strict:

- **Pruning is off by default** (`image_cache_prune: false`). A node shares its engine with the
  rest of the machine's work, and no amount of disk pressure justifies deleting an image out
  from under it.
- **Podium only ever removes an image it pulled itself.** Every successful pull is recorded in
  `<data_dir>/images.json`, which is both the LRU bookkeeping and the allow-list. The one
  function in the tree that calls `ImageRemove` refuses anything absent from it, before the
  engine is called at all.
- **Podium never runs `docker system prune`, `docker image prune`, or any bulk removal**,
  anywhere.
- **A moving tag is never refreshed.** An image is pulled only when the engine says it is
  absent, so `alpine:3` stays whatever version was first cached on that node. Pin digests if that
  matters.

What the high watermark does with pruning **off** — the default — is make the node advertise
`free_slots = 0` while the `data_dir` filesystem is above `image_cache_high_watermark` (0.80).
A machine that cannot fit another image cannot reliably start another task, so it stops taking
them and says so rather than failing them.

Consequence: **a long-lived node accumulates images until an operator intervenes.** That is the
intended trade. Turning pruning on is a per-machine decision:

```yaml
# /etc/podium/node.yaml — only on a machine whose Docker engine is Podium's alone
image_cache_prune: true
image_cache_high_watermark: 0.80
```

---

## Placement, in one list

- **Postgres**: real storage, real backups, not on a worker.
- **The object store**: real storage, backed up, not a cache.
- **`master.key`**: backed up somewhere that is not the control plane. No escrow, no recovery.
- **A worker's `data_dir`**: local disk, short path, `0600`, never copied.
- **The server's `PODIUM_TS_STATE_DIR`**: must persist across restarts, or the MagicDNS name
  drifts to `podium-1`, `podium-2`, …
- **A worker's Docker image cache**: the fast disk. This is the one that fills up.
