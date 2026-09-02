# Step 02 — Wire contract: protobuf services and `pkg/spec`

**Milestone:** M0 · **Depends on:** 01 · **Design ref:** §5 Node ↔ server protocol, §6.2 Schema

## Goal
Define the complete node↔server and client↔server contract as protobuf + Connect RPC, and the public Go
types for task/environment specs. Everything after this step codes against these types.

## In scope

### `pkg/spec` (public Go package, YAML/JSON tags, validation)
```go
type TaskSpec struct {
    Image       string            `yaml:"image"       json:"image"`
    Command     []string          `yaml:"command"     json:"command"`
    WorkingDir  string            `yaml:"working_dir" json:"working_dir"`   // default /workspace
    Env         map[string]string `yaml:"env"         json:"env"`
    Secrets     []SecretRef       `yaml:"secrets"     json:"secrets"`
    Sidecars    map[string]Sidecar`yaml:"sidecars"    json:"sidecars"`
    Resources   Resources         `yaml:"resources"   json:"resources"`
    Labels      []string          `yaml:"labels"      json:"labels"`        // required node labels
    Timeout     Duration          `yaml:"timeout"     json:"timeout"`       // default 1h
    MaxAttempts int               `yaml:"max_attempts" json:"max_attempts"` // default 1
    RetryOnNodeLoss bool          `yaml:"retry_on_node_loss" json:"retry_on_node_loss"`
    KeepWorkspace bool            `yaml:"keep_workspace" json:"keep_workspace"`
    Hardening   Hardening         `yaml:"hardening"   json:"hardening"`
}
type SecretRef struct { Name string; Target string /* "env"|"file" */; Key string /* env var name or file path */ }
type Sidecar   struct { Image string; Command []string; Env map[string]string; Readiness Readiness }
type Readiness struct { TCPPort int; HTTPPath string; HTTPPort int; Command []string; Timeout Duration /* default 60s */ }
type Resources struct { CPU float64 /* cores */; MemoryMB int; PIDs int /* default 4096 */ }
type Hardening struct { ReadOnlyRootfs bool; Capabilities []string /* added back; default none */ }
```
- `Duration` wraps `time.Duration` with YAML/JSON string parsing (`"30s"`, `"1h"`).
- `func (s *TaskSpec) Validate() error` and `func (s *TaskSpec) ApplyDefaults()`.
- `func ParseTaskSpec(r io.Reader) (*TaskSpec, error)` for YAML.

### `proto/podium/v1/`
- `common.proto`: `TaskStatus`, `NodeStatus` enums (canonical values), `TaskSpec` message mirroring `pkg/spec`
  (plus `ToProto`/`FromProto` in `pkg/spec/proto.go`), `Resources`, `Sidecar`, `Readiness`, `SecretRef`.
- `node.proto`:
  - `service NodeService { rpc Enroll(EnrollRequest) returns (EnrollResponse); rpc Stream(stream NodeMessage) returns (stream ServerMessage); }`
  - `NodeMessage = oneof { Hello, Heartbeat, TaskEvent }`
  - `ServerMessage = oneof { Assign, Ack, Cancel, Drain }`
  - `EnrollRequest{token, hostname, arch, os, cpu_cores, memory_mb, docker_version, labels[]}` → `EnrollResponse{node_id, node_key}`
  - `Hello{node_id, node_key, labels[], capacity{max_tasks, cpu_cores, memory_mb}, running_task_ids[], version}`
  - `Heartbeat{load{running_tasks, cpu_pct, mem_pct}, free_slots, disk_free_bytes, ts}`
  - `Assign{task_id, lease_id, spec TaskSpec, resolved_secrets[]{name, target, key, value bytes}, registry_auths[]{host, username, password}, deadline}`
  - `TaskEvent{task_id, lease_id, seq uint64, ts, kind enum, oneof payload { LogChunk{stream enum{stdout,stderr,sidecar}, sidecar_name, bytes}, Step{name, status, exit_code}, Artifact{name, content_type, size, object_key}, Exited{exit_code, oom_killed}, Finished{exit_code, usage{cpu_seconds, peak_memory_mb, wall_ms}}, Error{message, retryable} } }`
  - `Ack{task_id, seq}` · `Cancel{task_id, reason}` · `Drain{}`
- `task.proto`: `service TaskService { CreateTask(spec, priority) → Task; GetTask; ListTasks(filter, page); CancelTask; StreamTaskEvents(task_id, from_seq) → stream TaskEvent; }` and the `Task` message
  (`id, spec, status, node_id, attempts, created_at, started_at, finished_at, exit_code, usage, requested_by`).
- `admin.proto`: `service NodeAdminService { ListNodes; GetNode; CreateEnrollmentToken(labels[], ttl) → {token}; DrainNode; DeleteNode; }`.
- `secret.proto`: `service SecretService { SetSecret(name, value) ; ListSecrets → {name, version, updated_at}[]; DeleteSecret; }` — **no Get**.
- `artifact.proto`: `service ArtifactService { ListArtifacts(task_id); GetArtifactURL(artifact_id) → {url, expires_at}; }`.
- `buf generate` → `internal/proto/podium/v1/` (`*.pb.go`, `*connect/*.connect.go`). Generated code is committed.

### Docs
- `docs/protocol.md`: one page describing the stream lifecycle (from design §5) and the ordering/ack semantics
  of `TaskEvent.seq` — nodes keep a replay buffer until `Ack.seq`; server dedupes on `(task_id, seq)`.

## Out of scope
- Any server/node implementation. Auth headers (step 06/11) — but reserve: identity is transport-derived, not in messages.

## Acceptance checklist
- [x] `buf lint` and `buf breaking --against '.git#branch=main'` configured (breaking check may be a no-op now).
      `make proto-lint` / `make proto-breaking`; both also run in the new `proto` CI job.
- [x] `make proto` regenerates identical output (idempotent, committed).
- [x] `pkg/spec` tests: YAML round-trip, defaults applied, validation rejects empty image, zero/negative timeout,
      `max_attempts < 1`, empty env keys and env keys that are not valid shell identifiers.
- [ ] Validation rejects a bad secret target, negative resources, a sidecar name that is not a valid hostname label
      (`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`) or the reserved sidecar name `task` — trimmed for MVP-0
      (`SecretRef`, `Sidecar`, `Readiness`, `Resources` and `Hardening` do not exist in the trimmed spec).
- [x] `ToProto`/`FromProto` round-trip test for `TaskSpec`.
- [x] `docs/protocol.md` exists and matches the proto.

## Verification
```sh
make proto && git status --porcelain internal/proto | wc -l   # 0 after commit → idempotent
buf lint
go test ./pkg/spec/...
```

## Notes
- `resolved_secrets.value` is `bytes` and must be marked with a comment `// SENSITIVE: never log`. Add a helper
  `RedactForLog(*Assign) *Assign` in `internal/proto/…/redact.go` used by every log statement that touches Assign.
- Prefix IDs in messages as strings (`task_…`), never raw integers.

## Hand-off notes

Unit B of the MVP-0 track, built to the trims in `00-index.md` plus the orchestrator rulings. Everything
below is what units C, D, E, F and G code against.

### What exists

```
proto/podium/v1/{common,node,task,admin}.proto
internal/proto/podium/v1/{common,node,task,admin}.pb.go        # package podiumv1
internal/proto/podium/v1/redact.go                             # hand-written, same package
internal/proto/podium/v1/podiumv1connect/{node,task,admin}.connect.go   # package podiumv1connect
pkg/spec/{spec,duration,proto}.go
internal/ids/ids.go
docs/protocol.md
```

`common.proto` declares no service, so there is no `common.connect.go` — that is expected.

### Go import paths

```go
podiumv1 "github.com/alvaroibarguen/podium/internal/proto/podium/v1"
"github.com/alvaroibarguen/podium/internal/proto/podium/v1/podiumv1connect"
"github.com/alvaroibarguen/podium/pkg/spec"
"github.com/alvaroibarguen/podium/internal/ids"
```

### Connect constructors (exact names)

| | client | handler | handler interface |
|---|---|---|---|
| NodeService | `podiumv1connect.NewNodeServiceClient(httpClient, baseURL, opts...)` | `podiumv1connect.NewNodeServiceHandler(svc, opts...) (path string, h http.Handler)` | `podiumv1connect.NodeServiceHandler` |
| TaskService | `podiumv1connect.NewTaskServiceClient(...)` | `podiumv1connect.NewTaskServiceHandler(...)` | `podiumv1connect.TaskServiceHandler` |
| NodeAdminService | `podiumv1connect.NewNodeAdminServiceClient(...)` | `podiumv1connect.NewNodeAdminServiceHandler(...)` | `podiumv1connect.NodeAdminServiceHandler` |

Handler method signatures (implement these verbatim):

```go
Enroll(context.Context, *connect.Request[podiumv1.EnrollRequest]) (*connect.Response[podiumv1.EnrollResponse], error)
Stream(context.Context, *connect.BidiStream[podiumv1.NodeMessage, podiumv1.ServerMessage]) error

CreateTask(context.Context, *connect.Request[podiumv1.CreateTaskRequest]) (*connect.Response[podiumv1.CreateTaskResponse], error)
GetTask(context.Context, *connect.Request[podiumv1.GetTaskRequest]) (*connect.Response[podiumv1.GetTaskResponse], error)
ListTasks(context.Context, *connect.Request[podiumv1.ListTasksRequest]) (*connect.Response[podiumv1.ListTasksResponse], error)
CancelTask(context.Context, *connect.Request[podiumv1.CancelTaskRequest]) (*connect.Response[podiumv1.CancelTaskResponse], error)
StreamTaskEvents(context.Context, *connect.Request[podiumv1.StreamTaskEventsRequest], *connect.ServerStream[podiumv1.TaskEvent]) error

CreateEnrollmentToken(context.Context, *connect.Request[podiumv1.CreateEnrollmentTokenRequest]) (*connect.Response[podiumv1.CreateEnrollmentTokenResponse], error)
ListNodes(context.Context, *connect.Request[podiumv1.ListNodesRequest]) (*connect.Response[podiumv1.ListNodesResponse], error)
```

Client side of the bidi stream: `client.Stream(ctx) *connect.BidiStreamForClient[podiumv1.NodeMessage, podiumv1.ServerMessage]`
(`.Send(*NodeMessage)`, `.Receive() (*ServerMessage, error)`, `.CloseRequest()`, `.CloseResponse()`).
Server-streaming client: `client.StreamTaskEvents(ctx, req) (*connect.ServerStreamForClient[podiumv1.TaskEvent], error)`.

Route constants for the mux and for `curl`: `podiumv1connect.TaskServiceName == "podium.v1.TaskService"`,
`podiumv1connect.TaskServiceCreateTaskProcedure == "/podium.v1.TaskService/CreateTask"`, and the same pattern for
every other RPC (`NodeServiceEnrollProcedure`, `NodeServiceStreamProcedure`, `NodeAdminServiceListNodesProcedure`, …).

### Oneof accessors (the bit that is easy to get wrong)

`NodeMessage` and `ServerMessage` both name their oneof `msg`, so the generated wrapper types are:

```go
&podiumv1.NodeMessage{Msg: &podiumv1.NodeMessage_Hello{Hello: h}}
&podiumv1.NodeMessage{Msg: &podiumv1.NodeMessage_Heartbeat{Heartbeat: hb}}
&podiumv1.NodeMessage{Msg: &podiumv1.NodeMessage_TaskEvent{TaskEvent: ev}}

&podiumv1.ServerMessage{Msg: &podiumv1.ServerMessage_Assign{Assign: a}}
&podiumv1.ServerMessage{Msg: &podiumv1.ServerMessage_Ack{Ack: ack}}
&podiumv1.ServerMessage{Msg: &podiumv1.ServerMessage_Cancel{Cancel: c}}
&podiumv1.ServerMessage{Msg: &podiumv1.ServerMessage_Drain{Drain: &podiumv1.Drain{}}}
```

Read them with the typed getters — `m.GetHello()`, `m.GetHeartbeat()`, `m.GetTaskEvent()`, `m.GetAssign()`,
`m.GetAck()`, `m.GetCancel()`, `m.GetDrain()` — each returns nil when a different arm is set. Switch on
`m.GetMsg().(type)` when you need exhaustiveness.

`TaskEvent`'s oneof is named `payload`: `TaskEvent_Log{Log: *LogChunk}`, `TaskEvent_Step{Step: *Step}`,
`TaskEvent_Exited{Exited: *Exited}`, `TaskEvent_Finished{Finished: *Finished}`, `TaskEvent_Error{Error: *Error}`,
with getters `GetLog()`, `GetStep()`, `GetExited()`, `GetFinished()`, `GetError()`. **`kind` is a separate field**
and is authoritative: `provisioning`, `pulling` and `started` carry no payload at all.

### Enum constants

```go
podiumv1.TaskStatus_TASK_STATUS_{UNSPECIFIED,QUEUED,SCHEDULED,PROVISIONING,RUNNING,SUCCEEDED,FAILED,CANCELLED,LOST}
podiumv1.NodeStatus_NODE_STATUS_{UNSPECIFIED,ONLINE,UNREACHABLE,OFFLINE,DRAINING}
podiumv1.TaskEventKind_TASK_EVENT_KIND_{UNSPECIFIED,PROVISIONING,PULLING,STARTED,LOG,STEP,ARTIFACT,EXITED,FINISHED,ERROR}
podiumv1.LogChunk_STREAM_{UNSPECIFIED,STDOUT,STDERR,SIDECAR}   // nested enum type podiumv1.LogChunk_Stream
```

`STEP` and `ARTIFACT` stay in the enum because the value list is contractual, but nothing emits them in MVP-0 and
there is deliberately **no `Artifact` payload message**. `Step` does exist as a message. Documented in `docs/protocol.md`.

### `pkg/spec`

```go
type TaskSpec struct {
    Image       string            `yaml:"image" json:"image"`
    Command     []string          `yaml:"command,omitempty" json:"command,omitempty"`
    WorkingDir  string            `yaml:"working_dir,omitempty" json:"working_dir,omitempty"`
    Env         map[string]string `yaml:"env,omitempty" json:"env,omitempty"`
    Labels      []string          `yaml:"labels,omitempty" json:"labels,omitempty"`
    Timeout     Duration          `yaml:"timeout,omitempty" json:"timeout,omitempty"`
    MaxAttempts int               `yaml:"max_attempts,omitempty" json:"max_attempts,omitempty"`
}

func (s *TaskSpec) ApplyDefaults()
func (s *TaskSpec) Validate() error
func ParseTaskSpec(r io.Reader) (*TaskSpec, error)
func (s *TaskSpec) ToProto() *podiumv1.TaskSpec       // method on the Go type
func FromProto(p *podiumv1.TaskSpec) *TaskSpec        // package-level function

type Duration time.Duration
func (d Duration) Std() time.Duration                 // use this to get a time.Duration
const DefaultWorkingDir = "/workspace"; DefaultTimeout = time.Hour; DefaultMaxAttempts = 1
```

Conversion is spelled `s.ToProto()` / `spec.FromProto(p)`. Both are nil-safe and deep-copy slices and maps.

Behaviour worth knowing:
- `ApplyDefaults` only fills **zero** values. It deliberately does not repair a negative timeout or a negative
  `MaxAttempts`, so `Validate` still catches them. Call `ApplyDefaults` then `Validate`.
- `Validate` returns an `errors.Join` of **every** problem, with env keys sorted so the message is deterministic.
  It rejects: empty/blank image, `timeout <= 0`, `MaxAttempts < 1`, empty env key, env key not matching
  `^[A-Za-z_][A-Za-z0-9_]*$`, and blank labels.
- `ParseTaskSpec` decodes YAML with `KnownFields(true)` (unknown keys are an error — this is what makes
  `sidecars:`/`secrets:` fail loudly in MVP-0), then applies defaults, then validates. Step 06's `CreateTask`
  should call `ApplyDefaults` + `Validate` on the spec it decoded from the proto, not `ParseTaskSpec`.
- `Duration` marshals to/from `"1m30s"` in both YAML and JSON. YAML lib is **`go.yaml.in/yaml/v3`**, the
  maintained home of `gopkg.in/yaml.v3` — it was already in the module graph. Do not add `gopkg.in/yaml.v3`.
- The trimmed types (`SecretRef`, `Sidecar`, `Readiness`, `Resources`, `Hardening`) and the fields
  `Secrets`, `Sidecars`, `Resources`, `Hardening`, `RetryOnNodeLoss`, `KeepWorkspace` **do not exist**.
  `keepWorkspace` returns in unit D only as a parameter on the executor's `Teardown`.

### `internal/ids` (added by orchestrator decision — do not invent your own)

```go
ids.New(prefix string) string   // "<prefix>_<26-char lowercase ULID>"
ids.NewTask() string            // task_01j…
ids.NewNode() string            // node_01j…
ids.NewLease() string           // lease_01j…
```

Monotonic within a millisecond, mutex-guarded, `crypto/rand` entropy. Sequential IDs sort lexicographically,
which is what makes them usable as a `ListTasks` cursor. `github.com/oklog/ulid/v2 v2.1.2`.

### `RedactForLog`

`podiumv1.RedactForLog(*podiumv1.Assign) *podiumv1.Assign`, in `internal/proto/podium/v1/redact.go` —
**hand-written, in the generated package**. `buf generate` only writes the files it produces, so it does not
clobber it; verified by running `make proto` twice. If anyone ever adds `clean: true` to `buf.gen.yaml`, move this
file first. Today it is a defensive `proto.Clone`; it becomes load-bearing in step 09 when `Assign` gains
`resolved_secrets`. Step 06: route every log statement that touches an `Assign` through it.

### Build / tooling changes

- Unit A's "skip buf generate when there are no protos" guard is **deleted**. `make proto` now runs
  `proto-lint` (`buf lint`) then `buf generate`. New targets: `proto-lint`, `proto-breaking`
  (`buf breaking --against '.git#branch=main'`).
- `buf.yaml` gained `lint.ignore_only` for `RPC_REQUEST_STANDARD_NAME` (node.proto) and
  `RPC_RESPONSE_STANDARD_NAME` (node.proto, task.proto). buf's STANDARD lint wants
  `Stream(StreamRequest) returns (StreamResponse)`; the canonical names in `00-index.md` are
  `NodeMessage`/`ServerMessage`, and `StreamTaskEvents` streams a bare `TaskEvent`. The names win.
- `.github/workflows/ci.yml` gained a `proto` job: `bufbuild/buf-setup-action@v1` pinned to buf 1.72.0,
  `go install protoc-gen-go@v1.36.12` and `protoc-gen-connect-go@v1.20.0`, then `make proto-lint`,
  `make proto-breaking`, and `make proto` + `git diff --exit-code -- internal/proto` to prove the committed
  output is current. `fetch-depth: 0` is required for the breaking check.

### Deviations, and why

1. **`connectrpc.com/connect` runtime is pinned to `v1.18.1`, not `v1.20.0`.** connect v1.20.0's `go.mod`
   declares `go 1.25.0`, which would force this module's `go` directive up from the pre-decided `go 1.23.0`.
   The *generator* is still connect-go v1.20.0 (what is installed locally and what CI installs); its output only
   asserts `connect.IsAtLeastVersion1_13_0`, so the older runtime is fine. If a later unit needs a connect ≥1.19
   feature, bumping means bumping `go.mod`'s go directive too — that is a decision for Alvaro, not a unit.
2. **`TaskEvent` and its payload messages live in `node.proto`** (as the step file specifies), so `task.proto`
   imports `node.proto` for `StreamTaskEvents`. Slightly odd layering; moving `TaskEvent` to `common.proto`
   later is a pure file move with no wire change, but it *is* a `buf breaking FILE` failure, so do it never or
   do it deliberately.
3. **`NodeCapacity` lives in `common.proto`**, not `node.proto`, because both `Hello` and the admin `Node`
   message use it.
4. **`Task.exit_code` is proto3 `optional`** → the Go field is `ExitCode *int32` (test it for nil; the generated
   `GetExitCode() int32` flattens nil to 0). A task that has not exited has no exit code, and `0` is a real one. `Exited.exit_code` and `Finished.exit_code` are
   plain `int32` — by the time those are emitted the process has definitely exited.
5. `LogChunk.bytes` really is a field named `bytes` of type `bytes` → Go `LogChunk.Bytes []byte`.
6. `pkg/spec/.gitkeep` was deleted (the directory has real files now). `internal/proto/.gitkeep` was left alone.
7. `go mod tidy` was run (this unit was the only one in the tree). New direct deps:
   `connectrpc.com/connect`, `google.golang.org/protobuf`, `go.yaml.in/yaml/v3`, `github.com/oklog/ulid/v2`.

### Open problems

- `make proto-breaking` **fails on a tree whose `main` has no protos** (`Failure: Module "path: "proto"" had no
  .proto files`, exit 1) — the same buf behaviour unit A documented for `buf lint`. It only started passing once
  this commit put protos on `main`. If someone rewrites history back past this commit, the CI `proto` job will
  fail for that reason and not because of a real breaking change.
- No `web/` TS codegen yet. Unit G has to add `@bufbuild/protoc-gen-es` v2.14.x to `buf.gen.yaml` (see the
  Connect-ES v2 note in the shared conventions: there is no `protoc-gen-connect-es`). Nothing in this unit
  blocks that; it is a second plugin entry writing into `web/src/gen`.

