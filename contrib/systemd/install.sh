#!/usr/bin/env bash
# install.sh — install pumpkinPie systemd units from a pre-existing
# binary. For the canonical "download + install" path, use
# hack/get.sh instead. This script is the dev / source-build path
# used by `make install-systemd-{master,agent}`.
#
# Usage:
#   sudo ./contrib/systemd/install.sh master
#   sudo ./contrib/systemd/install.sh agent
#
# Environment overrides:
#   PP_USER              OS user that runs the master (default: pp)
#   PP_CONFIG            master: path to the YAML config file
#                        (default: /etc/pp/pp-master.yaml)
#   PP_AGENT_CONFIG      agent: path to the YAML config file
#                        (default: /etc/pp/pp-agent.yaml)
#   PP_DATA_DIR          master: data directory for SQLite + state
#                        (default: /var/lib/pp)
#   PP_STATE_DIR         agent: state directory (default: /var/lib/pp-agent)
#   PP_BIN               path to the pp binary (default: /usr/local/bin/pp)
#   PP_MASTER_ADDR       address agents dial (default: pp-master.internal:7000)
#   PP_NAME              agent: node name (default: %H)

set -euo pipefail

ROLE="${1:-}"
[[ "$ROLE" == "master" || "$ROLE" == "agent" ]] || { echo "usage: $0 <master|agent>"; exit 2; }

if [[ ! -x "${PP_BIN:-/usr/local/bin/pp}" ]]; then
  echo "error: ${PP_BIN:-/usr/local/bin/pp} not found or not executable" >&2
  echo "build and install first:  make build && sudo cp bin/pp ${PP_BIN:-/usr/local/bin/pp}" >&2
  exit 1
fi

if [[ $EUID -ne 0 ]]; then
  echo "error: must run as root (sudo $0 $ROLE)" >&2
  exit 1
fi

PP_USER="${PP_USER:-pp}"
PP_CONFIG="${PP_CONFIG:-/etc/pp/pp-master.yaml}"
PP_AGENT_CONFIG="${PP_AGENT_CONFIG:-/etc/pp/pp-agent.yaml}"
PP_DATA_DIR="${PP_DATA_DIR:-/var/lib/pp}"
PP_STATE_DIR="${PP_STATE_DIR:-/var/lib/pp-agent}"
PP_BIN="${PP_BIN:-/usr/local/bin/pp}"
PP_MASTER_ADDR="${PP_MASTER_ADDR:-pp-master.internal:7000}"
PP_NAME="${PP_NAME:-%H}"

# Create dedicated user for master (agent runs as root for Docker access).
if [[ "$ROLE" == "master" ]]; then
  if ! id -u "$PP_USER" >/dev/null 2>&1; then
    useradd --system --home "$PP_DATA_DIR" --shell /usr/sbin/nologin "$PP_USER"
  fi
  mkdir -p "$PP_DATA_DIR"
  chown -R "$PP_USER:$PP_USER" "$PP_DATA_DIR"

  # Drop the config file's directory and write a default YAML on first
  # install so the master starts with sane defaults. Operators can then
  # edit /etc/pp/pp-master.yaml to customise listen addresses, the
  # SQLite path, or enable the GreptimeDB sink.
  PP_CONFIG_DIR="$(dirname "$PP_CONFIG")"
  mkdir -p "$PP_CONFIG_DIR"
  if [[ ! -f "$PP_CONFIG" ]]; then
    SAMPLE=""
    for candidate in \
      "$PP_BIN.pp-master.yaml" \
      "/usr/local/share/pumpkinpie/pp-master.yaml" \
      "./pp-master.yaml"; do
      if [[ -f "$candidate" ]]; then SAMPLE="$candidate"; break; fi
    done
    if [[ -n "$SAMPLE" ]]; then
      install -m 0644 "$SAMPLE" "$PP_CONFIG"
    else
      # Last-resort inline default. Kept in sync with pp-master.yaml
      # in the repo so the binary works out-of-the-box even when the
      # sample file is missing (older tarballs).
      cat > "$PP_CONFIG" <<'EOF'
# pumpkinPie master configuration.
# See README for the full schema and examples.
http: ":8080"
grpc: ":7000"
db: "/var/lib/pp/pp.db"
# Optional: forward metrics to GreptimeDB (leave url empty to disable).
greptime:
  url: ""
  database: "public"
  table: "node_metrics"
EOF
    fi
    echo "  wrote default config to $PP_CONFIG (edit then restart pp-master)"
  fi
else
  mkdir -p "$PP_STATE_DIR"
  chmod 0700 "$PP_STATE_DIR"

  # Drop a default pp-agent.yaml on first install so the agent starts
  # with sane defaults. Operators edit /etc/pp/pp-agent.yaml to point
  # `master:` at the right address, override the node name, etc.
  PP_AGENT_CONFIG_DIR="$(dirname "$PP_AGENT_CONFIG")"
  mkdir -p "$PP_AGENT_CONFIG_DIR"
  if [[ ! -f "$PP_AGENT_CONFIG" ]]; then
    SAMPLE=""
    for candidate in \
      "$PP_BIN.pp-agent.yaml" \
      "/usr/local/share/pumpkinpie/pp-agent.yaml" \
      "./pp-agent.yaml"; do
      if [[ -f "$candidate" ]]; then SAMPLE="$candidate"; break; fi
    done
    if [[ -n "$SAMPLE" ]]; then
      install -m 0644 "$SAMPLE" "$PP_AGENT_CONFIG"
    else
      # Last-resort inline default. Kept in sync with pp-agent.yaml in
      # the repo so the binary works out-of-the-box even when the
      # sample file is missing (older tarballs).
      cat > "$PP_AGENT_CONFIG" <<EOF
# pumpkinPie agent configuration.
# See README for the full schema and examples.
master: "$PP_MASTER_ADDR"
name: "$PP_NAME"
state_dir: "$PP_STATE_DIR"
docker_sock: "/var/run/docker.sock"
EOF
    fi
    echo "  wrote default config to $PP_AGENT_CONFIG (edit then restart pp-agent)"
  fi
fi

# Render the unit via the shared helper, then install it.
DST="/etc/systemd/system/pp-$ROLE.service"
SELF_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
RENDER="$SELF_DIR/../../hack/render-unit.sh"
[[ -x "$RENDER" ]] || RENDER="$SELF_DIR/../hack/render-unit.sh"
if [[ ! -x "$RENDER" ]]; then
  echo "error: render-unit.sh not found next to install.sh" >&2
  exit 1
fi

export PP_BIN PP_CONFIG PP_AGENT_CONFIG PP_DATA_DIR PP_STATE_DIR PP_MASTER_ADDR PP_NAME
"$RENDER" "$ROLE" "$SELF_DIR/pp-$ROLE.service" > "$DST"

systemctl daemon-reload
systemctl enable "pp-$ROLE.service"
systemctl restart "pp-$ROLE.service"

echo "✔ installed and started pp-$ROLE.service"
echo "  status:   systemctl status pp-$ROLE"
echo "  logs:     journalctl -u pp-$ROLE -f"
echo "  stop:     systemctl stop pp-$ROLE"
if [[ "$ROLE" == "master" && -f "$PP_CONFIG" ]]; then
  echo "  config:   $PP_CONFIG"
elif [[ "$ROLE" == "agent" && -f "$PP_AGENT_CONFIG" ]]; then
  echo "  config:   $PP_AGENT_CONFIG"
fi