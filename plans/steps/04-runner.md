# Step 04 — `podium-runner`: PID 1 of every task container

**Milestone:** M1 · **Depends on:** 02 · **Design ref:** §2 binaries, §7.2 step 5, §8 topology

## Goal
A tiny static Linux binary that runs as PID 1 inside the task container, executes the task command,
forwards signals, reaps zombies, and reports structured events to the node over a Unix socket.
In this slice it runs exactly one command; the playbook step engine comes much later.

## In scope

### Behaviour
1. Start: read config from env set by the node: `PODIUM_TASK_ID`, `PODIUM_LEASE_ID`, `PODIUM_EVENTS_SOCK`
   (default `/podium/events.sock`), `PODIUM_WORKDIR` (default `/workspace`). Command = `os.Args[1:]`.
2. Connect to the events socket (retry up to 5s; if unavailable, **continue** — events are best-effort,
   stdout/stderr still flow through Docker). Send `{"kind":"started","pid":<n>,"ts":…}`.
3. `exec` the command as a child (not `syscall.Exec` — we must stay PID 1) with `Setpgid`, inheriting stdin/stdout/stderr,
   `Dir=PODIUM_WORKDIR`, env passed through minus `PODIUM_EVENTS_SOCK`.
4. Signal handling: forward `SIGTERM`, `SIGINT`, `SIGHUP`, `SIGUSR1/2` to the child's process group.
   On `SIGTERM`, start a 25s timer; if the child is still alive, `SIGKILL` the group (node's grace is 30s — stay inside it).
5. Zombie reaping: `SIGCHLD` handler loops `wait4(-1, WNOHANG)`; the child's own exit is distinguished by PID.
6. On child exit: send `{"kind":"exited","exit_code":n,"signal":"…","ts":…}` and exit with the same code
   (signal death → `128+signo`). Flush and close the socket.
7. Secrets: if `/podium/secrets/` exists, nothing special — files are just there. Runner never reads them.

### Event socket protocol (documented in `docs/runner-events.md`)
- Unix stream socket, newline-delimited JSON, runner → node only in this slice.
- Envelope: `{"v":1,"kind":"<kind>","ts":"<RFC3339Nano>", ...fields}`.
- Kinds now: `started{pid}`, `exited{exit_code, signal}`. Reserved for later: `step{name,status,exit_code}`,
  `artifact{name,path,content_type}`, `usage{...}`, `log{level,msg}`.
- The node treats unknown kinds as opaque `step` events (forward compatible).

### Package layout
- `internal/runner`: `Run(ctx, cfg Config, argv []string) int` (testable, no `os.Exit`), `events.Client` (dial, Send, Close),
  `reaper`, `signals`.
- `cmd/podium-runner/main.go`: parse env, call `Run`, `os.Exit(code)`.
- Build: `CGO_ENABLED=0`, `-ldflags "-s -w"`, `linux/amd64` + `linux/arm64`. Target size < 5 MB.
- Makefile target `runner-embed` copies both builds to `internal/node/docker/runnerbin/runner-linux-{amd64,arm64}`
  (`go:embed`'d by the node in step 05). Add a `.gitattributes` `binary` rule; embedded binaries are **built in CI,
  not committed** — `make build` for the node depends on `runner-embed`.

## Out of scope
- Multi-step execution, git checkout, artifact upload (all later). Windows/macOS containers (never).

## Acceptance checklist
- [ ] `runner -- sh -c 'echo out; echo err >&2; exit 3'` exits 3 with both streams passed through unchanged.
- [ ] `runner -- sleep 100` + SIGTERM → child receives SIGTERM, runner exits 143 within 1s of the child exiting.
- [ ] Child that ignores SIGTERM is SIGKILLed at 25s (test with a short override env `PODIUM_KILL_AFTER=1s`).
- [ ] Zombie test: child spawns a detached grandchild that exits early; runner reaps it (no `<defunct>` in `ps` inside a container test).
- [ ] Missing socket → warning on stderr, command still runs, exit code still propagated.
- [ ] Events test: a fake listener receives `started` then `exited{exit_code:3}` as valid JSON lines.
- [ ] Integration test runs the built runner as PID 1 in `alpine:3` via Docker (`--init` **not** set) and asserts the above.
- [ ] Binary is static (`file bin/podium-runner` says statically linked) and < 5 MB.

## Verification
```sh
make build && file bin/podium-runner && ls -la bin/podium-runner
go test ./internal/runner/...
make test-integration ./internal/runner/...
```

## Notes
- Do **not** use `syscall.Exec`; PID 1 must survive to reap and to emit `exited`.
- Use `os/exec` with `SysProcAttr{Setpgid: true}`; kill with `syscall.Kill(-pgid, sig)`.
- Keep zero third-party dependencies in `internal/runner` — this binary is copied into untrusted images.

## Hand-off notes
_(fill in when done)_
