# Step 05 — Node Docker executor (single container, no server)

**Milestone:** M1 · **Depends on:** 04 · **Design ref:** §7.2 Executing an assignment, §8 topology

## Goal
Implement `internal/node/docker`: given an `Assign`-shaped request, run the task container with the
embedded runner, stream ordered events to a Go channel, and tear everything down. Tested directly
against a local Docker engine; no network protocol involved yet.

## In scope

### Public API (`internal/node/docker`)
```go
type Executor struct { /* docker client, runner bin path, data dir, logger */ }
func New(ctx, Options{DataDir string, DockerHost string}) (*Executor, error)   // verifies API ≥ 1.43, cgroup v2, extracts embedded runner to DataDir/runner/<arch>
type Request struct { TaskID, LeaseID string; Spec spec.TaskSpec; Secrets []ResolvedSecret; RegistryAuths []RegistryAuth }
type Event struct { Seq uint64; Kind string; TS time.Time; Payload any }   // mirrors proto TaskEvent kinds
func (e *Executor) Run(ctx context.Context, req Request, events chan<- Event) (Result, error)
func (e *Executor) Cancel(taskID string)                                      // idempotent; SIGTERM → 30s → SIGKILL
func (e *Executor) ListOwned(ctx) ([]OwnedContainer, error)                   // containers with label podium.task (for step 12 reconciliation)
func (e *Executor) Teardown(ctx, taskID string, keepWorkspace bool) error
type Result struct { ExitCode int; OOMKilled bool; Usage Usage; }
```

### Run() sequence (this step: task container only; sidecars/secrets/limits arrive in 08/09)
1. Emit `provisioning` (seq 1).
2. Pull `Spec.Image` if not present by digest/tag (`ImagePull` with progress → `pulling` events, throttled to 1/s).
   Use `RegistryAuths` matching the image host if provided.
3. Create network `podium-<task_id>` (bridge, `internal=false`, labels).
4. Create volume `podium-ws-<task_id>` (labels).
5. Create container:
   - image, `Cmd = ["/podium/runner", "--"] + Spec.Command`, `Entrypoint` cleared, `WorkingDir` = Spec.WorkingDir,
     `Env` = Spec.Env + `PODIUM_TASK_ID`, `PODIUM_LEASE_ID`, `PODIUM_EVENTS_SOCK=/podium/events.sock`, `PODIUM_WORKDIR`.
   - Mounts: volume → `/workspace`; bind `DataDir/runner/<arch>/podium-runner` → `/podium/runner` **read-only**;
     bind `DataDir/tasks/<task_id>/events.sock` (a Unix socket the executor listens on) → `/podium/events.sock`.
   - Labels: `podium.task`, `podium.lease`, `podium.role=task`. Network: the task network, alias `task`.
   - Host config now: `AutoRemove=false` (we remove explicitly), `RestartPolicy=no`, `Init=false`.
6. Start listening on the events socket **before** `ContainerStart`; accept one connection; parse JSON lines into
   `step`-kind events (runner `started`/`exited` map to `started`/`exited` kinds).
7. `ContainerAttach`/`ContainerLogs` with follow, demultiplex stdout/stderr → `log` events with `stream`.
   Chunk at 64 KB max; batching/flush timing is the daemon's job (step 07), the executor just emits.
8. `ContainerWait`; read `State.OOMKilled`; emit `exited{exit_code, oom_killed}`.
9. Collect usage: wall time; CPU seconds and peak memory from `ContainerStats` sampled every 2s during run (best-effort).
10. Emit `finished{exit_code, usage}`. Return.
11. `Teardown` (called by the caller, not inside Run): remove container(s) on the network (force), remove network,
    remove volume unless `keepWorkspace`, remove `DataDir/tasks/<task_id>`.

Seq numbering: a per-task `atomic.Uint64`, starting at 1, assigned at emit time so log and socket events interleave in true order.

### Cancel semantics
- `Cancel` sends SIGTERM to the container (`ContainerKill` with `SIGTERM`), waits up to 30s, then `SIGKILL`.
  `Run` observes the exit as normal and reports `exited`; the *caller* labels the task `cancelled` (the executor doesn't know why).

### Errors
- Image pull failure, create failure → `error{message, retryable}` event then `Run` returns the error. `retryable=true`
  for pull/network errors, `false` for spec errors (e.g. bad command). Partial resources are torn down.

## Out of scope
- Sidecars, readiness, resource limits, hardening flags, secret injection, image cache pruning (08/09/12).
- Talking to the server (07).

## Acceptance checklist (integration tests against real Docker)
- [ ] `alpine:3` + `sh -c 'echo hi; echo err >&2; exit 3'` → events in order: provisioning, (pulling…), started, log(stdout "hi"), log(stderr "err"), exited(3), finished(3); `Result.ExitCode==3`.
- [ ] Seq is strictly increasing with no gaps across all events.
- [ ] Cancel during `sleep 60` → container gone within 35s, `exited` has signal-death exit code (143), no leaked network/volume.
- [ ] Memory: run `stress`-like alloc under a 64 MB limit **only if** limits are trivial to add now — otherwise leave to step 08 and note it.
- [ ] Nonexistent image → `error{retryable:true}`, `Run` returns error, nothing leaked (`docker network ls | grep podium-` empty).
- [ ] After `Teardown`, `docker ps -a --filter label=podium.task=<id>` is empty and the workspace volume is gone; with `keepWorkspace` it remains.
- [ ] `New` fails fast with a clear message on cgroup v1 (unit-test the check with a fake `/sys/fs/cgroup` probe).
- [ ] Works on both Docker Desktop (macOS) and Linux Docker (CI) — the runner arch is selected from `Info().Architecture`.

## Verification
```sh
make runner-embed && make test-integration ./internal/node/docker/...
docker ps -a --filter label=podium.task -q | wc -l      # 0 after tests
docker network ls --filter name=podium- -q | wc -l     # 0
```

## Notes
- Use `github.com/docker/docker/client` with `client.WithAPIVersionNegotiation()`.
- The events socket lives on the **host** (in `DataDir`) and is bind-mounted in; that is why `DataDir` must be on the same
  host as the Docker daemon — document that `DOCKER_HOST` pointing at a remote engine is unsupported.
- Never call `docker` CLI; API only.

## Hand-off notes
_(fill in when done)_
