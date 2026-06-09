# pumpkinPie

A multi-node Docker container management platform with a web dashboard.
Nodes (machines with Docker) self-register to a central **Master**, report
live CPU / memory / disk / GPU metrics, and can be told by the Master to
create / stop containers. Container services are exposed to the outside
world through a single reverse-proxy port on the Master, with traffic
tunneled over gRPC to the right node.

```
                    ┌──────────────────────────────────┐
                    │  Web UI (Vue 3 + Element Plus)  │
                    └────────────────┬─────────────────┘
                                     │  HTTP REST
                    ┌────────────────▼─────────────────┐
                    │  Master (Go + Gin)               │
                    │   - /api REST                    │
                    │   - /ui  built-in dashboard      │
                    │   - reverse-proxy  →  gRPC tunnel│
                    │   - SQLite                       │
                    └────────────────┬─────────────────┘
                                     │  gRPC bidirectional stream
              ┌──────────────────────┼──────────────────────┐
              ▼                      ▼                      ▼
       ┌─────────────┐        ┌─────────────┐        ┌─────────────┐
       │  Agent #1   │        │  Agent #2   │        │  Agent #N   │
       │  Docker SDK │        │  Docker SDK │        │  Docker SDK │
       │  metrics    │        │  metrics    │        │  metrics    │
       └─────────────┘        └─────────────┘        └─────────────┘
```

## Features

1. **Multi-node registration** — agents dial Master on startup, identify
   themselves by `name + hostname`, and reconnect with exponential backoff.
2. **Live dashboard** — per-node CPU / memory / disk / GPU (via `nvidia-smi`),
   updated every 5s, plus cluster-wide gauges and per-node utilization
   charts in the UI.
3. **Container creation via UI** — pick image, env, ports, resource limits
   (CPU / memory / GPU), and either auto-select the least-loaded node or
   pin to a specific one. The Master opens a tunneled reverse proxy to the
   resulting container.
4. **Unified container list** — every container on every node is visible in
   a single table with state, status, assigned node, and public URL.

## Quick start

### 1. Build everything

```bash
make all           # generates proto, builds web/dist, builds bin/pp
```

### 2. Start the Master

```bash
./bin/pp master \
  --http=:8080          # UI + REST API
  --grpc=:7000          # gRPC for agents
  --db=./pumpkinpie.db  # SQLite
```

The master does not expose a single "proxy port" — every container
created on the cluster gets its own dedicated port in the range
**30000-32767** (see "Reverse-proxy URL scheme" below).

Open <http://localhost:8080/ui/> in your browser.

### 3. Start one or more Agents (on any machine with Docker)

The agent discovers the Docker socket in this order:

1. `$DOCKER_SOCK` — explicit override (highest priority)
2. `$DOCKER_HOST` — standard Docker variable, only `unix://` prefix is recognized
3. `/var/run/docker.sock` — hardcoded fallback (Linux default)

```bash
# Linux with standard Docker — no env var needed
./bin/pp agent --master=master.example.com:7000 --name=node-A

# Linux with rootless Docker
DOCKER_SOCK=$XDG_RUNTIME_DIR/docker.sock ./bin/pp agent --master=... --name=node-A

# macOS Docker Desktop
DOCKER_SOCK=$HOME/.docker/run/docker.sock ./bin/pp agent --master=... --name=node-A

# Remote Docker daemon
DOCKER_HOST=tcp://docker-host:2375 ./bin/pp agent --master=... --name=node-A
```

Within ~5 seconds the node cards should appear in the dashboard with live
metrics.

### 5. Subcommands

`pp` is a single binary that runs as either role:

```
pp master [flags]   # control plane: UI + API + gRPC + reverse proxy
pp agent  [flags]   # node agent: registers to master, hosts containers
pp version          # print version
pp help             # print usage
```

Run `pp <subcommand> -h` to see flags for a subcommand.

### 4. Create a container

In the UI, click **New Container**, fill in:
- Image: `nginx:alpine`
- Port mapping: container `80` / `tcp`
- (optional) CPU / memory / GPU limits
- (optional) pin to a specific node

Hit **Create** — the scheduler picks an idle node, the agent creates the
container, the Master registers a public URL, and you can click it to
verify the proxy works.

## Reverse-proxy URL scheme

Each container gets a dedicated port in the range **30000-32767**, chosen
automatically when the container is created (and freed when it's deleted).

```
http://localhost:30000/   →  container on first online node
http://localhost:30001/   →  container on next online node
...
```

The Master opens a fresh `net.Listener` only for the ports actually in
use, so the number of open file descriptors equals the number of
running containers (not the size of the port range). After deleting a
container, its port is immediately released and reused by the next
container.

Under the hood, the Master hijacks the HTTP connection, opens a gRPC
tunnel to the owning node, and pipes bytes both ways.

## Architecture notes

- **gRPC bidirectional stream** carries both directions of traffic on one
  connection per agent — control (register, metrics, container state)
  and per-request proxy data share the same stream, with `tunnel_id`
  multiplexing.
- **HTTP API** uses Gin; metrics are pushed to the browser over a
  WebSocket at `/api/ws`.
- **SQLite** (`modernc.org/sqlite`, pure Go) for metadata. No external
  database required.
- **Docker integration** is a tiny in-tree HTTP client against the
  Docker Engine API over the unix socket — no SDK churn, no cgo.
- **GPU detection** uses `nvidia-smi` (works with the NVIDIA container
  toolkit); on hosts without an NVIDIA GPU the field stays at 0.

## Development

```bash
make proto                 # regenerate Go code from proto/agent.proto
make web-dev               # Vite dev server with /api proxy to :8080
make run-master            # go run ./cmd/pp master
make run-agent             # go run ./cmd/pp agent --master=127.0.0.1:7000 --name=local-node
make test                  # go test ./...
make test-race             # go test -race ./...  (validates port-allocation lock)
```

## Project layout

```
pumpkinPie/
├── Makefile
├── proto/                 # agent.proto + generated Go code
├── cmd/pp/main.go         # single entrypoint, dispatches to subcommands
├── internal/
│   ├── cmd/
│   │   ├── master/        # master subcommand entrypoint
│   │   └── agent/         # agent subcommand entrypoint
│   ├── master/
│   │   ├── store/         # SQLite layer (nodes, containers, port allocation)
│   │   ├── agentmgr/      # gRPC server, per-agent session, message dispatch
│   │   ├── scheduler/     # idle-node selection (weighted score)
│   │   ├── proxy/         # reverse-proxy + gRPC tunnel
│   │   └── api/           # Gin HTTP handlers + WebSocket
│   └── agent/
│       ├── collector/     # CPU/mem/disk/GPU via gopsutil + nvidia-smi
│       ├── docker/        # Docker Engine HTTP client
│       └── agent.go       # gRPC client, create/stop, tunnel pump
└── web/                   # Vue 3 + Vite + Element Plus
    ├── src/views/         # Dashboard / Nodes / Containers / NewContainer
    └── dist/              # built static assets
```

## License

MIT
