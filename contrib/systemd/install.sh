#!/usr/bin/env bash
# install.sh — install pumpkinPie systemd units
#
# Usage:
#   sudo ./contrib/systemd/install.sh master
#   sudo ./contrib/systemd/install.sh agent
#
# Environment overrides:
#   PP_USER              OS user that runs the master (default: pp)
#   PP_DATA_DIR          data directory for SQLite + state (default: /var/lib/pp)
#   PP_BIN               path to the pp binary (default: /usr/local/bin/pp)
#   PP_MASTER_ADDR       address agents dial (default: pp-master.internal:7000)

set -euo pipefail

ROLE="${1:-}"
[[ "$ROLE" == "master" || "$ROLE" == "agent" ]] || { echo "usage: $0 <master|agent>"; exit 2; }

PP_USER="${PP_USER:-pp}"
PP_DATA_DIR="${PP_DATA_DIR:-/var/lib/pp}"
PP_BIN="${PP_BIN:-/usr/local/bin/pp}"
PP_MASTER_ADDR="${PP_MASTER_ADDR:-pp-master.internal:7000}"

if [[ ! -x "$PP_BIN" ]]; then
  echo "error: $PP_BIN not found or not executable" >&2
  echo "build and install first:  make build && sudo cp bin/pp $PP_BIN" >&2
  exit 1
fi

if [[ $EUID -ne 0 ]]; then
  echo "error: must run as root (sudo $0 $ROLE)" >&2
  exit 1
fi

# Create dedicated user for master (agent runs as root for Docker access).
if [[ "$ROLE" == "master" ]]; then
  if ! id -u "$PP_USER" >/dev/null 2>&1; then
    useradd --system --home "$PP_DATA_DIR" --shell /usr/sbin/nologin "$PP_USER"
  fi
fi

mkdir -p "$PP_DATA_DIR"
chown -R "$PP_USER:$PP_USER" "$PP_DATA_DIR"

# Render the unit with the right paths, then install it.
SRC="$(dirname "$0")/pp-$ROLE.service"
DST="/etc/systemd/system/pp-$ROLE.service"

sed -e "s|/usr/local/bin/pp|$PP_BIN|g" \
    -e "s|/var/lib/pp|$PP_DATA_DIR|g" \
    -e "s|pp-master.internal:7000|$PP_MASTER_ADDR|g" \
    "$SRC" > "$DST"

systemctl daemon-reload
systemctl enable "pp-$ROLE.service"
systemctl restart "pp-$ROLE.service"

echo "✔ installed and started pp-$ROLE.service"
echo "  status:   systemctl status pp-$ROLE"
echo "  logs:     journalctl -u pp-$ROLE -f"
echo "  stop:     systemctl stop pp-$ROLE"
