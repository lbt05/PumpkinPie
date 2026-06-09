#!/usr/bin/env bash
# uninstall.sh — remove pumpkinPie systemd units
#
# Usage: sudo ./contrib/systemd/uninstall.sh [master|agent]
# (no arg = remove both)

set -euo pipefail

remove_one() {
  local role="$1"
  local unit="pp-$role.service"
  if systemctl list-unit-files "$unit" >/dev/null 2>&1; then
    systemctl stop "$unit" 2>/dev/null || true
    systemctl disable "$unit" 2>/dev/null || true
    rm -f "/etc/systemd/system/$unit"
    echo "✔ removed $unit"
  else
    echo "  $unit not installed, skipping"
  fi
}

if [[ $EUID -ne 0 ]]; then
  echo "error: must run as root" >&2
  exit 1
fi

if [[ $# -eq 0 ]]; then
  remove_one master
  remove_one agent
else
  for role in "$@"; do
    remove_one "$role"
  done
fi

systemctl daemon-reload
echo
echo "Note: data directory /var/lib/pp was NOT removed. Run:"
echo "  sudo rm -rf /var/lib/pp   # to wipe SQLite + state"
echo "  sudo userdel pp           # to remove the master service user"
