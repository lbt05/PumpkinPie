#!/usr/bin/env bash
# install-local.sh — install pumpkinPie from a local tarball (no GitHub).
#
# Use this when GitHub Releases isn't reachable (firewall, air-gapped
# environment, behind a corporate proxy, etc.). Same end state as
# hack/get.sh, but every byte comes from the local file you point it
# at — no network access required after the tarball is on disk.
#
# Usage:
#   sudo ./install-local.sh master /path/to/pumpkinpie_vX.Y.Z_linux_amd64.tar.gz
#   sudo ./install-local.sh agent  /path/to/pumpkinpie_vX.Y.Z_linux_amd64.tar.gz
#
# The tarball is the standard artifact produced by
#   make release-snapshot    # ./dist/pumpkinpie_*_linux_amd64.tar.gz
# or downloaded from https://github.com/lbt05/PumpkinPie/releases
# on a machine that does have GitHub access, then sneakernet'd over.
#
# Reference implementation only — keep this file POSIX-ish bash.
# Tested on: Ubuntu 20.04+, Debian 11+, RHEL 9+, Amazon Linux 2023,
# macOS (binary only, no systemd unit install).

set -euo pipefail
shopt -s nocasematch

# --- config defaults ---------------------------------------------------------
ROLE=""
TARBALL=""
PP_BIN="${PP_BIN:-/usr/local/bin/pp}"
PP_CONFIG="${PP_CONFIG:-/etc/pp/pp-master.yaml}"
PP_AGENT_CONFIG="${PP_AGENT_CONFIG:-/etc/pp/pp-agent.yaml}"
PP_DATA_DIR="${PP_DATA_DIR:-/var/lib/pp}"
PP_STATE_DIR="${PP_STATE_DIR:-/var/lib/pp-agent}"
PP_MASTER_ADDR="${PP_MASTER_ADDR:-pp-master.internal:7000}"
PP_NAME="${PP_NAME:-}"
PP_NO_SYSTEMD="${PP_NO_SYSTEMD:-}"
DRY_RUN="${PP_DRY_RUN:-}"

# --- helpers -----------------------------------------------------------------
log()  { printf '\033[1;36m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m==>\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[1;31m==>\033[0m %s\n' "$*" >&2; exit 1; }

usage() {
  cat <<'EOF'
install-local.sh — install pumpkinPie from a local tarball (no GitHub).

Usage:
  sudo ./install-local.sh master <path-to-tarball>
  sudo ./install-local.sh agent  <path-to-tarball>

  e.g. sudo ./install-local.sh master ~/Downloads/pumpkinpie_v0.1.0_linux_amd64.tar.gz

Arguments:
  <role>                 master or agent
  <path-to-tarball>      the .tar.gz file produced by `make release-snapshot`
                         (or downloaded from GitHub Releases)

Flags (after the role and tarball):
  --bin <path>             where to install the binary        (default: /usr/local/bin/pp)
  --config <path>          master: path to pp-master.yaml      (default: /etc/pp/pp-master.yaml)
  --agent-config <path>    agent: path to pp-agent.yaml       (default: /etc/pp/pp-agent.yaml)
  --data-dir <path>        master: SQLite + state dir         (default: /var/lib/pp)
  --state-dir <path>       agent: machine-id persistence      (default: /var/lib/pp-agent)
  --master-ip <host:port>  agent: master address to dial      (default: pp-master.internal:7000)
  --name <hostname>        agent: node name                   (default: %H)
  --no-systemd             install binary only, skip unit
  --dry-run                show what would happen, don't install
  -h, --help               this help

Environment overrides (same names, no leading --):
  PP_BIN, PP_CONFIG, PP_AGENT_CONFIG, PP_DATA_DIR, PP_STATE_DIR,
  PP_MASTER_ADDR, PP_NAME, PP_NO_SYSTEMD, PP_DRY_RUN
EOF
}

# --- arg parse ---------------------------------------------------------------
if [[ $# -lt 1 ]]; then
  usage
  exit 2
fi

# First arg: role (or -h/--help)
case "${1:-}" in
  master|agent) ROLE="$1"; shift ;;
  -h|--help) usage; exit 0 ;;
  *) die "first argument must be 'master' or 'agent' (got '$1')" ;;
esac

# Second arg: tarball
TARBALL="${1:-}"
if [[ -z "$TARBALL" ]]; then
  die "missing tarball path. usage: $0 <master|agent> <path-to-tarball>"
fi
shift

# Remaining args: flags. Both "--flag value" and "--flag=value" forms
# are accepted (the latter is friendlier for shell variable expansion
# and matches what get.sh does).
while [[ $# -gt 0 ]]; do
  case "$1" in
    -h|--help) usage; exit 0 ;;
    --bin)          PP_BIN="$2"; shift 2 ;;
    --bin=*)        PP_BIN="${1#*=}"; shift ;;
    --config)       PP_CONFIG="$2"; shift 2 ;;
    --config=*)     PP_CONFIG="${1#*=}"; shift ;;
    --agent-config) PP_AGENT_CONFIG="$2"; shift 2 ;;
    --agent-config=*) PP_AGENT_CONFIG="${1#*=}"; shift ;;
    --data-dir)     PP_DATA_DIR="$2"; shift 2 ;;
    --data-dir=*)   PP_DATA_DIR="${1#*=}"; shift ;;
    --state-dir)    PP_STATE_DIR="$2"; shift 2 ;;
    --state-dir=*)  PP_STATE_DIR="${1#*=}"; shift ;;
    --master-ip)    PP_MASTER_ADDR="$2"; shift 2 ;;
    --master-ip=*)  PP_MASTER_ADDR="${1#*=}"; shift ;;
    --name)         PP_NAME="$2"; shift 2 ;;
    --name=*)       PP_NAME="${1#*=}"; shift ;;
    --no-systemd)   PP_NO_SYSTEMD=1; shift ;;
    --dry-run)      DRY_RUN=1; shift ;;
    *) die "unknown flag: $1 (try --help)" ;;
  esac
done

# Normalize booleans so `1`, `true`, `yes`, `on` all behave the same.
for var in PP_NO_SYSTEMD DRY_RUN; do
  case "${!var:-}" in
    1|true|TRUE|yes|YES|on|ON) printf -v "$var" '%s' 1 ;;
    *)                          printf -v "$var" '%s' '' ;;
  esac
done

# --- preflight ---------------------------------------------------------------
if [[ ! -f "$TARBALL" ]]; then
  die "tarball not found: $TARBALL"
fi

case "$(uname -s)" in
  Linux)  OS="linux" ;;
  Darwin) OS="darwin" ;;
  *) die "unsupported OS: $(uname -s). This installer targets Linux (systemd) and macOS (binary only)." ;;
esac

if [[ "$OS" == "linux" && "$PP_NO_SYSTEMD" != "1" && $EUID -ne 0 ]]; then
  die "must be root to install systemd units. re-run with sudo, or pass --no-systemd."
fi

if [[ "$OS" == "linux" && "$PP_NO_SYSTEMD" != "1" ]] && ! command -v systemctl >/dev/null 2>&1; then
  warn "systemctl not found — falling back to --no-systemd (binary only)."
  PP_NO_SYSTEMD=1
fi

# --- temp workspace ----------------------------------------------------------
TMP="$(mktemp -d -t pp-local-install.XXXXXX)"
trap 'rm -rf "$TMP"' EXIT

log "extracting $TARBALL"
if [[ -n "$DRY_RUN" ]]; then
  warn "DRY RUN: skipping extraction"
else
  tar -xzf "$TARBALL" -C "$TMP"
fi

# Locate the standard GoReleaser layout inside the tarball.
BIN_SRC="$TMP/pumpkinpie"
UNIT_TPL="$TMP/contrib/systemd/pp-${ROLE}.service"
SAMPLE_CFG="$TMP/pp-${ROLE}.yaml"

if [[ -z "$DRY_RUN" ]]; then
  if [[ ! -f "$BIN_SRC" ]]; then
    die "binary not found in tarball: pumpkinpie (expected at the tarball root)"
  fi
  if [[ "$OS" == "linux" && "$PP_NO_SYSTEMD" != "1" && ! -f "$UNIT_TPL" ]]; then
    die "systemd unit not found in tarball: $UNIT_TPL"
  fi
fi

# --- install binary ----------------------------------------------------------
if [[ -n "$DRY_RUN" ]]; then
  log "DRY RUN: would install $BIN_SRC -> $PP_BIN"
else
  log "installing binary -> $PP_BIN"
  install -d -m 0755 "$(dirname "$PP_BIN")"
  install -m 0755 "$BIN_SRC" "$PP_BIN"
  log "installed:"
  "$PP_BIN" version || true
fi

# --- generate config file (first-install only) -------------------------------
# write_config <destination-path> <role>
#   - Skips if the file already exists (operator edits are preserved).
#   - Uses the sample YAML from the tarball if present.
#   - Falls back to an inline default if the sample is missing (older
#     tarballs that didn't ship the YAML).
write_config() {
  local dest="$1"
  local role="$2"
  local sample="$TMP/pp-${role}.yaml"

  if [[ -f "$dest" ]]; then
    log "config already exists at $dest (leaving untouched)"
    return 0
  fi

  if [[ -n "$DRY_RUN" ]]; then
    log "DRY RUN: would write default config to $dest"
    return 0
  fi

  install -d -m 0755 "$(dirname "$dest")"

  if [[ -f "$sample" ]]; then
    install -m 0644 "$sample" "$dest"
    log "wrote default config to $dest (from tarball sample)"
  else
    # Last-resort inline default. Kept in sync with the repo's
    # pp-<role>.yaml so the binary works out-of-the-box even when
    # the tarball didn't ship one.
    if [[ "$role" == "master" ]]; then
      cat > "$dest" <<'YAML'
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
YAML
    else
      cat > "$dest" <<YAML
# pumpkinPie agent configuration.
# See README for the full schema and examples.
master: "$PP_MASTER_ADDR"
name: "$PP_NAME"
state_dir: "$PP_STATE_DIR"
docker_sock: "/var/run/docker.sock"
YAML
    fi
    log "wrote default config to $dest (inline fallback)"
  fi
}

if [[ "$ROLE" == "master" ]]; then
  write_config "$PP_CONFIG" "master"
  # Idempotent: create the system user and the data dir on first install.
  # Skipped on macOS — there's no `useradd` and the operator runs the
  # master under their own user account.
  if [[ -z "$DRY_RUN" && "$OS" == "linux" ]]; then
    if ! id -u pp >/dev/null 2>&1; then
      log "creating system user 'pp'"
      useradd --system --home "$PP_DATA_DIR" --shell /usr/sbin/nologin pp
    fi
    install -d -m 0755 -o pp -g pp "$PP_DATA_DIR"
  fi
else
  write_config "$PP_AGENT_CONFIG" "agent"
  if [[ -z "$DRY_RUN" && "$OS" == "linux" ]]; then
    install -d -m 0700 "$PP_STATE_DIR"
  fi
fi

# --- systemd unit ------------------------------------------------------------
if [[ "$OS" == "linux" && "$PP_NO_SYSTEMD" != "1" ]]; then
  UNIT_DIR="/etc/systemd/system"
  SERVICE_NAME="pp-${ROLE}.service"
  DST="$UNIT_DIR/$SERVICE_NAME"

  if [[ -n "$DRY_RUN" ]]; then
    log "DRY RUN: would render and install $DST"
  else
    log "installing systemd unit -> $DST"
    if [[ "$ROLE" == "master" ]]; then
      sed -e "s|/usr/local/bin/pp|$PP_BIN|g" \
          -e "s|/etc/pp/pp-master.yaml|$PP_CONFIG|g" \
          -e "s|/var/lib/pp|$PP_DATA_DIR|g" \
          "$UNIT_TPL" > "$DST"
    else
      sed -e "s|/usr/local/bin/pp|$PP_BIN|g" \
          -e "s|/etc/pp/pp-agent.yaml|$PP_AGENT_CONFIG|g" \
          -e "s|/var/lib/pp-agent|$PP_STATE_DIR|g" \
          "$UNIT_TPL" > "$DST"
    fi
    chmod 0644 "$DST"

    systemctl daemon-reload
    systemctl enable "$SERVICE_NAME"
    systemctl restart "$SERVICE_NAME"

    log "started systemd unit $SERVICE_NAME"
  fi
elif [[ "$OS" == "darwin" ]]; then
  log "macOS does not have systemd. Run manually:"
  if [[ "$ROLE" == "master" ]]; then
    printf '    edit %s then: %s master --config=%s\n' "$PP_CONFIG" "$PP_BIN" "$PP_CONFIG"
  else
    printf '    edit %s then: %s agent --config=%s\n' "$PP_AGENT_CONFIG" "$PP_BIN" "$PP_AGENT_CONFIG"
  fi
fi

# --- summary -----------------------------------------------------------------
log "done. installed $ROLE to $PP_BIN"
if [[ -n "$DRY_RUN" ]]; then
  warn "DRY RUN — no changes were made"
fi