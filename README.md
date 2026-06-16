# pumpkinPie

A multi-node Docker container management platform with a web dashboard.
Nodes (machines with Docker) self-register to a central **Master**, report
live CPU / memory / disk / GPU metrics, and can be told by the Master to
create / stop containers. Each container's published port is exposed to
the outside world through a dedicated TCP listener on the Master, which
forwards connections directly to the agent host's published port.

```
                    ┌──────────────────────────────────┐
                    │  Web UI (Vue 3 + Element Plus)  │
                    └────────────────┬─────────────────┘
                                     │  HTTP REST
                    ┌────────────────▼─────────────────┐
                    │  Master (Go + Gin)               │
                    │   - /api REST                    │
                    │   - /console  built-in dashboard │
                    │   - per-container TCP proxy      │
                    │   - SQLite                       │
                    └──┬──────────────┬─────────────┬──┘
                       │ gRPC ctl     │ TCP proxy   │
                       ▼              ▼             ▼
                ┌─────────────┐ ┌─────────────┐ ┌─────────────┐
                │  Agent #1   │ │  Agent #2   │ │  Agent #N   │
                │  Docker SDK │ │  Docker SDK │ │  Docker SDK │
                │  metrics    │ │  metrics    │ │  metrics    │
                └─────────────┘ └─────────────┘ └─────────────┘
```

## Features

1. **Multi-node registration** — agents dial Master on startup, identify
   themselves by `name + hostname`, and reconnect with exponential backoff.
2. **Live dashboard** — per-node CPU / memory / disk / GPU (via `nvidia-smi`),
   updated every 5s, plus cluster-wide gauges and per-node utilization
   charts in the UI.
3. **Container creation via UI** — pick image, env, ports, resource limits
   (CPU / memory / GPU), and either auto-select the least-loaded node or
   pin to a specific one. The Master opens a dedicated TCP listener for
   the container's published port and forwards connections to the agent.
4. **Unified container list** — every container on every node is visible in
   a single table with state, status, assigned node, and public URL.

## Install (recommended)

The `pp` binary is statically linked, has the web UI embedded, and
needs no runtime dependencies. A one-liner downloads the latest
release from GitHub, verifies the SHA-256, installs the binary, and
sets up a systemd unit.

### Master

```bash
curl -sSf https://raw.githubusercontent.com/lbt05/PumpkinPie/main/hack/get.sh \
  | sudo bash -s -- master
```

The installer drops a default `/etc/pp/pp-master.yaml` (listen
addresses, SQLite path, optional GreptimeDB metrics sink). Edit it
and restart `pp-master` to change any setting:

```bash
sudo $EDITOR /etc/pp/pp-master.yaml
sudo systemctl restart pp-master
```

Open <http://localhost:8080/console/>. Status:

```bash
systemctl status pp-master
journalctl -u pp-master -f
```

### Agent (on every worker host)

```bash
PP_MASTER_ADDR=master.example.com:7000 PP_NAME=worker-1 \
  curl -sSf https://raw.githubusercontent.com/lbt05/PumpkinPie/main/hack/get.sh \
  | sudo bash -s -- agent
```

`PP_MASTER_ADDR` / `PP_NAME` are installer-time helpers — get.sh
writes them into the freshly-generated `/etc/pp/pp-agent.yaml`.
After install, edit that YAML directly to change anything; the
agent binary itself takes only `--config=<path>`. The agent also
runs as `root` so it can talk to the Docker socket; set
`docker_sock:` in the YAML (or `DOCKER_SOCK=` env var) for
rootless / Docker Desktop / custom setups.

### Pinning a version

```bash
curl -sSf https://raw.githubusercontent.com/lbt05/PumpkinPie/main/hack/get.sh \
  | sudo bash -s -- master --version v0.1.0
```

The script is signed only by the GitHub TLS certificate — for
stronger supply-chain guarantees, verify the SHA-256 in
`pumpkinpie_v0.1.0_checksums.txt` (linked from the release page) by
hand or wire it into your provisioning tool.

### macOS (binary only, no systemd)

```bash
curl -sSf https://raw.githubusercontent.com/lbt05/PumpkinPie/main/hack/get.sh \
  | sudo bash -s -- master --no-systemd
pp master --config=./pp-master.yaml
```

### Manual install (offline / no GitHub access)

If `api.github.com` or `github.com` is unreachable from the target
host, you can still install from a tarball you sideloaded
(sneakernet, internal mirror, internal release server, etc.):

```bash
# 1. On a machine with GitHub access, fetch the tarball
curl -sSfL -o pumpkinpie_v0.1.0_linux_amd64.tar.gz \
  https://github.com/lbt05/PumpkinPie/releases/download/v0.1.0/pumpkinpie_v0.1.0_linux_amd64.tar.gz

# 2. Move it to the target host (scp, USB, whatever)

# 3. On the target host, run the bundled installer
sudo ./install-local.sh master ./pumpkinpie_v0.1.0_linux_amd64.tar.gz
sudo ./install-local.sh agent  ./pumpkinpie_v0.1.0_linux_amd64.tar.gz \
  --master-ip=10.0.0.1:7000 --name=worker-1
```

`install-local.sh` is included in every release tarball (alongside
the canonical `install.sh` which downloads from GitHub). It does
the same end-state as `get.sh` but every byte comes from the local
file — no network access required after the tarball is on disk.

Same flag surface as the GitHub installer, with one positional
argument: the path to the tarball. See `./install-local.sh --help`
on a host with the tarball unpacked, or `hack/install-local.sh
--help` in the source repo.

## Build from source

If you'd rather compile from source, or you need a custom patch:

### 0. Install build dependencies

You only need these on the machine where you build. Follow the
official install docs — these projects ship their own installers and
update them on every release.

- **Go 1.23+** — <https://go.dev/doc/install>
- **Node.js 20+ and npm** — <https://nodejs.org/en/download>
- **Docker** — <https://docs.docker.com/engine/install/> (only on
  machines that will run `pp agent`)

The `pp` binary itself is statically linked and has no runtime
dependencies beyond the Linux kernel.

**Only required if you plan to edit `proto/agent.proto`** (the
generated `proto/gen/*.pb.go` files are committed, so a fresh
`make build` works without these):
- `protoc` — <https://grpc.io/docs/protoc-installation/>
- `protoc-gen-go` and `protoc-gen-go-grpc` — install with:
  ```bash
  go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
  go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
  export PATH="$PATH:$(go env GOPATH)/bin"
  ```

Verify your toolchain:

```bash
go version          # go1.23+
node --version      # v20+
npm --version       # 10+
docker --version    # any recent
protoc --version    # libprotoc 3.x+   (optional, only for proto regen)
```

If you are behind a firewall and `npm install` is slow, point npm at a
mirror first:

```bash
npm config set registry https://registry.npmmirror.com
```

### 1. Build everything

```bash
make all           # builds bin/pp and web/dist
```

`make all` will:
1. Run `make proto` if `protoc` is on `PATH` (no-op otherwise — committed
   generated code is used).
2. Compile `bin/pp` (Go static binary).
3. Run `npm ci` in `web/` and produce `web/dist/`.

If you only want the Go binary (no UI bundle):

```bash
make build         # just bin/pp
```

`make build` also injects version metadata into the binary via `-ldflags`.
Override the version with `make build VERSION=v0.1.0`. Print the
resulting binary's version with `pp version`.

### 2. Start the Master

Every master setting (listen addresses, SQLite path, optional
GreptimeDB metrics sink) lives in a single YAML file. Copy the
sample, edit it, and point the binary at it:

```bash
cp pp-master.yaml /etc/pp/pp-master.yaml   # or anywhere on disk
$EDITOR /etc/pp/pp-master.yaml
./bin/pp master --config=/etc/pp/pp-master.yaml
```

If you skip `--config`, the master looks for `/etc/pp/pp-master.yaml`
by default. If that file is missing too, it falls back to built-in
defaults (`:8080` HTTP, `:7000` gRPC, `pumpkinpie.db` in the working
directory, no GreptimeDB) — useful for local smoke tests.

The master does not expose a single "proxy port" — every container
created on the cluster gets its own dedicated port in the range
**30000-32767** (see "Reverse-proxy URL scheme" below).

Open <http://localhost:8080/console/> in your browser. Use the language
selector at the bottom of the sidebar to switch between **English** and
**简体中文** — your choice is persisted in `localStorage`.

### 3. Start one or more Agents (on any machine with Docker)

Like the master, every agent setting (master gRPC address, node name,
state directory, Docker socket) lives in a single YAML file:

```bash
cp pp-agent.yaml /etc/pp/pp-agent.yaml   # or anywhere on disk
$EDITOR /etc/pp/pp-agent.yaml             # set `master:` to your master
./bin/pp agent --config=/etc/pp/pp-agent.yaml
```

If you skip `--config`, the agent looks for `/etc/pp/pp-agent.yaml`
by default. If that's missing too, it falls back to built-in defaults
(`pp-master.internal:7000`, name=hostname, platform-aware state_dir,
`/var/run/docker.sock`) — useful for local smoke tests.

Docker socket precedence (highest first):
1. `docker_sock:` in the YAML
2. `$DOCKER_SOCK` env var (legacy)
3. `$DOCKER_HOST` env var, `unix://` prefix only
4. `/var/run/docker.sock` (Linux built-in)

```bash
# Linux with standard Docker — no extra config needed
./bin/pp agent --config=/etc/pp/pp-agent.yaml

# Linux with rootless Docker — point the YAML's docker_sock at it
docker_sock: "/run/user/1000/docker.sock"

# macOS Docker Desktop
docker_sock: "$HOME/.docker/run/docker.sock"

# Or override at runtime via env var (precedence #2 above)
DOCKER_SOCK=$HOME/.docker/run/docker.sock ./bin/pp agent --config=...
```

Within ~5 seconds the node cards should appear in the dashboard with live
metrics.

### Subcommands

`pp` is a single binary that runs as either role:

```
pp master [--config=path]   # control plane: UI + API + gRPC + reverse proxy
pp agent  [--config=path]   # node agent: registers to master, hosts containers
pp version                  # print version, commit, build time
pp help                     # print usage
```

Both `master` and `agent` take a single `--config=<path>` flag (default
`/etc/pp/pp-master.yaml` / `/etc/pp/pp-agent.yaml`). Every other
setting lives in the YAML.

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

Under the hood, the Master accepts a TCP connection on the public port,
dials the agent host's published port (the agent registers via gRPC and
the master remembers its peer address), and `io.Copy`s bytes both ways.
This is protocol-agnostic — HTTP, WebSocket, HTTP/2, raw TCP services
like Postgres or Redis all flow through transparently.

> Note: the agent's published container ports are bound on `0.0.0.0`, so
> anything that can reach the agent host on those ports can talk to the
> container directly, bypassing the master. On untrusted networks you
> should firewall the agent host's `30000-32767` range to allow only
> the master's IP.

## Enabling GPUs on a node

GPU containers in pumpkinPie use Docker's `--gpus` machinery, which
needs the NVIDIA Container Toolkit installed and registered with the
Docker daemon on each GPU node.

### Host setup (per GPU host, Linux)

1. **NVIDIA driver** — `apt install nvidia-driver-535` (or the version
   your GPU needs), then reboot. Verify with `nvidia-smi`.

2. **NVIDIA Container Toolkit:**
   ```bash
   curl -fsSL https://nvidia.github.io/libnvidia-container/gpgkey \
     | gpg --dearmor -o /usr/share/keyrings/nvidia-container-toolkit-keyring.gpg
   curl -s -L https://nvidia.github.io/libnvidia-container/stable/deb/nvidia-container-toolkit.list \
     | sed 's#deb https://#deb [signed-by=/usr/share/keyrings/nvidia-container-toolkit-keyring.gpg] https://#g' \
     | sudo tee /etc/apt/sources.list.d/nvidia-container-toolkit.list
   sudo apt update && sudo apt install -y nvidia-container-toolkit
   ```

3. **Register the runtime with Docker:**
   ```bash
   sudo nvidia-ctk runtime configure --runtime=docker
   sudo systemctl restart docker
   ```

4. **Smoke test** the toolkit before relying on pumpkinPie:
   ```bash
   docker run --rm --gpus all nvidia/cuda:12.3.0-base-ubuntu22.04 nvidia-smi
   ```
   You should see the GPU table printed from inside the container.
   If it fails (e.g. `could not select device driver "" with capabilities: [[gpu]]`),
   the toolkit is not wired into Docker correctly — re-run step 3.

### How pumpkinPie schedules GPUs

- **Discovery** — the agent runs `nvidia-smi` every metrics interval
  and reports per-device usage/memory to the master. Hosts without
  `nvidia-smi` report zero GPUs.
- **Exclusive reservation** — the master keeps a `gpu_alloc` table
  with a composite primary key on `(node_id, gpu_index)`. When you
  create a container with `gpu_count: 2`, the master picks the lowest
  two free indices, persists the reservation, and sends them to the
  agent as `DeviceIDs` in the Docker `DeviceRequests` payload. The
  daemon then attaches exactly those GPUs to the container.
- **No accidental sharing** — a second container that asks for a GPU
  on the same node is scheduled onto a different node (or rejected if
  none has enough free GPUs). The rejection error lists how many
  online nodes were inspected and how many had enough free devices,
  so you can tell the difference between "all GPUs busy" and "all
  nodes offline".
- **Stopped containers release their GPUs** so other workloads can
  use them. Restarting a stopped container will try to re-reserve the
  exact same indices it was created with; if any of those are now
  held by another container, `POST /api/containers/:id/start` returns
  HTTP 409 and you'll need to delete + recreate the container to get
  a fresh assignment.
- **Failed creates release their GPUs** automatically — if the agent
  reports `ContainerCreated.error` (e.g. image pull failed, GPU runtime
  missing), the master frees the reservation so other containers can
  pick those devices up.

### What you'll see in the UI

- The **Nodes** page shows `2 / 4 GPU free` per node instead of just the
  total count.
- The **Containers** table appends `· GPU 0, 2` to the Resources column
  for each container holding GPUs.
- The **New Container** form's `GPU count` field is unchanged — the
  master always picks indices on your behalf. Pin to a specific node
  if you care which physical host runs it.

### Limits / known gaps

- **No MIG / time-slicing support** — each physical GPU is treated as
  one indivisible unit. If you've configured the toolkit to advertise
  N replicas of a GPU, `nvidia-smi -L` will report each replica as a
  separate device and pp will treat them independently.
- **No CUDA-version or compute-capability filtering** — `gpu_count` is
  a number, not a constraint on device capability. Use node-pinning if
  you need to target specific hardware.
- **No memory-MB constraint** — you can't request "a GPU with ≥24 GB
  free". Reservations are purely by device count.

## Forwarding metrics to GreptimeDB (optional)

By default the master keeps the latest snapshot of every node's CPU /
memory / disk / GPU / load metrics in SQLite and that's what the UI
reads. For long-term analytics you can additionally forward each
report to a [GreptimeDB](https://github.com/GreptimeTeam/greptimedb)
instance over its gRPC frontend (port `4001` by default).

The sink is **off by default** and the master starts and runs
identically with or without it. To enable it, fill in the
`greptime:` section of `pp-master.yaml`:

```yaml
greptime:
  url: "greptime.internal:4001"
  database: "public"
  table: "node_metrics"
  # username: "..."
  # password: "..."
```

All `greptime:` fields:

| Field | Default | Purpose |
|---|---|---|
| `url` | _empty_ (sink off) | gRPC endpoint of the GreptimeDB frontend. Accepts `host:port` or `http(s)://host:port`. |
| `database` | `public` | Logical database to write into. |
| `table` | `node_metrics` | Destination table. Auto-created on first insert. |
| `username` / `password` | _empty_ | Basic auth (GreptimeCloud sets these; standalone leaves them blank). |

### Schema

A single wide table — `node_metrics` by default — with the agent's
report fields as columns:

- **Tags** (indexed, low-cardinality): `node_id`, `node_name`
- **Fields** (scalars): `cpu_percent`, `cpu_cores`, `mem_used_bytes`,
  `mem_total_bytes`, `load1`, `gpu_count`, `gpu_usage_percent`,
  `gpu_mem_used_bytes`, `gpu_mem_total_bytes`
- **Fields** (JSON): `disks` (`[]DiskSample`), `gpus` (`[]GPUSample`)
- **Timestamp**: `ts` (TIMESTAMP_MILLISECOND)

Query examples (MySQL/Postgres frontend):

```sql
-- 5-minute rolling average CPU per node, last hour
SELECT node_id,
       avg(cpu_percent) AS avg_cpu
FROM   node_metrics
WHERE  ts > NOW() - INTERVAL '1' HOUR
GROUP  BY node_id, date_trunc('minute', ts)
ORDER  BY ts;

-- Top 10 disk-hungry nodes right now
SELECT node_id,
       json_extract_string(d, '$.path')    AS path,
       json_extract_double(d, '$.usage_percent') AS pct
FROM   node_metrics,
       unnest(json_to_array(disks)) AS a(d)
WHERE  ts > NOW() - INTERVAL '5' MINUTE
ORDER  BY pct DESC
LIMIT  10;
```

### Failure handling

The sink fails soft: an unreachable or misconfigured GreptimeDB logs
a warning at startup and every failed `Write` is logged at runtime,
but the master's SQLite snapshot (and therefore the UI) is never
affected. If the gRPC client fails to construct, the master logs the
error once and returns the same error from every `Write` until it
restarts — no retry loop, no exponential backoff, no unbounded
backlog. The internal sink channel drops oldest metrics on overflow
(the SQLite snapshot is still authoritative, so losing a sample for
the analytics side is preferable to blocking the agent gRPC loop).

### Persistence

GreptimeDB keeps a full history of every metric; SQLite keeps only the
latest. GreptimeDB is read by external tools (Grafana, ad-hoc SQL,
the `gorm` driver, etc.); the dashboard UI still reads from SQLite.

## Architecture notes

- **gRPC bidirectional stream** carries control traffic per agent —
  register, metrics, container lifecycle (create / start / stop /
  state). Proxy data flows out-of-band over direct TCP between the
  master and the agent host's published container port.
- **HTTP API** uses Gin; metrics are pushed to the browser over a
  WebSocket at `/api/ws`.
- **SQLite** (`modernc.org/sqlite`, pure Go) for metadata. No external
  database required.
- **Docker integration** is a tiny in-tree HTTP client against the
  Docker Engine API over the unix socket — no SDK churn, no cgo.
- **GPU detection** uses `nvidia-smi` (works with the NVIDIA container
  toolkit); on hosts without an NVIDIA GPU the field stays at 0.

## Running as a systemd service

If you used the [Install (recommended)](#install-recommended) section,
your `pp-master` / `pp-agent` units are already installed and running.
This section is for the source-build path: build the binary, drop it
in `/usr/local/bin`, then run the systemd install script.

Two unit files ship in [`contrib/systemd/`](contrib/systemd/):

| File | Purpose |
|---|---|
| `pp-master.service` | Central control plane (UI + API + gRPC + reverse proxy) |
| `pp-agent.service`  | Node agent (registers to master, hosts containers) |
| `install.sh`        | One-shot installer that renders the units and starts them |
| `uninstall.sh`      | Stop and remove the units |

### 1. Build and install the binary

```bash
make build
sudo cp bin/pp /usr/local/bin/pp
```

### 2. Install on the master host

```bash
sudo PP_MASTER_ADDR=10.0.0.1:7000 ./contrib/systemd/install.sh master
```

This will:
- create a system user `pp` (if missing)
- create `/var/lib/pp/` for the SQLite database
- render `pp-master.service` with the right paths
- `enable` and `start` it

The UI is now reachable at <http://10.0.0.1:8080/console/>.

### 3. Install on every worker host

```bash
sudo PP_MASTER_ADDR=10.0.0.1:7000 ./contrib/systemd/install.sh agent
```

The agent runs as `root` because it needs to access the Docker socket.
It connects to the master outbound, so no inbound ports need to be
opened on the worker firewall.

If your Docker socket is not at `/var/run/docker.sock` (rootless Docker,
macOS, custom path), edit `docker_sock:` in `/etc/pp/pp-agent.yaml` and
`sudo systemctl restart pp-agent`.

### 4. Day-to-day operations

```bash
# Status
sudo systemctl status pp-master
sudo systemctl status pp-agent

# Logs (follow)
sudo journalctl -u pp-master -f
sudo journalctl -u pp-agent -f

# Logs since last boot
sudo journalctl -u pp-agent -b

# Restart
sudo systemctl restart pp-master

# Disable autostart (but keep installed)
sudo systemctl disable pp-agent
```

### 5. Uninstall

```bash
sudo ./contrib/systemd/uninstall.sh        # removes both
# or
sudo ./contrib/systemd/uninstall.sh agent  # removes only the agent
```

The data directory and the `pp` user are **not** removed automatically —
see the script's output for cleanup hints.

### Customizing the units

The shipped units enable most of systemd's hardening options
(`NoNewPrivileges`, `ProtectSystem=strict`, `ProtectHome`,
`MemoryDenyWriteExecute`, etc.). If a unit fails to start because of a
denied syscall or filesystem access, look at
`journalctl -xeu pp-<role>` first. Common tweaks:

- **`ProtectSystem=strict` blocks the master from writing outside
  `ReadWritePaths=`** — add any extra data dir to `ReadWritePaths=`.
- **`MemoryDenyWriteExecute=true` blocks JITs** — pumpkinPie doesn't
  use one, so leave it on; if you add Go plugins later, drop it.
- **`RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6`** on the agent
  prevents the process from binding to other sockets; safe to keep.

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
│   │   ├── proxy/         # per-container TCP listener -> agent direct dial
│   │   ├── metrics/       # optional GreptimeDB metrics sink (no-op when off)
│   │   └── api/           # Gin HTTP handlers + WebSocket
│   └── agent/
│       ├── collector/     # CPU/mem/disk/GPU via gopsutil + nvidia-smi
│       ├── docker/        # Docker Engine HTTP client
│       └── agent.go       # gRPC client, create/stop, metrics loop
└── web/                   # Vue 3 + Vite + Element Plus
    ├── src/views/         # Dashboard / Nodes / Containers / NewContainer
    └── dist/              # built static assets
```

## License

MIT
