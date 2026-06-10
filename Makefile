SHELL := /bin/bash
GO    ?= go
NPM   ?= npm

PROTO_DIR := proto
PROTO_OUT := proto/gen

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
	$(GO) build -o bin/pp ./cmd/pp

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
	$(GO) run ./cmd/pp master --http=:8080 --grpc=:7000 --db=./pp.db

.PHONY: run-agent
run-agent:
	$(GO) run ./cmd/pp agent --master=127.0.0.1:7000 --name=local-node

.PHONY: clean
clean:
	rm -rf bin/ web/dist/ web/node_modules/ $(PROTO_OUT)/
