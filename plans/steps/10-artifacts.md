# Step 10 — Artifacts and log roll-up (S3 / MinIO)

**Milestone:** M2 ✔ · **Depends on:** 07 · **Design ref:** §6.1 `server/artifacts`, `server/logs`; §3 host compose

## Goal
Tasks can produce files (screenshots, reports, recordings) that end up in an S3-compatible store and are
listable/downloadable through the API; completed task logs roll up from Postgres into the same store so the
hot tables stay small.

## In scope

### Server — `internal/server/artifacts`
- S3 client from `PODIUM_S3_*` env (`minio-go/v7`, path-style, bucket auto-created on start if missing; refuse to start if the
  endpoint is unreachable → `/readyz` 503).
- Object key layout: `tasks/<task_id>/artifacts/<artifact_id>-<sanitized name>` and `tasks/<task_id>/logs/<stream>.log.zst`.
- Presigned URLs: `PresignPut(key, contentType, maxSize, ttl=15m)` for uploads, `PresignGet(key, ttl=15m)` for downloads.
- `ArtifactService.ListArtifacts(task_id)`, `GetArtifactURL(artifact_id)` → presigned GET.
- Node upload path (no public S3 exposure required): the node receives artifacts from the runner and uploads them **through the server**
  via a new `NodeService.UploadArtifact(stream)` client-streaming RPC (chunks of 1 MB, max 512 MB per artifact, metadata first). The server
  writes to S3 and inserts the `artifacts` row, then the node emits `TaskEvent{kind:artifact, object_key}`. (Presigned PUT directly from the
  node is an optimization for later: it requires the node to reach S3, which breaks the "nodes only talk to the server" rule.)

### Runner → node (`internal/runner`, `internal/node`)
- Runner event `artifact{name, path, content_type}` (path inside the container). The node handles it by `CopyFromContainer(path)`
  (tar stream → single file) and uploading via `UploadArtifact`. Add a helper for adopters: `podium-runner artifact add <path> [--name] [--type]`
  (a subcommand that just writes the event to the socket; usable from any shell inside the task).
- Auto-collection: files placed under `/workspace/.podium/artifacts/` at task exit are uploaded automatically (name = relative path).

### Log roll-up — `internal/server/logs`
- On terminal status (`succeeded|failed|cancelled|lost`): a background job concatenates `task_log_chunks` per stream (stdout, stderr,
  each sidecar) into `logs/<stream>.log.zst` (zstd), records them as artifacts with `content_type: text/plain+zstd` and a `kind=log` marker
  (add `kind text default 'file'` to `artifacts` via migration `0003`), then deletes the chunks after a 24h grace (`PruneLogChunks`, hourly).
- `StreamTaskEvents` / `podium logs` for a finished task older than the grace period read from the rolled-up object transparently
  (server streams the decompressed object as `log` events with synthetic seq).

### CLI
- `podium artifacts ID` table; `podium artifact get ARTIFACT_ID [-o file]` (downloads via presigned URL — the CLI **may** need S3 reachability;
  add `--via-server` fallback that proxies through the API, default on).

## Out of scope
- Retention policies beyond the 24h chunk prune; GCS/AWS-specific auth (S3 API only); UI (13).

## Acceptance checklist
- [ ] Task writes `/workspace/.podium/artifacts/report.txt` → artifact row exists, `podium artifacts` lists it, `podium artifact get` downloads
      byte-identical content.
- [ ] `podium-runner artifact add /tmp/shot.png --type image/png` from inside the task → uploaded with correct content type.
- [ ] 600 MB artifact → rejected with a clear error event; task itself still succeeds.
- [ ] After a task finishes, within the roll-up interval `logs/stdout.log.zst` exists; after the prune horizon (test with 1s override) chunks
      are gone and `podium logs ID` still returns the full log.
- [ ] MinIO down → `/readyz` 503, task creation still works (queue) but assignment is paused? **No** — artifacts are not required for a task
      to run; only uploads fail (with `error{retryable:true}` events). Assert that behaviour.
- [ ] Compose file in `deploy/docker-compose.yml` now includes MinIO with a persistent volume and the server env wired.

## Verification
```sh
make test-integration ./internal/server/artifacts/... ./internal/server/logs/...
docker compose -f deploy/docker-compose.yml up -d minio && ./bin/podium run --image alpine:3 -- sh -c 'mkdir -p /workspace/.podium/artifacts; echo ok > /workspace/.podium/artifacts/r.txt'
```

## Notes
- Sanitize artifact names to `[A-Za-z0-9._-]`, max 128 chars; keep original name in the DB.
- Use `github.com/klauspost/compress/zstd`.

## Hand-off notes
_(fill in when done)_
