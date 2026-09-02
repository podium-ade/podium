# protoc-gen-go / protoc-gen-connect-go live in GOPATH/bin, which is usually not on
# an interactive PATH. buf generate shells out to them, so put it on PATH here.
export PATH := $(shell go env GOPATH)/bin:$(PATH)

MODULE   := github.com/alvaroibarguen/podium
BINARIES := podium podium-server podium-node podium-runner

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
LDFLAGS := -X $(MODULE)/internal/version.Version=$(VERSION) -X $(MODULE)/internal/version.Commit=$(COMMIT)

.PHONY: build test test-integration lint proto proto-lint proto-breaking fmt clean

build:
	@mkdir -p bin
	@for b in $(BINARIES); do \
		echo "go build ./cmd/$$b -> bin/$$b"; \
		go build -ldflags "$(LDFLAGS)" -o bin/$$b ./cmd/$$b || exit 1; \
	done

test:
	go test ./...

test-integration:
	go test -tags integration ./...

lint:
	golangci-lint run

proto: proto-lint
	buf generate

proto-lint:
	buf lint

# No-op until main has a committed proto history to compare against.
proto-breaking:
	buf breaking --against '.git#branch=main'

fmt:
	golangci-lint fmt

clean:
	rm -rf bin
