# Step 12 — Scheduler, leases, heartbeats, reconciliation, drain

**Milestone:** M4 ✔ · **Depends on:** 08, 11 · **Design ref:** §6.1 scheduler, §7.3 local policies, §10 failure modes

## Goal
Replace the naive assigner with a correct scheduler: label/capacity matching, leases with expiry, node
health from heartbeats, requeue-or-lost policy, reconciliation on reconnect, drain for upgrades, and image
cache pruning on nodes. After this, killing nodes, unplugging networks, and filling disks all produce the
documented behaviour.

## In scope

### Server — `internal/server/scheduler` (replaces `naive.go`)
- Loop every 500ms (and immediately on `pg_notify('podium_tasks_queued')` from `CreateTask`):
  1. `ClaimQueuedTasks(limit 50)` inside a tx (`SKIP LOCKED`).
  2. For each task, eligible nodes = sessions with `status=online`, not draining, `labels ⊇ spec.Labels`,
     `freeSlots ≥ 1`, `freeCPU ≥ spec.Resources.CPU`, `freeMemMB ≥ spec.Resources.MemoryMB` (sidecar resources included).
     Choose the one with the most free slots (ties: least recently assigned). If none: leave queued, record `last_schedule_attempt`.
  3. `Resolve` secrets (step 09); on failure → `failed` with reason, skip.
  4. `AssignTask` (`queued→scheduled`, `lease_id`, `lease_expires_at = now + 2m`), decrement the session's free capacity, send `Assign`.
- Lease handling: `provisioning` event within 15s of Assign extends the lease to `now + spec.Timeout + 5m`; missing it → revoke:
  send `Cancel`, transition `scheduled→queued` (if `attempts < max_attempts`) else `failed{reason:"node did not accept assignment"}`.
- Timeout: `running` past `started_at + spec.Timeout` → send `Cancel`; on `exited` mark `failed{reason:"timeout"}`.
- Cancel requested by user while `scheduled|provisioning|running` → `Cancel`; final state `cancelled` on `exited`, or forced after 60s if the node is gone.

### Node health — `internal/server/nodes`
- Heartbeat watchdog every 5s: `last_heartbeat > 30s` → `unreachable`; `> 120s` → `offline` and **expire its leases**:
  for each task on that node in `scheduled|provisioning|running`: if `spec.RetryOnNodeLoss && attempts < max_attempts` → `queued`
  (new attempt), else → `lost{reason:"node <name> offline"}`. Emit a synthetic `error` event on the task so the log explains it.
- Reconciliation on `Hello`:
  - For each `running_task_ids` the node reports: if the server still has it leased to this node → resume (keep state, expect events);
    if the server has moved it (`queued` again, or `lost`, or assigned elsewhere) → send `Cancel` so the node tears it down.
  - For each task the server believes is on this node but the node did **not** report → mark `lost` immediately (container is gone).
- Capacity accounting lives in the session, refreshed from every heartbeat (the node is the source of truth for free slots).

### Drain and upgrades
- `NodeAdminService.DrainNode` → session `draining=true`, send `Drain`; node stops accepting, finishes running tasks, then exits 0 if
  started with `--exit-on-drain` (used by the installer/upgrade path). `UndrainNode` clears it. `DeleteNode` requires `offline` or drained.
- `podium node drain NAME`, `podium node undrain NAME`, `podium node rm NAME`.

### Node — `internal/node/reconcile`, `internal/node/docker`
- On startup, before `Hello`: `ListOwned()`; containers whose `podium.lease` is unknown to the node's local state file
  (`data_dir/tasks/*.json`, written at assignment) are torn down as orphans; known ones are re-attached: reopen the events socket
  listener (the runner reconnects? **No** — runner only dials once. Accept the gap: re-attach to Docker logs only and emit a
  `step{name:"node/reattached"}` marker; document that runner socket events are lost across a node restart).
- Image cache pruning (`internal/node/docker/prune.go`): every 5 min and when `disk_free` heartbeat value crosses the watermark:
  remove images not referenced by any running task, least-recently-used first (track last-use in `data_dir/images.json`), until
  below the low watermark (`high - 0.10`). Report `disk_free` and `free_slots = 0` while above high watermark.
- Node also enforces `max_tasks` itself: an `Assign` when full → `error{retryable:true, message:"node full"}` (server requeues).

### Store additions
- Migration `0005`: `tasks.last_schedule_attempt_at`, `tasks.cancel_requested_at`, `nodes.draining bool`, `nodes.ts_stable_id` if not done in 11.
- `pg_notify('podium_tasks_queued')` trigger on insert/update to `queued`.

## Out of scope
- Priorities beyond the integer sort; fair-share between users; bin-packing heuristics; multi-server coordination.

## Acceptance checklist (integration + e2e; use `PODIUM_TEST_FAST_TIMERS=1` to shrink timers 10×)
- [ ] Task with `labels:[browser]` and two nodes (one labeled) always lands on the labeled node; unlabeled node stays idle.
- [ ] Three tasks, one node with `max_tasks: 2` → two run, third waits `queued`, starts when one finishes.
- [ ] Resource fit: node advertises 4 CPU; task asking 8 CPU stays queued with `last_schedule_attempt_at` set and a clear reason in `podium task get`.
- [ ] Kill the node process (SIGKILL) mid-task, don't restart: after `offline`, `retry_on_node_loss:true` task is re-run elsewhere
      (attempt 2) and `false` task is `lost` with the explanatory event.
- [ ] Kill the node and restart it within 60s: task continues; logs have `node/reattached` marker; final state correct.
- [ ] Node reports a task the server has since `lost` → node receives `Cancel` and cleans it up (no orphan containers).
- [ ] Block the node's network for 60s (iptables in CI, or pause the server container) → no log gaps, no state change beyond `unreachable`→`online`.
- [ ] Assign not acknowledged in 15s (fake node ignores it) → task requeued, attempts=2; after `max_attempts` → `failed`.
- [ ] Timeout `10s` on `sleep 60` → cancelled by server, `failed{reason:timeout}` within 15s + grace.
- [ ] Drain: node with 1 running task accepts no new work, finishes, exits 0 with `--exit-on-drain`.
- [ ] Prune: fill the node's image cache past the watermark (pull several big images in a test) → images pruned, running task's image untouched.
- [ ] Naive scheduler file is deleted; `Scheduler` interface has one implementation.

## Verification
```sh
make test-integration ./internal/server/scheduler/... ./internal/server/nodes/... ./internal/node/...
make e2e   # includes the chaos scenarios above under PODIUM_TEST_FAST_TIMERS=1
```

## Notes
- All timer constants live in one `internal/server/scheduler/timing.go` struct so tests can shrink them.
- Never transition a task based solely on in-memory session state; always go through `TransitionTask` with `from` guards — races between
  heartbeat expiry and late events are expected and must be no-ops, not corruption.

## Hand-off notes
_(fill in when done)_
