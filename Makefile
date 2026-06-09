SHELL := /bin/bash
GO    ?= go
NPM   ?= npm

PROTO_DIR := proto
PROTO_OUT := proto/gen

.PHONY: all
all: proto web-deps build

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

.PHONY: build
build:
	$(GO) build -o bin/master ./cmd/master
	$(GO) build -o bin/agent  ./cmd/agent

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
	$(GO) run ./cmd/master

.PHONY: run-agent
run-agent:
	$(GO) run ./cmd/agent --master=127.0.0.1:7000 --name=local-node

.PHONY: clean
clean:
	rm -rf bin/ web/dist/ web/node_modules/ $(PROTO_OUT)/
