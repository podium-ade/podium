# Contributing

> Podium is [MIT licensed](LICENSE). By opening a pull request you agree that your
> contribution is offered under the same terms.

---

## What you need

| | version | why |
|---|---|---|
| Go | **1.27+** | `go.mod` sets the floor, and the comment above it says why 1.26 will not do |
| Docker | Engine 24+, **cgroup v2** | the integration and e2e suites drive a real engine |
| Node | 22+ | the web UI |
| pnpm | 11.x | the lockfile is `pnpm-lock.yaml` |
| buf | 1.72.0 | only if you change a `.proto` |
| golangci-lint | **v2**.13.2 | the config is v2-shaped and a v1 binary will not read it |
| sqlc | 1.31.1 | only if you change a query |

Two things bite people:

```sh
docker info --format 'cgroup v{{.CgroupVersion}}'   # must be 2
```

and **`~/go/bin` is usually not on an interactive PATH**, which is where `protoc-gen-go` and
`protoc-gen-connect-go` live. The Makefile puts it there for you, so run `make proto`, never a
bare `buf generate`.

## First build

```sh
git clone https://github.com/alvaroibarguen/podium.git
cd podium
make build
```

`make build` builds the web UI, cross-compiles the two Linux `podium-runner` binaries that
`podium-node` embeds, and writes the three host-native binaries into `bin/`.

If you have no Node and want to iterate on Go alone:

```sh
go build -tags noui ./...
```

`web/dist` holds a committed placeholder so `go build ./...` compiles on a fresh clone; a server
built that way serves a page saying no UI was built in.

## The loop

```sh
make lint            # golangci-lint v2
make test            # unit tests, no Docker, no Postgres
make test-integration    # + real Postgres and Docker via testcontainers
make e2e                 # boots a full stack and drives the real CLI as a subprocess
make fmt                 # golangci-lint fmt
```

```sh
cd web
pnpm lint && pnpm typecheck && pnpm test
```

**`make e2e` is the contract.** It builds its own binaries into a temp directory rather than
trusting `bin/`, uses ephemeral ports, runs the server in-process (so a test can stop and restart
it on the same address) and the node and CLI as real subprocesses.

All of `make lint test`, the integration suite, `make e2e` and the web suite must be green at
every commit.

### Things that will confuse you

- **One `podium-node` per Docker engine.** The daemon claims every container labelled
  `podium.task` on the engine, so two of them adopt each other's work. The e2e suite is strictly
  sequential for that reason.
- **`make proto`, not `buf generate`.** See the PATH note above. The generated Go *and*
  TypeScript are committed, and CI fails if `make proto` changes anything.
- **`podium-runner` is always a Linux binary**, even on a Mac. It is PID 1 inside a Linux task
  container, so a darwin build could never run — `file bin/podium-runner` reporting ELF on macOS
  is correct.
- **Migrations are numbered in order and never reused, and an applied one is never edited.** The
  migration runner tracks applied files by name. Take the next free number.
- **The `dev` transport needs h2c for the node stream.** `NodeService.Stream` is bidirectional
  and Connect refuses that on HTTP/1.1. `internal/transport/local.NewStreamClient` is the reference;
  do not "simplify" it to a plain `http.Client`.
- **Ports.** If 5432 or 8080 are taken on your machine, `PODIUM_PG_PORT` and
  `PODIUM_LOCAL_LISTEN=127.0.0.1:18080` are the escape hatches.

## Running a stack by hand

See [`docs/quickstart.md`](docs/quickstart.md). The short version:

```sh
docker compose -f deploy/docker-compose.dev.yml up -d --wait postgres
PODIUM_TRANSPORT=dev PODIUM_LOCAL_TOKEN=devtoken \
  PODIUM_DATABASE_URL=postgres://podium:podium@127.0.0.1:5432/podium ./bin/podium-server &
export PODIUM_SERVER=http://127.0.0.1:8080 PODIUM_TOKEN=devtoken
TOKEN=$(./bin/podium node enroll-token --label demo)
PODIUM_NODE_SERVER=$PODIUM_SERVER PODIUM_NODE_TRANSPORT=dev PODIUM_NODE_LOCAL_TOKEN=devtoken \
  PODIUM_NODE_ENROLL_TOKEN=$TOKEN PODIUM_NODE_DATA_DIR=/tmp/podium-node ./bin/podium-node &
./bin/podium run --image alpine:3 -- echo hello
```

For the UI with hot reload, which needs no token because the Vite proxy injects one:

```sh
cd web && PODIUM_SERVER=http://127.0.0.1:8080 PODIUM_LOCAL_TOKEN=devtoken pnpm dev
```

Teardown: `pkill -f bin/podium-node; pkill -f bin/podium-server`, then
`docker compose -f deploy/docker-compose.dev.yml down -v`. A clean run leaves no container,
network or volume behind — check with `docker ps -a --filter label=podium.task`.

---

## House rules

These are what the existing code follows. Match them.

- **`slog` for all logging.** `context.Context` is the first argument everywhere. Errors are
  wrapped with `%w`.
- **No global state** except the process-level logger.
- **Never shell out to the `docker` CLI from Go.** The Docker Engine SDK
  (`github.com/docker/docker/client`) only.
- **Never commit secrets, tokens or key material.** Test fixtures use obvious fakes. `.gitignore`
  already covers `master.key`, `identity.json` and `tailscaled.state` in the spellings the docs
  lead people to.
- **Never disable, skip or `t.Skip` a failing test to get green.** Fix the cause.
- **Comment why, not what.** The existing comments explain decisions that are not obvious from
  the code — why `ContainerWait` uses `WaitConditionNextExit`, why the event socket uses `Binds`
  and not `Mounts`. Write those; do not narrate the code.
- **Surgical changes.** Do not reformat, rename or "improve" code your change does not touch.

### Podium's own rules about the machine it runs on

These exist because a developer's Docker engine is shared with the rest of their work:

- **Podium only ever removes an image it pulled itself.** `<data_dir>/images.json` is the
  allow-list, image pruning is **off** by default, and nothing in this repository runs
  `docker system prune` or any bulk removal.
- **No test may pull, build, tag or remove an image** it did not have to. There is exactly one
  pre-existing exception (`TestPullEmitsThrottledPullingEvents`, which removes and re-pulls
  `alpine:3.21` so the pull it measures is real, and removes it again). Do not add a second.
- **Every container, network and volume a test creates must be torn down.** The leak checks are
  there to catch you.

### Documentation is part of the change

- A new `PODIUM_*` environment variable **must** be added to
  [`deploy/.env.example`](deploy/.env.example). `go test ./deploy/...` fails otherwise, in both
  directions: it also fails on a documented variable nothing reads any more.
- A new `examples/*.yaml` is parsed by `go test ./examples/...` with the same decoder
  `podium run --spec` uses, so a renamed field fails there rather than in front of a reader.
- `docs/cli.md`'s exit codes and stream contract are **contractual**. Changing one is a breaking
  change.
- **Be honest about what has not been tested.** Several documents carry an explicit
  "UNVERIFIED" marker — the tailnet transport, the object store against a real S3, the container
  images, the node installer. If your change proves one of them, delete the marker. If it adds a
  new unproven path, add one.

## Commits and pull requests

- One logical change per commit. Subject in the imperative: `fix(node): release a slot when a
  task reaches a terminal status`.
- The PR template asks what you ran. Fill it in with the actual output, not a claim.
- CI is path-filtered and runs on pull requests only, never on the push to main that a merge
  produces. Go changes run `lint / test / build` and `integration`; `web/**` runs the web checks;
  `proto/**` runs buf lint, breaking and the generated-code check; the release snapshot runs only
  when `.goreleaser.yaml`, `release.yml`, `go.mod`, `Makefile` or `LICENSE` change. Everything runs
  once a week on main, and on `workflow_dispatch`.
- **Draft PRs do not run CI.** Mark the PR ready for review, add the `ci` label, or dispatch the
  workflow by hand. Pushing again to a branch cancels the run still in flight.
- `make e2e` is **not** in CI (it needs a Docker engine CI does not reliably have), so run it
  locally and say that you did. Neither is `make build` with a real UI — the release snapshot is
  the only job that links `web/dist` into the binaries.

## Known flake

There are none currently outstanding. `TestNodeRestartAdoptsItsContainers` used to duplicate one
log line at a batch boundary about one run in three; step 12 fixed it by having the control plane
tell a reconnecting node its true per-stream byte offsets. If you see a duplicated or missing log
line at a reconnect seam, that is a real regression in reconciliation — do not loosen the
assertion.

## Where things are

```
cmd/                     the four binaries; thin, cobra only
internal/server/         api, nodes, scheduler, store, secrets, logs, artifacts
internal/node/           the daemon; docker/ is the executor
internal/runner/         PID 1 inside a task container. No third-party imports, deliberately
internal/transport/      dev (loopback bearer token) and tailnet (tsnet)
internal/proto/          generated. `make proto`
pkg/spec/                the task spec: the only package outside internal/
web/                     React + Vite + Tailwind, embedded into podium-server with go:embed
deploy/                  compose files, the installer, the systemd unit, .env.example
docs/                    everything a user reads
examples/                task specs the docs point at
test/e2e/                the full-stack suite, build tag `e2e`
```
