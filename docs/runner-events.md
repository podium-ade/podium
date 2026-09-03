# The runner event socket

`podium-runner` is PID 1 of every task container. It reports what it does to the node over a
Unix stream socket, which the node bind-mounts into the container at `/podium/events.sock`.

This document is the contract between `internal/runner` (the writer) and
`internal/node/docker` (the reader). It is a private protocol between two binaries the node
ships together — it is not part of the public wire contract in
[`protocol.md`](protocol.md) — but the node forwards some of it onward as `TaskEvent`s.

## Why the runner exists

The kernel discards a signal with default disposition sent to the init process of a PID
namespace. Run a task command as PID 1 and `podium task cancel` cannot stop it: SIGTERM is
dropped and the container only dies when the node's 30-second grace expires and SIGKILL
arrives, reporting exit 137. With the runner at PID 1 there is a real handler, so a bare
`sleep 60` is cancelled in about a second and reports exit 143.

Being PID 1 also makes the runner the reaper of last resort: a grandchild orphaned by its own
parent is re-parented onto it, and without a wait loop it would stay a zombie for the life of
the task.

## Transport

- `AF_UNIX`, `SOCK_STREAM`. The node listens, the runner connects. One connection per task.
- Newline-delimited JSON, one object per line, **runner → node only** in this slice. The node
  never writes to the socket.
- The node stops accepting after five seconds. The runner connects within milliseconds of the
  container starting; anything slower is a container that will never connect.
- The runner retries the connection for five seconds and then **gives up and runs the command
  anyway**, with one warning on the task's stderr. Events are diagnostics; the task's own
  output flows through Docker and is never at risk.
- Every write carries a two-second deadline. A failed write retires the connection for the
  rest of the run: the node is gone, and the runner has nothing useful to do about it.

## Envelope

```json
{"v":1,"kind":"started","ts":"2026-09-02T20:14:07.918273Z","pid":7}
```

| field | type | meaning |
|---|---|---|
| `v` | int | protocol version, currently `1` |
| `kind` | string | event kind, see below |
| `ts` | string | RFC3339 with nanoseconds, UTC |

Kind-specific fields follow.

## Kinds

| kind | fields | when |
|---|---|---|
| `started` | `pid` | the task command has been forked and exec'd successfully |
| `exited` | `exit_code`, `signal` | the task command exited; `signal` is the `SIGxxx` name when one killed it, and absent otherwise |

Reserved for the playbook engine, which arrives with a later step: `step{name,status,exit_code}`,
`artifact{name,path,content_type}`, `usage{...}`, `log{level,msg}`.

**A node treats any kind it does not know as an opaque `step` event**
(`TASK_EVENT_KIND_STEP`, with `name` set to the kind when the event carries no `name` of its
own), so a runner newer than the node it is mounted by still delivers something the server can
store. An undecodable line is logged and skipped; it never fails a task.

## What the node does with them

- **`started` is authoritative.** It is what the executor turns into its own `started`
  `TaskEvent`, which is the transition the server uses to move a task from `provisioning` to
  `running`. It means the task command is running, not merely that the engine accepted the
  container. If no runner reports in within five seconds the executor emits `started` anyway
  from the Docker API, because that status transition must never go missing.
- **`exited` is not.** The container's exit code comes from `ContainerWait`, which is the
  only source available for a container the node adopted after a restart. The runner's
  `exited` is logged for diagnosis and goes no further.

## Container shape

The node creates the task container like this:

```
Entrypoint: []                                   # cleared; the image's is folded into Cmd
Cmd:        ["/podium/runner", "--", <command>]  # <command> is the spec's, or the image's own
Env:        … PODIUM_TASK_ID, PODIUM_LEASE_ID, PODIUM_WORKDIR, PODIUM_EVENTS_SOCK
Mounts:     podium-ws-<task_id> -> /workspace
            <data_dir>/runner/<arch>/podium-runner -> /podium/runner  (read-only)
Binds:      <socket path>:/podium/events.sock
HostConfig.Init: false                           # the runner *is* the init
```

`Cmd` falls back to the image's own `ENTRYPOINT` + `CMD` when the task spec names no command,
because clearing the entrypoint would otherwise throw them away.

The event socket uses the legacy `Binds` form rather than `Mounts` on purpose: Docker Desktop
validates a `Mounts`-style bind by stat-ing the source through its file-sharing namespace,
where Unix sockets are not visible, and rejects every socket source with *"bind source path
does not exist"*. `Binds` is the form `docker run -v` uses and the one that makes
`/var/run/docker.sock` mountable.

### Where the socket lives on the host

Normally `<data_dir>/tasks/<task_id>/events.sock`, so it is removed with the rest of the
task's state at teardown. `sun_path` is capped at 104 bytes on Darwin and 108 on Linux, so a
data directory deep enough to blow that budget makes the node keep its sockets in a private
`/tmp/podium-run-*` directory instead, removed when the executor closes.

## Environment the runner reads

| variable | default | |
|---|---|---|
| `PODIUM_TASK_ID` | — | informational |
| `PODIUM_LEASE_ID` | — | informational |
| `PODIUM_EVENTS_SOCK` | `/podium/events.sock` | stripped from the child's environment |
| `PODIUM_WORKDIR` | `/workspace` | the child's working directory |
| `PODIUM_KILL_AFTER` | `25s` | SIGTERM → SIGKILL delay for the child's process group |

Everything else in the container environment is passed to the child untouched.

## Signals and exit codes

The runner forwards `SIGTERM`, `SIGINT`, `SIGHUP`, `SIGQUIT`, `SIGUSR1` and `SIGUSR2` to the
child's process group (`kill(-pgid, sig)`; the child is a process group leader). On the first
`SIGTERM` it starts a `PODIUM_KILL_AFTER` timer and, if the child is still alive when it
fires, sends `SIGKILL` to the group. The default of 25s sits inside the node's own 30s cancel
grace, so the engine's SIGKILL never has to fire.

The runner exits with the child's exit code, or `128+signo` when a signal killed it. Failures
of its own use shell conventions: `2` when it was given no command, `126` when the command
could not be executed (including a missing working directory), `127` when it was not found,
and `125` if the runner loses track of its own child.

## Adoption: what the runner does when the node dies

A node that restarts re-attaches to its containers with `docker.Adopt`. The socket the runner
connected to died with the process that listened on it, and nothing will ever accept on that
path again. Both sides degrade quietly:

- The **runner** keeps running. Its next write fails, it retires the connection, and the task
  finishes normally. Its output still reaches the node through Docker's log stream.
- The **node** never opens a socket for an adopted task and never waits for one. An adopted
  run reports `exited` and `finished` from the Docker API alone, and emits no `started` —
  that belonged to the incarnation that started the container.

So a re-adopted task loses only its structured events, which today means nothing beyond the
`started` timestamp. When the playbook engine starts reporting steps and artifacts over this
socket, an adopted task will lose those for the remainder of its run.
