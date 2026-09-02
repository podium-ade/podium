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
- [ ] `buf lint` and `buf breaking --against '.git#branch=main'` configured (breaking check may be a no-op now).
- [ ] `make proto` regenerates identical output (idempotent, committed).
- [ ] `pkg/spec` tests: YAML round-trip, defaults applied, validation rejects empty image, bad target, negative resources,
      sidecar name that is not a valid hostname label (`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`), reserved sidecar name `task`.
- [ ] `ToProto`/`FromProto` round-trip test for `TaskSpec`.
- [ ] `docs/protocol.md` exists and matches the proto.

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
_(fill in when done)_
