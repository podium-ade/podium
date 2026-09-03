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
| stderr | the task's stderr for `run` and `logs`; Podium's own progress lines, prefixed `→` and dimmed on a terminal |

So `podium run … > out.txt` captures exactly the task's stdout, and
`TOKEN=$(podium node enroll-token)` captures exactly the token.

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
| `--timeout` | task timeout (default 1h) |
| `--working-dir` | working directory inside the container (default `/workspace`) |
| `--spec FILE` | task spec YAML; flags override its fields |
| `--detach` | print the task ID and return 0 |

Everything after `--` is the command. Progress lines on stderr:

```
→ task task_01j…
→ scheduled on node_01j…
→ running
→ finished exit 3 in 5.2s
```

`run` reconnects to the event stream on its own if the control plane restarts mid-task,
resuming from the last sequence number it printed, so the output has no gap.

### `podium tasks [--status s]... [--limit n] [--node ID]`

Table of tasks, newest first. Statuses: `queued`, `scheduled`, `provisioning`, `running`,
`succeeded`, `failed`, `cancelled`, `lost`.

### `podium task get TASK_ID [--json]`

One task. `--json` prints the wire representation, which is what scripts should parse.

### `podium task cancel TASK_ID [--reason TEXT]`

Asks the node to stop the task and returns immediately. Cancellation is **slow**: the node
sends SIGTERM and SIGKILLs 30 seconds later, and in MVP-0 the task command is PID 1, so a
command that does not trap SIGTERM ignores it and dies at the SIGKILL with exit 137. Allow
about 35 seconds before expecting a terminal status. A command that traps SIGTERM exits 143
in a couple of seconds.

### `podium logs [-f] [--from-seq N] TASK_ID`

Prints a task's output, stdout and stderr on the matching local streams. `--from-seq` is
**exclusive**: the output resumes at `N+1`, so passing the last sequence number you saw
gives you the next one and no repeat. Without `-f` the command stops once it has caught up;
with `-f` it follows until the task is terminal, reconnecting on its own if the stream
breaks.

### `podium nodes`

Table of enrolled nodes: name, ID, status, labels, running/max slots and heartbeat age.
`RUNNING/MAX` is filled in only for nodes holding a live stream on the server you asked.

### `podium node enroll-token [--label L]... [--ttl 1h]`

Mints a single-use enrollment token and prints it to stdout, so it can be captured:

```sh
TOKEN=$(podium node enroll-token --label linux/arm64)
```

The token is shown once. The server keeps only its SHA-256.

This is Podium's own enrollment token, not a Tailscale auth key — a worker needs both, and they
are different things. See [networking.md](networking.md#the-two-keys-which-are-not-the-same-thing).

### `podium node rekey NODE_ID`

Unbinds a node from the Tailscale device it enrolled from.

A node enrolled over the tailnet is pinned to one device, so a copied `identity.json` is useless
elsewhere. Rekey when the worker is genuinely rebuilt or replaced: the node keeps its ID, labels
and history, and the next connection binds it to whatever device it arrives from. Until it
reconnects the node key alone is enough, so rekey immediately before the move, not routinely.

### `podium version`

Prints `podium <version> (<commit>)`.
