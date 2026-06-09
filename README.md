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
make all           # generates proto, builds web/dist, builds bin/master and bin/agent
```

### 2. Start the Master

```bash
./bin/master \
  --http=:8080          # UI + REST API
  --grpc=:7000          # gRPC for agents
  --proxy-port=8081     # public reverse-proxy port
  --db=./pumpkinpie.db  # SQLite
```

Open <http://localhost:8080/ui/> in your browser.

### 3. Start one or more Agents (on any machine with Docker)

The agent discovers the Docker socket in this order:

1. `$DOCKER_SOCK` — explicit override (highest priority)
2. `$DOCKER_HOST` — standard Docker variable, only `unix://` prefix is recognized
3. `/var/run/docker.sock` — hardcoded fallback (Linux default)

```bash
# Linux with standard Docker — no env var needed
./bin/agent --master=master.example.com:7000 --name=node-A

# Linux with rootless Docker
DOCKER_SOCK=$XDG_RUNTIME_DIR/docker.sock ./bin/agent --master=... --name=node-A

# macOS Docker Desktop
DOCKER_SOCK=$HOME/.docker/run/docker.sock ./bin/agent --master=... --name=node-A

# Remote Docker daemon
DOCKER_HOST=tcp://docker-host:2375 ./bin/agent --master=... --name=node-A
```

Within ~5 seconds the node cards should appear in the dashboard with live
metrics.

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

```
http://<master>:<proxy-port>/c/<container_id>/<path>
```

For example `http://localhost:8081/c/c-7bb09bb560f9/`. The Master hijacks
the connection, opens a gRPC tunnel to the owning node, and pipes bytes
both ways. The container's original path is preserved (the `/c/<id>` prefix
is stripped before being sent to the container).

This single-port design avoids the need to bind hundreds of ports when
many containers are deployed; just add a DNS record or `/etc/hosts` entry
if you want a prettier hostname.

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
make run-master            # go run ./cmd/master
make run-agent             # go run ./cmd/agent --master=127.0.0.1:7000 --name=local-node
```

## Project layout

```
pumpkinPie/
├── Makefile
├── proto/                 # agent.proto + generated Go code
├── cmd/
│   ├── master/main.go     # Master entrypoint
│   └── agent/main.go      # Agent entrypoint
├── internal/
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
