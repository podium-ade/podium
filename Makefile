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

# The agent runtime images. Local, tagged :dev, and never pushed by this Makefile: the e2e
# node runs on the host's Docker engine, so a locally built tag is visible to a task
# without a registry in between.
AGENT_RUNTIME := podium-agent-runtime

.PHONY: build runner-embed dist-node dist-node-all web web-deps web-test test test-integration e2e e2e-memory lint proto proto-lint proto-breaking fmt clean agent-runtime agent-runtime-test

# The shipped binary carries the real UI, so build waits for it. `go build ./...` on its own
# still compiles: web/dist holds a committed placeholder and the handler reports that no UI was
# built in. For Go-only iteration use `go build -tags noui ./...`, which needs no Node at all.
build: web runner-embed
	@mkdir -p bin
	@for b in $(BINARIES); do \
		echo "go build ./cmd/$$b -> bin/$$b"; \
		go build -ldflags "$(LDFLAGS)" -o bin/$$b ./cmd/$$b || exit 1; \
	done

# podium-runner is PID 1 inside a Linux task container, so every build of it is a Linux
# cross-compile: a darwin binary would be useless. Both architectures land in $(RUNNERBIN),
# which internal/node/docker embeds, and the host architecture's copy is also written to
# bin/podium-runner so it can be inspected and run by hand. The binaries are build output
# and are gitignored; only the .gitkeep placeholder that keeps //go:embed compiling on a
# fresh clone is committed.
runner-embed:
	@mkdir -p $(RUNNERBIN) bin
	@for a in amd64 arm64; do \
		out=$(RUNNERBIN)/runner-linux-$$a; \
		echo "GOOS=linux GOARCH=$$a CGO_ENABLED=0 go build ./cmd/podium-runner -> $$out"; \
		GOOS=linux GOARCH=$$a CGO_ENABLED=0 \
			go build -trimpath -ldflags "$(LDFLAGS) -s -w" -o $$out ./cmd/podium-runner || exit 1; \
	done
	@cp $(RUNNERBIN)/runner-linux-$(HOST_GOARCH) bin/podium-runner

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

# The three agent runtime images, host arch, tagged :dev. `build` deliberately does NOT
# depend on this: Docker is not a prerequisite for compiling the Go binaries. -browser and
# -data copy the runtime layer out of the base image, so the order below matters.
agent-runtime:
	docker build --build-arg VERSION="$(VERSION)" --build-arg REVISION="$(COMMIT)" \
		-t $(AGENT_RUNTIME):dev -f agent/runtime/Dockerfile agent/runtime
	docker build --build-arg VERSION="$(VERSION)" --build-arg REVISION="$(COMMIT)" \
		--build-arg RUNTIME_IMAGE=$(AGENT_RUNTIME):dev \
		-t $(AGENT_RUNTIME)-browser:dev -f agent/runtime/Dockerfile.browser agent/runtime
	docker build --build-arg VERSION="$(VERSION)" --build-arg REVISION="$(COMMIT)" \
		--build-arg RUNTIME_IMAGE=$(AGENT_RUNTIME):dev \
		-t $(AGENT_RUNTIME)-data:dev -f agent/runtime/Dockerfile.data agent/runtime

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
proto-breaking:
	buf breaking --against '.git#branch=main'

fmt:
	golangci-lint fmt

clean:
	rm -rf bin web/dist/assets web/dist/index.html
	rm -f $(RUNNERBIN)/runner-linux-*
