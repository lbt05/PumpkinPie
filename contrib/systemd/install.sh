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
#   PP_DATA_DIR          data directory for SQLite + state (default: /var/lib/pp)
#   PP_STATE_DIR         agent: state directory (default: /var/lib/pp-agent)
#   PP_BIN               path to the pp binary (default: /usr/local/bin/pp)
#   PP_MASTER_ADDR       address agents dial (default: pp-master.internal:7000)
#   PP_NAME              agent: node name (default: %H)
#   PP_HTTP / PP_GRPC    master: bind addresses

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
else
  mkdir -p "$PP_STATE_DIR"
  chmod 0700 "$PP_STATE_DIR"
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

export PP_BIN PP_DATA_DIR PP_STATE_DIR PP_MASTER_ADDR PP_NAME
"$RENDER" "$ROLE" "$SELF_DIR/pp-$ROLE.service" > "$DST"

systemctl daemon-reload
systemctl enable "pp-$ROLE.service"
systemctl restart "pp-$ROLE.service"

echo "✔ installed and started pp-$ROLE.service"
echo "  status:   systemctl status pp-$ROLE"
echo "  logs:     journalctl -u pp-$ROLE -f"
echo "  stop:     systemctl stop pp-$ROLE"
