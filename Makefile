# protoc-gen-go / protoc-gen-connect-go live in GOPATH/bin and protoc-gen-es lives in
# web/node_modules/.bin; neither is usually on an interactive PATH. buf generate shells out to
# all three, so put both directories on PATH here.
export PATH := $(CURDIR)/web/node_modules/.bin:$(shell go env GOPATH)/bin:$(PATH)

MODULE   := github.com/alvaroibarguen/podium
BINARIES := podium podium-server podium-node podium-agent

# podium-runner is embedded into podium-node, not linked into it.
RUNNERBIN   := internal/node/docker/runnerbin
HOST_GOARCH := $(shell go env GOARCH)

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
LDFLAGS := -X $(MODULE)/internal/version.Version=$(VERSION) -X $(MODULE)/internal/version.Commit=$(COMMIT)

# The agent runtime image. Local, tagged :dev, and never pushed by this Makefile: the e2e
# node runs on the host's Docker engine, so a locally built tag is visible to a task
# without a registry in between.
AGENT_RUNTIME := podium-agent-runtime

.PHONY: build runner-embed dist-node dist-node-all web web-deps web-test test test-integration e2e e2e-memory lint proto proto-lint proto-breaking fmt clean agent-runtime agent-runtime-dist agent-runtime-test stack-up stack-down stack-status

# The shipped binary carries the real UI, so build waits for it. `go build ./...` on its own
# still compiles: web/dist holds a committed placeholder and the handler reports that no UI was
# built in. For Go-only iteration use `go build -tags noui ./...`, which needs no Node at all.
build: web runner-embed
	@mkdir -p bin
	@for b in $(BINARIES); do \
		echo "go build ./cmd/$$b -> bin/$$b"; \
		go build -ldflags "$(LDFLAGS)" -o bin/$$b ./cmd/$$b || exit 1; \
	done

# podium-runner is built twice, for two different jobs.
#
# Both Linux architectures land in $(RUNNERBIN), which internal/node/docker embeds: inside a
# task container the runner is PID 1, and that container is always Linux.
#
# bin/podium-runner is a NATIVE build, for this host. It used to be a copy of the Linux
# binary for the host's architecture, which was wrong on a Mac in two ways: it could not be
# run by hand, which the comment here claimed was its purpose, and a HOST TURN could not
# spawn it. A host turn runs the agent runtime as a child of the conductor, on this machine,
# and the runtime emits its events by exec'ing this binary — so on Darwin an ELF binary meant
# every host turn exited without emitting anything and reported success having said nothing.
# PODIUM_AGENT_RUNNER_BIN points here.
#
# "Native" is pinned to GOHOSTOS/GOHOSTARCH rather than left to the environment because
# `make dist-node GOOS=linux GOARCH=amd64` — the documented way to build a worker binary —
# exports both variables into every recipe line, this one included. Without the pin that
# command quietly replaced the Mac's runner with an ELF binary, and every host turn after
# it ended without a word (2026-09-09).
#
# The binaries are build output and are gitignored; only the .gitkeep placeholder that keeps
# //go:embed compiling on a fresh clone is committed.
runner-embed:
	@mkdir -p $(RUNNERBIN) bin
	@for a in amd64 arm64; do \
		out=$(RUNNERBIN)/runner-linux-$$a; \
		echo "GOOS=linux GOARCH=$$a CGO_ENABLED=0 go build ./cmd/podium-runner -> $$out"; \
		GOOS=linux GOARCH=$$a CGO_ENABLED=0 \
			go build -trimpath -ldflags "$(LDFLAGS) -s -w" -o $$out ./cmd/podium-runner || exit 1; \
	done
	@echo "go build ./cmd/podium-runner -> bin/podium-runner (native, for host turns)"
	@GOOS=$$(go env GOHOSTOS) GOARCH=$$(go env GOHOSTARCH) \
		go build -trimpath -ldflags "$(LDFLAGS)" -o bin/podium-runner ./cmd/podium-runner

# Cross-compiled worker binaries.
#
# The control plane and its workers do not have to share an architecture: a darwin/arm64 server
# drives linux/amd64 workers perfectly well. `make build` above is host-native, which on this
# machine produces a Mach-O binary that cannot run on a Linux worker, so a node binary has to be
# built *for the worker*. linux/amd64 is the expected default; check the target with `uname -m`
# (x86_64 -> amd64, aarch64 -> arm64).
#
# CGO_ENABLED=0 gives a static binary that runs on any glibc or musl Linux. Nothing the node
# needs wants cgo: the Docker SDK, pgx, tsnet and gopsutil (which reads /proc on Linux) are all
# pure Go. The matrix matches .goreleaser.yaml, which is the release build of the same set.
GOOS   ?= linux
GOARCH ?= amd64
DIST_BINARIES := podium-node podium

dist-node: runner-embed
	@mkdir -p bin
	@for b in $(DIST_BINARIES); do \
		out=bin/$$b-$(GOOS)-$(GOARCH); \
		echo "GOOS=$(GOOS) GOARCH=$(GOARCH) CGO_ENABLED=0 go build ./cmd/$$b -> $$out"; \
		GOOS=$(GOOS) GOARCH=$(GOARCH) CGO_ENABLED=0 \
			go build -trimpath -ldflags "$(LDFLAGS)" -o $$out ./cmd/$$b || exit 1; \
	done

dist-node-all:
	$(MAKE) dist-node GOOS=linux GOARCH=amd64
	$(MAKE) dist-node GOOS=linux GOARCH=arm64

# web builds the UI into web/dist, which //go:embed picks up. VERSION reaches the bundle so the
# header shows the version of the binary serving it.
web: web-deps
	cd web && PODIUM_VERSION="$(VERSION)" pnpm build

web-deps:
	cd web && pnpm install --frozen-lockfile

web-test: web-deps
	cd web && pnpm lint && pnpm typecheck && pnpm test

# The agent runtime images, host arch, tagged :dev. Podium ships the base plus -dev, which
# is a dogfood image for turns that build Podium itself; -dev copies the runtime layer out
# of the base, so the order below matters. Any OTHER set of tools is an image of your own,
# built `FROM podium-agent-runtime` and yours to publish. `build` deliberately does NOT
# depend on this: Docker is not a prerequisite for compiling the Go binaries.
agent-runtime:
	docker build --build-arg VERSION="$(VERSION)" --build-arg REVISION="$(COMMIT)" \
		-t $(AGENT_RUNTIME):dev -f agent/runtime/Dockerfile agent/runtime
	docker build --build-arg VERSION="$(VERSION)" --build-arg REVISION="$(COMMIT)" \
		--build-arg RUNTIME_IMAGE=$(AGENT_RUNTIME):dev \
		-t $(AGENT_RUNTIME)-dev:dev -f agent/runtime/Dockerfile.dev agent/runtime

# The runtime built for THIS HOST, which is what PODIUM_AGENT_HOST_RUNTIME points at: the
# assistant answers a conversation by running dist/main.js as a child of podium-agent, with
# no container and therefore no image to get it from.
#
# It has a target because dist/ is build output and is NOT committed — it was, by accident,
# and a stale copy in git is worse than none: the file on disk would silently be older than
# the source beside it. `build` does not depend on this for the same reason it does not
# depend on the images: a host that runs no assistant needs neither Node nor opencode.
agent-runtime-dist:
	cd agent/runtime && pnpm install --frozen-lockfile && pnpm build
	@echo "PODIUM_AGENT_HOST_RUNTIME=$(CURDIR)/agent/runtime/dist/main.js"

# The runtime's unit tests, then the image tests. The image tests need Docker and the
# `make agent-runtime` tags; they SKIP with a message naming that target when either is
# missing, so this target still works on a machine with no engine.
agent-runtime-test:
	cd agent/runtime && pnpm install --frozen-lockfile && pnpm typecheck && pnpm test && pnpm test:images

test:
	go test ./...

test-integration: runner-embed
	go test -tags integration ./...

# End-to-end: a real Postgres (testcontainers), a real podium-server and podium-node, and
# the CLI driven as a subprocess. Needs a working Docker engine. The test builds the
# binaries it drives, so `build` is not a prerequisite.
e2e: runner-embed
	go test -tags e2e ./test/e2e/... -count=1 -timeout 30m -v

# The shared memory against the REAL engine, not a fake. Deliberately not part of `make e2e`:
# it pulls a 5.9 GB third-party image and asserts that image's own REST and MCP contract, so
# a red run here means the pinned engine changed, not that Podium broke. Run it by hand when
# the pinned version moves.
e2e-memory:
	go test -tags 'e2e e2e_memory' ./test/e2e/... -count=1 -timeout 20m -v \
		-run TestTheRealMemoryEngineSpeaksWhatTheClientExpects

lint:
	golangci-lint run

# protoc-gen-es comes from web/node_modules, so codegen needs the web dependencies installed.
proto: proto-lint web-deps
	buf generate

proto-lint:
	buf lint

# No-op until main has a committed proto history to compare against.
# Against origin/main, not main: `actions/checkout` leaves a detached HEAD and no local
# `main` branch, so `#branch=main` fails there with "couldn't find remote ref main" while
# working fine on a developer's machine. The remote-tracking ref exists in both places —
# CI sets fetch-depth: 0 so it is fetched.
proto-breaking:
	buf breaking --against '.git#ref=origin/main'

fmt:
	golangci-lint fmt

# Run what `build` produced, against the dependencies in docker-compose.dev.yml and the
# .env `podium-server init` writes. This is the only supported way to run the conductor
# under the tailnet transport — see the note in deploy/run-host.sh — and the shortest loop
# when you are changing Go code. Name services to act on a subset: `make stack-up S=agent`.
S ?=

stack-up:
	@deploy/run-host.sh up $(S)

stack-down:
	@deploy/run-host.sh down $(S)

stack-status:
	@deploy/run-host.sh status $(S)

clean:
	rm -rf bin web/dist/assets web/dist/index.html
	rm -f $(RUNNERBIN)/runner-linux-*
