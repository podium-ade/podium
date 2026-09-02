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
- [x] `alpine:3` + `sh -c 'echo hi; echo err >&2; exit 3'` → events in order: provisioning, (pulling…), started, log(stdout "hi"), log(stderr "err"), exited(3), finished(3); `Result.ExitCode==3`. — `TestRunEmitsOrderedEventsAndExitCode`. The *relative* order of the stdout and stderr chunks is proven by `TestLogStreamsKeepTheirRelativeOrder` instead: the two writes in the literal command land in the same millisecond and the daemon's two log collectors are free to record them in either order, so that assertion would be a coin flip.
- [x] Seq is strictly increasing with no gaps across all events. — asserted in every integration test via `assertSeq`, incl. `TestRunSeqHasNoGapsUnderHeavyOutput` (804 events), plus `TestEmitterSeqIsOrderedUnderConcurrency` (1600 concurrent emits) under plain `make test` and `-race`.
- [x] Cancel during `sleep 60` → container gone within 35s, `exited` has signal-death exit code (143), no leaked network/volume. — `TestCancelEscalatesToSIGKILL`: gone in **31.1s** with exit **137**, not 143. With the runner trimmed for MVP-0 the task command is PID 1, and the kernel discards a default-disposition SIGTERM sent to a PID namespace's init, so `sleep` only dies when the 30s grace expires and SIGKILL lands. `TestCancelSignalsTheContainer` proves the SIGTERM itself arrives and is honoured (2.1s, exit 143) for a command that installs a TERM handler; step 04's runner is what restores 143 for every command.
- [ ] Memory: run `stress`-like alloc under a 64 MB limit **only if** limits are trivial to add now — otherwise leave to step 08 and note it. — deferred to step 08 (limits). OOM detection itself is wired (`State.OOMKilled` from inspect → `ExitedPayload.OOMKilled`, `Result.OOMKilled`), but nothing sets a memory limit yet so it cannot be exercised.
- [x] Nonexistent image → `error{retryable:true}`, `Run` returns error, nothing leaked (`docker network ls | grep podium-` empty). — `TestNonexistentImageFailsRetryablyAndLeaksNothing`; `TestEmptyImageFailsWithoutRetry` covers the `retryable:false` spec-error side.
- [x] After `Teardown`, `docker ps -a --filter label=podium.task=<id>` is empty and the workspace volume is gone; with `keepWorkspace` it remains. — `TestListOwnedAndTeardown`, which also covers `ListOwned` and a second, idempotent `Teardown`.
- [x] `New` fails fast with a clear message on cgroup v1 — `TestCheckEngine` (7 cases, no Docker needed, runs under plain `make test`). **Deviation:** the check reads `CgroupVersion` from the Docker API's `Info()` and the API version from the negotiated `ClientVersion()`, not from a `/sys/fs/cgroup` probe — on Docker Desktop the daemon runs in a Linux VM and the host has no `/sys/fs/cgroup` at all, so a host probe would be meaningless. `checkEngine(apiVersion string, info system.Info) error` is pure and is tested against fabricated `system.Info` values.
- [ ] Works on both Docker Desktop (macOS) and Linux Docker (CI) — the runner arch is selected from `Info().Architecture`. — **only Docker Desktop 29.4.3 / arm64 / API 1.54 / cgroup v2 was verified here**; Linux CI is unproven. The runner-arch half is moot: no runner in MVP-0 (step 04 skipped), so nothing reads `Info().Architecture`.

## Verification

`make runner-embed` does not exist and was dropped: there is no runner to embed in MVP-0 (step 04 skipped).

```sh
go test -tags integration ./internal/node/docker/... -count=1 -v   # 22 tests, all PASS
docker ps -a --filter label=podium.task -q | wc -l      # 0 after tests
docker network ls --filter name=podium- -q | wc -l      # 0
docker volume ls --filter name=podium-ws- -q | wc -l    # 0
make lint test build                                    # 0 issues, all green
```

## Notes
- Use `github.com/docker/docker/client` with `client.WithAPIVersionNegotiation()`.
- The events socket lives on the **host** (in `DataDir`) and is bind-mounted in; that is why `DataDir` must be on the same
  host as the Docker daemon — document that `DOCKER_HOST` pointing at a remote engine is unsupported.
- Never call `docker` CLI; API only.

## Hand-off notes

Unit D of the MVP-0 track, built to the trims in `00-index.md` (**no runner**: `Cmd = Spec.Command`, image
entrypoint kept, no events socket, no runner mount, no `Secrets`/`RegistryAuths`) plus the orchestrator's
cgroup-check ruling. Everything below is what **step 07** codes against.

### Files

```
internal/node/docker/events.go            Event, kind constants, payload types, the seq emitter
internal/node/docker/executor.go          Options, Executor, New, Close, checkEngine, naming, run registry
internal/node/docker/run.go               Request, Usage, Result, Run, image pull, log demux, stats
internal/node/docker/lifecycle.go         Cancel, ListOwned, Teardown, OwnedContainer
internal/node/docker/executor_test.go     unit tests (no Docker; run under plain `make test`)
internal/node/docker/integration_test.go  //go:build integration, real local Docker
```

Import path: `"github.com/alvaroibarguen/podium/internal/node/docker"`, package name `docker`. Step 07 needs no
direct dependency on the Docker SDK: everything it touches is in this package.

### Public API — exactly this

```go
type Options struct {
    DataDir    string        // required; must be on the same host as the daemon
    DockerHost string        // "" = FromEnv (DOCKER_HOST / docker context / default socket)
    Logger     *slog.Logger  // nil = slog.Default()
}
func New(ctx context.Context, opts Options) (*Executor, error)

type Request struct {
    TaskID  string
    LeaseID string
    Spec    spec.TaskSpec   // pkg/spec
}

type Event struct {
    Seq     uint64
    Kind    string
    TS      time.Time       // UTC
    Payload any
}

type Usage struct {
    CPUSeconds   float64
    PeakMemoryMB int64
    WallMS       int64
}
type Result struct {
    ExitCode  int
    OOMKilled bool
    Usage     Usage
}
type OwnedContainer struct {
    ID, Name, TaskID, LeaseID, Role, Image, State, Status string
    CreatedAt time.Time
}

func (e *Executor) Run(ctx context.Context, req Request, events chan<- Event) (Result, error)
func (e *Executor) Cancel(taskID string)                                            // idempotent, non-blocking
func (e *Executor) ListOwned(ctx context.Context) ([]OwnedContainer, error)
func (e *Executor) Teardown(ctx context.Context, taskID string, keepWorkspace bool) error
func (e *Executor) Close() error
```

### Event kinds and payload types

| `Event.Kind` (constant) | value | `Event.Payload` concrete type |
|---|---|---|
| `docker.KindProvisioning` | `"provisioning"` | `nil` |
| `docker.KindPulling` | `"pulling"` | `PullingPayload` |
| `docker.KindStarted` | `"started"` | `nil` |
| `docker.KindLog` | `"log"` | `LogPayload` |
| `docker.KindExited` | `"exited"` | `ExitedPayload` |
| `docker.KindFinished` | `"finished"` | `FinishedPayload` |
| `docker.KindError` | `"error"` | `ErrorPayload` |

```go
type LogPayload      struct { Stream string; Bytes []byte }   // Stream is StreamStdout|StreamStderr
type PullingPayload  struct { Image, Status, LayerID string; Current, Total int64 }
type ExitedPayload   struct { ExitCode int; OOMKilled bool }
type FinishedPayload struct { ExitCode int; Usage Usage }
type ErrorPayload    struct { Message string; Retryable bool }

const StreamStdout = "stdout"; StreamStderr = "stderr"
```

`provisioning` and `started` carry a **nil** payload — do not type-assert them. `LogPayload.Bytes` is a fresh
copy per event and is safe to keep. `PullingPayload.Current/Total` are 0 for status lines with no progress
detail. The kind strings match `podiumv1.TaskEventKind` one-for-one apart from `step`/`artifact`, which this
executor never emits. Nothing here emits `step`.

### Contract details step 07 must respect

- **Run does not close `events`** — the channel belongs to the caller. Drain it from a goroutine; `Run` blocks
  on a full channel until the consumer catches up or `ctx` is done. If `ctx` is cancelled, pending events are
  dropped (that is the only way a gap can appear).
- **`Seq` starts at 1 per `Run` call**, is assigned at emit time under a mutex that also covers the channel
  send, so the order the consumer sees *is* the sequence order. Verified with `-race`.
- **Guaranteed event order:** `provisioning` is always seq 1. Then zero or more `pulling`, then `started`, then
  zero or more `log`, then `exited`, then `finished` — `finished` is always last on a successful run. On a
  failure the last event is `error` and there is no `exited`/`finished`.
- **`Run` returns `(Result, nil)` for any container that produced an exit code**, including non-zero. A non-nil
  error means the task never ran; `Result` is then the zero value and an `error` event was already emitted.
- **`ErrorPayload.Retryable`** is `false` only for spec errors (currently: empty `Spec.Image`); every engine,
  pull, network, create or wait failure is `true`.
- **On the error path `Run` tears down its own partial resources** (on a `context.WithoutCancel` +60s context),
  so the caller does not have to. On the success path **the caller must call `Teardown`** — nothing else does.
- **`Cancel(taskID)` returns immediately** and is safe to call any number of times, before/during/after the run
  and for unknown task IDs. It does *not* cancel `ctx` and does *not* make `Run` return an error: the run
  finishes normally and reports the resulting exit code. Labelling the task `cancelled` is step 06/07's job.
- **Concurrent `Run` for the same `TaskID` is refused** with an error (and an `error` event) — one run per task
  ID at a time.
- `Teardown` is idempotent; missing resources are not an error. `keepWorkspace=true` keeps only the volume.
- `New` fails fast if the engine is unreachable, API < 1.43, or cgroup != v2.

### Container/resource shape (canonical names from 00-index)

| thing | name |
|---|---|
| container | `podium-<task_id>` |
| network | `podium-<task_id>` (bridge, `internal=false`, alias `task`) |
| volume | `podium-ws-<task_id>` → `/workspace` |
| labels | `podium.task=<task_id>`, `podium.lease=<lease_id>`, `podium.role=task` (exported as `docker.LabelTask`, `LabelLease`, `LabelRole`, `RoleTask`) |

Container config: `Cmd = Spec.Command`, **image entrypoint untouched**, `WorkingDir = Spec.WorkingDir` or
`spec.DefaultWorkingDir` when empty, `Env = sorted(Spec.Env) + PODIUM_TASK_ID + PODIUM_LEASE_ID + PODIUM_WORKDIR`.
Host config: `AutoRemove=false`, `RestartPolicy=no`, `Init=false`. No limits, no hardening, no tmpfs (08/09).

### DataDir layout

```
<DataDir>/tasks/<task_id>/      # created by Run, removed by Teardown
```

`<DataDir>/tasks` is created by `New` with mode 0700. The per-task directory is **empty in MVP-0** — it exists
because it is where step 04's `events.sock` and step 09's `/podium/secrets` tmpfs staging go, and it keeps the
teardown path already correct. `DataDir` must be on the same host as the Docker daemon; a remote `DOCKER_HOST`
is unsupported and is documented on `Options.DataDir`.

### Deviations, and why

1. **cgroup/API check reads the Docker API, not the host filesystem** (orchestrator ruling). `checkEngine(
   apiVersion string, info system.Info) error` takes the negotiated `cli.ClientVersion()` and `cli.Info()`. On
   Docker Desktop the daemon lives in a Linux VM and the macOS host has no `/sys/fs/cgroup`, so a host probe
   would test nothing. Unit-tested against fabricated `system.Info`.
2. **`github.com/pkg/errors v0.9.1` was added with `go get`** (never `go mod tidy`). `docker/docker`'s client
   package imports it from `client/build_prune.go`; the pre-seeded `go.mod` did not have it and the package
   would not compile. This is the only module this unit added.
3. **`Cancel` on `sleep 60` ends in SIGKILL/137, not SIGTERM/143.** See the checklist note: no runner ⇒ the task
   command is PID 1 ⇒ the kernel drops a default-disposition SIGTERM. Every cancel of a command that ignores
   SIGTERM therefore costs the full 30s grace. Step 07 should not shorten its own deadlines assuming 143.
4. **`ContainerWait` uses `WaitConditionNextExit`, not `WaitConditionNotRunning`.** This bit hard: the wait is
   subscribed *before* `ContainerStart` (so an instantly-exiting container is not missed), and a container in
   state `created` is already "not running", so `NotRunning` returns `StatusCode: 0` immediately and every task
   looks successful. Do not "simplify" this back.
5. **Pull progress is decoded with `encoding/json` directly**, not `pkg/jsonmessage`, to avoid dragging
   `moby/term` and friends into the node binary. Throttled to one event per second; the first message always
   emits, so a fast pull yields exactly one `pulling` event.
6. **The image is pulled only when `ImageInspect` says it is absent** (step file: "skip if digest present
   locally"). A moving tag such as `:latest` is therefore never refreshed once cached. Image cache policy is
   step 12's.
7. **No timeout enforcement.** `Spec.Timeout` is ignored by the executor; per §7.2/§10 the *server* turns a
   timeout into a `Cancel`. Step 07/06 owns that.
8. **Usage is best-effort and sampled** (`ContainerStatsOneShot`, immediately then every 2s), so a task shorter
   than one sample reports `CPUSeconds: 0, PeakMemoryMB: 0`. `WallMS` is always real (measured around
   `ContainerStart`→`ContainerWait`). Memory subtracts `inactive_file` the way `docker stats` does on cgroup v2.
9. `make runner-embed` dropped from Verification — the target does not exist and there is nothing to embed.

### Gotchas worth remembering

- `HostConfig.Init` is a field on `HostConfig` itself, **not** on the embedded `container.Resources`.
- Cross-stream ordering of stdout vs stderr writes made in the same millisecond is not deterministic: the
  daemon collects the two pipes with separate goroutines. Ordering *within* a stream is exact.
- `container.Summary.Names[0]` comes back with a leading `/`; `ListOwned` strips it.
- Use `cerrdefs.IsNotFound` (`github.com/containerd/errdefs`) for not-found checks; `client.IsErrNotFound` is
  deprecated and staticcheck flags it.
- Integration tests leave the engine clean; `TestPullEmitsThrottledPullingEvents` deliberately removes and
  re-pulls `alpine:3.21` (and removes it again afterwards) so a cold pull is guaranteed. `alpine:3` is assumed
  present or is pulled on first use.
- `internal/node/docker` is only lint-checked without the `integration` tag (`.golangci.yml` sets no
  `build-tags`); `go vet -tags integration ./internal/node/docker/...` is clean and was run by hand.

### Open problems

- Only Docker Desktop 29.4.3 on darwin/arm64 (API 1.54, cgroup v2, storage driver `overlayfs`) was exercised.
  Linux CI is unverified, and CI has no `make test-integration` job that provides a Docker engine yet.
- `TestCancelEscalatesToSIGKILL` costs 31s of wall time on its own; the whole integration package takes ~40s.
- `go.mod`/`go.sum` were committed with unit C's `jackc/puddle/v2` and `golang.org/x/sync` lines in them —
  unavoidable in a shared tree. The orchestrator's post-merge `go mod tidy` is what makes the `// indirect`
  markers honest.
