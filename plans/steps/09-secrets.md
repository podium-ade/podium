# Step 09 — Secrets: encrypted store, resolution, injection, redaction

**Milestone:** M2 · **Depends on:** 07 · **Design ref:** §9 Secrets flow, §6.1 `server/secrets`

## Goal
Operators set named secrets once; tasks reference them by name; values travel only inside the `Assign`
message, live only in node memory and the task container, and never appear in the database as plaintext,
in logs, or in any API response.

## In scope

### Server — `internal/server/secrets`
- Master key: 32 bytes from `PODIUM_MASTER_KEY_FILE` (hex or raw; file mode must be `0600` or the server refuses to start) or
  `PODIUM_MASTER_KEY` env (dev only, warn loudly). `podium-server gen-master-key` subcommand writes a new one.
- `Encrypt(name, value) (ciphertext, nonce)` AES-256-GCM with a random 12-byte nonce and `name` as additional authenticated data
  (so a ciphertext cannot be re-labelled). `Decrypt` symmetric. Key versioning: store `key_id` (SHA-256 prefix of the key) in the
  `secrets` row — add migration `0002_secrets_key_id.sql`; `podium-server rotate-master-key --old FILE --new FILE` re-encrypts all rows in one tx.
- `Resolve(ctx, refs []spec.SecretRef) ([]ResolvedSecret, error)` — fails the whole task if any name is missing (`error{retryable:false}`
  before assignment; the task goes `failed` with `failure_reason: "missing secret <name>"`, never reaches a node).
- `SecretService`: `SetSecret` (upsert, version+1, audit `secret.set` with name+version only), `ListSecrets`, `DeleteSecret`.
  There is deliberately **no** read endpoint. Authorization: any authenticated user in this slice (RBAC later); record `actor`.
- Scheduler integration: the (naive or real) scheduler calls `Resolve` immediately before sending `Assign` and populates
  `resolved_secrets`. `RedactForLog` (step 02) must be used on every log of `Assign`.

### Node — injection (`internal/node/docker`)
- For each resolved secret: `target=env` → add `Key=value` to the task container env; `target=file` → write to
  `DataDir/tasks/<task_id>/secrets/<basename>` mode `0400` and bind-mount **that file** read-only to `Key` path (absolute) inside the
  container. Files are on the node's disk only for the task's lifetime and shredded (overwritten then removed) at teardown.
  (Alternative considered: write into the container via `CopyToContainer` onto the tmpfs mount from step 08 — do this instead **if**
  it works on Docker Desktop; it avoids touching node disk at all. Try it first, fall back to bind mounts, and document which won.)
- Sidecars never receive task secrets unless a sidecar spec lists its own `Secrets` (add the field; same mechanism).
- In-memory hygiene: keep values in `[]byte`, zero them after container start (env) / after copy (file); never `string`-convert them.

### Redaction (`internal/node/logs/redact.go`)
- Build an Aho-Corasick (or simple multi-pattern) matcher from all secret values ≥ 8 bytes plus their base64 and URL-encoded forms;
  replace occurrences in `log` chunks with `[redacted:<name>]` before batching. Chunk boundaries: keep a 256-byte tail from the previous
  chunk to catch straddling matches. Document clearly that this is best-effort.

### CLI
- `podium secret set NAME` (value from `--value`, `--from-file`, or stdin; `--value` warns about shell history),
  `podium secret ls`, `podium secret rm NAME`.
- `podium run --secret NAME[:env:KEY|:file:/path]` shorthand → `SecretRef`.

## Out of scope
- External providers (Vault/1Password/KMS) — define `type Provider interface { Resolve(ctx, name) ([]byte, error) }` and implement only
  `builtin`. RBAC over which tasks may use which secrets.

## Acceptance checklist
- [ ] `podium secret set GREETING` then `podium run --secret GREETING:env:GREETING --image alpine:3 -- sh -c 'echo $GREETING'` prints the value
      in the task output — and the stored `task_log_chunks` row contains `[redacted:GREETING]` instead (redaction proves the pipeline).
- [ ] `file` target: `--secret GREETING:file:/podium/secrets/greeting` → `cat` shows the value; `ls -l` shows `0400`/read-only; after teardown
      nothing remains under `DataDir/tasks/<id>`.
- [ ] Missing secret name → task `failed` before any node sees it; failure_reason names the secret.
- [ ] `psql -c 'select ciphertext from secrets'` never contains the plaintext (test grep over a hex dump); wrong master key → decrypt error, not garbage.
- [ ] Rotating the master key re-encrypts all rows; old key can no longer decrypt; values round-trip with the new key.
- [ ] `RedactForLog` test: an `Assign` with secrets logged at debug level produces no value bytes (grep the log output).
- [ ] Server refuses to start with a world-readable key file.

## Verification
```sh
make test-integration ./internal/server/secrets/... ./internal/node/...
./bin/podium-server gen-master-key > /tmp/mk && chmod 600 /tmp/mk
```

## Notes
- Use `crypto/aes` + `crypto/cipher` only; no third-party crypto.
- Audit rows: `secret.set`, `secret.delete`, `secret.resolve` (per task, names only).

## Hand-off notes
_(fill in when done)_
