SHELL := /bin/bash
GO    ?= go
NPM   ?= npm

PROTO_DIR := proto
PROTO_OUT := proto/gen

# Build-time version metadata, injected via -ldflags. Override on the
# command line: `make build VERSION=v0.1.0 COMMIT=$(git rev-parse HEAD)`.
# GoReleaser sets these automatically (see .goreleaser.yml).
VERSION   ?= dev
COMMIT    ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILD_TIME ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS   := -X main.Version=$(VERSION) \
             -X main.Commit=$(COMMIT) \
             -X main.BuildTime=$(BUILD_TIME)

.PHONY: all
all: proto web-deps web-build build

.PHONY: proto
proto:
	@mkdir -p $(PROTO_OUT)
	protoc \
		--proto_path=$(PROTO_DIR) \
		--go_out=$(PROTO_OUT) --go_opt=paths=source_relative \
		--go-grpc_out=$(PROTO_OUT) --go-grpc_opt=paths=source_relative \
		$(PROTO_DIR)/agent.proto
	@echo "proto generated -> $(PROTO_OUT)"

.PHONY: tidy
tidy:
	$(GO) mod tidy

.PHONY: test
test:
	$(GO) test ./...

.PHONY: test-race
test-race:
	$(GO) test -race ./...

.PHONY: install
install: build
	install -m 0755 bin/pp $(DESTDIR)/usr/local/bin/pp

.PHONY: install-systemd-master
install-systemd-master: install
	sudo PP_BIN=/usr/local/bin/pp contrib/systemd/install.sh master

.PHONY: install-systemd-agent
install-systemd-agent: install
	sudo PP_BIN=/usr/local/bin/pp contrib/systemd/install.sh agent

.PHONY: uninstall-systemd
uninstall-systemd:
	sudo contrib/systemd/uninstall.sh

.PHONY: build
build:
	$(GO) build -ldflags "$(LDFLAGS)" -o bin/pp ./cmd/pp

# `make release-snapshot` runs goreleaser in snapshot mode — builds
# the full cross-compile matrix to ./dist/ without publishing or
# requiring git tags. Use this to dry-run the release pipeline locally.
.PHONY: release-snapshot
release-snapshot:
	goreleaser release --snapshot --clean --skip publish,announce,validate

.PHONY: release-check
release-check:
	goreleaser check

.PHONY: web-deps
web-deps:
	cd web && $(NPM) install

.PHONY: web-dev
web-dev:
	cd web && $(NPM) run dev

.PHONY: web-build
web-build:
	cd web && $(NPM) run build

.PHONY: run-master
run-master:
	$(GO) run -ldflags "$(LDFLAGS)" ./cmd/pp master --http=:8080 --grpc=:7000 --db=./pp.db

.PHONY: run-agent
run-agent:
	$(GO) run -ldflags "$(LDFLAGS)" ./cmd/pp agent --master=127.0.0.1:7000 --name=local-node

.PHONY: clean
clean:
	rm -rf bin/ web/dist/ web/node_modules/ $(PROTO_OUT)/
