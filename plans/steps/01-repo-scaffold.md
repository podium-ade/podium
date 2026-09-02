# Step 01 — Repository scaffold

**Milestone:** M0 · **Depends on:** — · **Design ref:** §11 Repository layout and stack

## Goal
Create the Podium Go repository skeleton with four buildable (no-op) binaries, tooling, CI, and
licensing, so every later step adds code into a known structure with a green build from commit one.

## In scope
- `go mod init github.com/alvaroibarguen/podium` (Go 1.23+).
- Directories from the canonical layout (create `.gitkeep` where empty): `cmd/{podium,podium-server,podium-node,podium-runner}`,
  `internal/{server,node,runner,transport,proto}`, `proto/podium/v1`, `pkg/spec`, `web`, `deploy`, `docs`.
- Each `cmd/*/main.go`: parses `--version` and `--help` via `flag`/`cobra` (pick **cobra** for the CLI and
  server/node; runner uses plain `flag` to stay tiny), prints `podium-<name> <version> (<commit>)` and exits 0.
  Version/commit injected via `-ldflags` from the Makefile.
- `Makefile` targets: `build` (all four binaries into `bin/`), `test`, `test-integration`, `lint`
  (`golangci-lint`), `proto` (runs `buf generate`; step 02 fills it in), `fmt`, `clean`.
- `buf.yaml` + `buf.gen.yaml` skeleton configured for `protoc-gen-go` and `protoc-gen-connect-go`
  output into `internal/proto` (no protos yet).
- `.golangci.yml` with a sane default set (`govet`, `errcheck`, `staticcheck`, `gofmt`, `goimports`, `revive`).
- `.goreleaser.yaml` stub: builds the four binaries for `linux/amd64`, `linux/arm64`, `darwin/arm64`
  (runner: linux only). No publishing configured yet (step 14).
- GitHub Actions `.github/workflows/ci.yml`: on push/PR run `make lint test build` on ubuntu-latest with Go
  from `go.mod`; a second job runs `make test-integration` with Docker available (needed from step 03).
- `LICENSE` — **placeholder** file containing `TODO: choose Apache-2.0 or AGPL-3.0 before first public push`.
  Do not pick one; that is Alvaro's decision.
- `README.md` — one paragraph: what Podium is, status "pre-alpha, not usable yet", link to `plans/`.
- `.gitignore` for Go, `bin/`, `web/node_modules`, `web/dist`, `.env*`, and `plans/` **commented out with a note**
  (Alvaro decides whether plans are public).
- `git init`, first commit.

## Out of scope
- Any real logic in the binaries. Protos (step 02). Web app scaffold (step 13).

## Files
```
go.mod go.sum Makefile buf.yaml buf.gen.yaml .golangci.yml .goreleaser.yaml .gitignore LICENSE README.md
.github/workflows/ci.yml
cmd/podium/main.go cmd/podium-server/main.go cmd/podium-node/main.go cmd/podium-runner/main.go
internal/version/version.go        # var Version, Commit set via ldflags; func String()
```

## Acceptance checklist
- [x] `make build` produces `bin/podium`, `bin/podium-server`, `bin/podium-node`, `bin/podium-runner`.
- [x] `bin/podium-server --version` prints `podium-server dev (none)` when built without ldflags, and the
      injected values with `make build`.
- [x] `make lint` and `make test` pass (no tests yet is fine; `go vet ./...` must pass).
- [x] `buf lint` passes on the empty module (or is skipped cleanly when no protos exist — document which).
      — documented: bare `buf lint` **fails** on an empty module (`had no .proto files`, exit 1); the config
      itself is verified good (`buf config ls-modules`, plus a throwaway proto that linted and generated
      clean), and `make proto` skips cleanly with exit 0 until step 02 adds protos.
- [x] CI workflow file is valid YAML and references the same Make targets.
- [x] LICENSE is the TODO placeholder, README states pre-alpha.

## Verification
```sh
make build && ./bin/podium --version && ./bin/podium-node --version
make lint test
git log --oneline | head -1   # "step 01: repository scaffold"
```

## Notes
- Keep `cmd/*/main.go` under ~40 lines each; real wiring arrives in steps 06/07.
- Do not add dependencies beyond cobra, testify, and the buf/connect toolchain here.

## Hand-off notes

Done as written (Unit A of the MVP-0 track, no trims). Everything below is what step 02+ must code against.

**Module & versions.** `go mod init github.com/alvaroibarguen/podium`; `go.mod` declares `go 1.23.0`
(local toolchain is go1.27.1, which builds it fine). Direct deps are only `github.com/spf13/cobra v1.10.2`
and `github.com/stretchr/testify v1.12.1`. `go.sum` is committed.

**Version stamping.** `internal/version` exports `var Version = "dev"`, `var Commit = "none"` and
`func String() string` returning `"<version> (<commit>)"` — *not* the binary name. Each `main.go`
prepends its own name, so `podium-server --version` prints `podium-server dev (none)` and the CLI
prints `podium dev (none)`. Both vars carry doc comments because `revive` fails the build without them.
The Makefile injects
`-X github.com/alvaroibarguen/podium/internal/version.Version=$(VERSION) -X ….Commit=$(COMMIT)` with
`VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)` and
`COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)`. `--always` keeps it working in a
repo with no tags; the `|| echo` fallbacks keep it working in a repo with no commits.

**Binaries.** `cmd/podium`, `cmd/podium-server`, `cmd/podium-node` use cobra: a root command with
`Version: version.String()`, `SilenceErrors`/`SilenceUsage` true, `RunE` = `cmd.Help()`, and
`root.SetVersionTemplate("{{.Name}} {{.Version}}\n")` to get the required format instead of cobra's default
`<name> version <ver>`. `cmd/podium-runner` uses a plain `flag.NewFlagSet(..., flag.ContinueOnError)` so
`--help` exits 0 rather than stdlib's 2. All four are 30–31 lines. Later steps should add subcommands to the
existing root command rather than rewriting `main`.

**Makefile PATH trick (important).** The first line is
`export PATH := $(shell go env GOPATH)/bin:$(PATH)`. `protoc-gen-go` and `protoc-gen-connect-go` live in
`~/go/bin`, which is not on the interactive PATH on this machine, so `buf generate` cannot find them unless it
is run through make. Run `make proto`, not bare `buf generate`, or `export PATH="$HOME/go/bin:$PATH"` first.

**buf schema: v2.** `buf.yaml` and `buf.gen.yaml` both use `version: v2` — verified against the installed
buf 1.72.0 with `buf config ls-modules` (prints `proto`) and `buf lint`. buf v1 module config was not used.
`buf.yaml` declares `modules: [{path: proto}]`, `lint.use: [STANDARD]`, `breaking.use: [FILE]`.
`buf.gen.yaml` uses managed mode with `file_option: go_package_prefix` =
`github.com/alvaroibarguen/podium/internal/proto`, and two `local:` plugins (`protoc-gen-go`,
`protoc-gen-connect-go`) writing to `internal/proto` with `opt: paths=source_relative`.
Verified end to end with a throwaway `proto/podium/v1/smoke.proto` (since removed): output landed at
`internal/proto/podium/v1/smoke.pb.go` (package `podiumv1`) and
`internal/proto/podium/v1/podiumv1connect/smoke.connect.go` (package `podiumv1connect`) with go_package
`github.com/alvaroibarguen/podium/internal/proto/podium/v1`. **Step 02 gets that layout for free.**

**`buf lint` does NOT pass on the empty module** — it fails hard, it does not skip:
`Failure: Module "path: "proto"" had no .proto files` (exit 1). This is buf behaviour, not a config error
(the same config lints clean the moment a `.proto` exists). So `make proto` guards itself:
it `find proto -name '*.proto' -print -quit` and prints
`proto: no .proto files yet — skipping buf generate (step 02 adds them)` when there are none, exiting 0.
**Step 02 should delete that guard** once real protos land and add a `buf lint` step to the Makefile/CI.

**golangci-lint config: v2 schema.** `.golangci.yml` starts with `version: "2"`, enables
`errcheck, govet, revive, staticcheck` under `linters.enable`, and puts `gofmt`/`goimports` under a separate
top-level `formatters:` key (v2 moved formatters out of `linters`) with
`formatters.settings.goimports.local-prefixes: [github.com/alvaroibarguen/podium]`, plus
`linters.exclusions.presets: [std-error-handling]`. Verified with `golangci-lint config verify` (exit 0) and
`golangci-lint run` (0 issues) on golangci-lint 2.13.2. `make fmt` runs `golangci-lint fmt`, not `gofmt`.

**Make targets** (CI uses the same names): `build test test-integration lint proto fmt clean`, all `.PHONY`.
`build` writes all four binaries to `bin/` (gitignored). `test` = `go test ./...`;
`test-integration` = `go test -tags integration ./...` (already green with no integration tests; step 03
adds the first `//go:build integration` files).

**CI.** `.github/workflows/ci.yml` has two ubuntu-latest jobs: `build` (checkout, `actions/setup-go@v5` with
`go-version-file: go.mod`, install golangci-lint **v2.13.2** to `$(go env GOPATH)/bin`, then `make lint test build`)
and `integration` (`make test-integration`). Bump the pinned golangci-lint version there if the local one moves.

**goreleaser.** `.goreleaser.yaml` is a `version: 2` stub with four `builds` entries; the three non-runner
binaries target linux/amd64, linux/arm64, darwin/arm64 (darwin/amd64 ignored), `podium-runner` is linux-only.
`release.disable: true` — no publishing (step 14). **`goreleaser check` was NOT run: goreleaser is not
installed on this machine.** The file is confirmed valid YAML only.

**Layout.** Canonical directories all exist with `.gitkeep` in the empty ones, including
`internal/transport/{dev,tailnet}` (no `mtls` — deliberately deferred, see 00-index).
`LICENSE` is exactly `TODO: choose Apache-2.0 or AGPL-3.0 before first public push`.
`.gitignore` ignores `bin/`, `dist/`, `web/node_modules/`, `web/dist/`, `.env*`, and carries a
**commented-out** `# plans/` line — `plans/` is tracked for now; Alvaro decides whether it stays public.

**One deviation from the step file:** it says "no tests yet is fine", but leaving `testify` in `go.mod`
unused would make `go mod tidy` drop it. `internal/version/version_test.go` is one 12-line testify test so
the dependency is real and `make test` actually runs something.

