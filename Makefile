# protoc-gen-go / protoc-gen-connect-go live in GOPATH/bin and protoc-gen-es lives in
# web/node_modules/.bin; neither is usually on an interactive PATH. buf generate shells out to
# all three, so put both directories on PATH here.
export PATH := $(CURDIR)/web/node_modules/.bin:$(shell go env GOPATH)/bin:$(PATH)

MODULE   := github.com/alvaroibarguen/podium
BINARIES := podium podium-server podium-node podium-runner

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
LDFLAGS := -X $(MODULE)/internal/version.Version=$(VERSION) -X $(MODULE)/internal/version.Commit=$(COMMIT)

.PHONY: build web web-deps web-test test test-integration e2e lint proto proto-lint proto-breaking fmt clean

# The shipped binary carries the real UI, so build waits for it. `go build ./...` on its own
# still compiles: web/dist holds a committed placeholder and the handler reports that no UI was
# built in. For Go-only iteration use `go build -tags noui ./...`, which needs no Node at all.
build: web
	@mkdir -p bin
	@for b in $(BINARIES); do \
		echo "go build ./cmd/$$b -> bin/$$b"; \
		go build -ldflags "$(LDFLAGS)" -o bin/$$b ./cmd/$$b || exit 1; \
	done

# web builds the UI into web/dist, which //go:embed picks up. VERSION reaches the bundle so the
# header shows the version of the binary serving it.
web: web-deps
	cd web && PODIUM_VERSION="$(VERSION)" pnpm build

web-deps:
	cd web && pnpm install --frozen-lockfile

web-test: web-deps
	cd web && pnpm lint && pnpm typecheck && pnpm test

test:
	go test ./...

test-integration:
	go test -tags integration ./...

# End-to-end: a real Postgres (testcontainers), a real podium-server and podium-node, and
# the CLI driven as a subprocess. Needs a working Docker engine. The test builds the
# binaries it drives, so `build` is not a prerequisite.
e2e:
	go test -tags e2e ./test/e2e/... -count=1 -timeout 30m -v

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
