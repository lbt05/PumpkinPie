#!/usr/bin/env bash
# get.sh — install pumpkinPie from a GitHub Release.
#
# Usage:
#   curl -sSf https://raw.githubusercontent.com/lbt05/PumpkinPie/main/hack/get.sh | sudo bash -s -- master
#   curl -sSf https://raw.githubusercontent.com/lbt05/PumpkinPie/main/hack/get.sh | sudo bash -s -- agent
#
# Flags (after the role):
#   --version <vX.Y.Z>       pin a release (default: latest)
#   --prerelease             include pre-releases when resolving "latest"
#   --data-dir <path>        master: SQLite + state dir       (default: /var/lib/pp)
#   --state-dir <path>       agent: machine-id persistence    (default: /var/lib/pp-agent)
#   --bin <path>             where to install the binary      (default: /usr/local/bin/pp)
#   --http <addr>            master: HTTP listen addr         (default: 0.0.0.0:8080)
#   --grpc <addr>            master: gRPC listen addr         (default: 0.0.0.0:7000)
#   --master <host:port>     agent: master address to dial    (default: pp-master.internal:7000)
#   --name <hostname>        agent: node name                 (default: %H)
#   --no-systemd             install binary only, skip unit
#   --dry-run                show what would happen, don't install
#   -h, --help               this help
#
# Environment overrides (same names, no leading --):
#   PP_VERSION, PP_BIN, PP_DATA_DIR, PP_STATE_DIR, PP_HTTP, PP_GRPC,
#   PP_MASTER_ADDR, PP_NAME, PP_NO_SYSTEMD, PP_PRERELEASE
#
# Reference implementation only — keep this file POSIX-ish bash.
# Tested on: Ubuntu 20.04+, Debian 11+, RHEL 9+, Amazon Linux 2023,
# macOS (no systemd unit install).

set -euo pipefail
shopt -s nocasematch

# --- config defaults ---------------------------------------------------------
GITHUB_OWNER="${GITHUB_OWNER:-lbt05}"
GITHUB_REPO="${GITHUB_REPO:-PumpkinPie}"
PROJECT_NAME="${PROJECT_NAME:-pumpkinpie}"
ROLE=""
VERSION=""
PP_BIN="${PP_BIN:-/usr/local/bin/pp}"
PP_DATA_DIR="${PP_DATA_DIR:-/var/lib/pp}"
PP_STATE_DIR="${PP_STATE_DIR:-/var/lib/pp-agent}"
PP_HTTP="${PP_HTTP:-0.0.0.0:8080}"
PP_GRPC="${PP_GRPC:-0.0.0.0:7000}"
PP_MASTER_ADDR="${PP_MASTER_ADDR:-pp-master.internal:7000}"
PP_NAME="${PP_NAME:-}"
PP_NO_SYSTEMD="${PP_NO_SYSTEMD:-}"
PP_PRERELEASE="${PP_PRERELEASE:-}"
DRY_RUN="${PP_DRY_RUN:-}"

# --- helpers -----------------------------------------------------------------
log()  { printf '\033[1;36m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m==>\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[1;31m==>\033[0m %s\n' "$*" >&2; exit 1; }

usage() {
  cat <<'EOF'
get.sh — install pumpkinPie from a GitHub Release.

Usage:
  curl -sSf https://raw.githubusercontent.com/lbt05/PumpkinPie/main/hack/get.sh | sudo bash -s -- master
  curl -sSf https://raw.githubusercontent.com/lbt05/PumpkinPie/main/hack/get.sh | sudo bash -s -- agent

Flags (after the role):
  --version <vX.Y.Z>       pin a release (default: latest)
  --prerelease             include pre-releases when resolving "latest"
  --data-dir <path>        master: SQLite + state dir       (default: /var/lib/pp)
  --state-dir <path>       agent: machine-id persistence    (default: /var/lib/pp-agent)
  --bin <path>             where to install the binary      (default: /usr/local/bin/pp)
  --http <addr>            master: HTTP listen addr         (default: 0.0.0.0:8080)
  --grpc <addr>            master: gRPC listen addr         (default: 0.0.0.0:7000)
  --master <host:port>     agent: master address to dial    (default: pp-master.internal:7000)
  --name <hostname>        agent: node name                 (default: %H)
  --no-systemd             install binary only, skip unit
  --dry-run                show what would happen, don't install
  -h, --help               this help

Environment overrides (same names, no leading --):
  PP_VERSION, PP_BIN, PP_DATA_DIR, PP_STATE_DIR, PP_HTTP, PP_GRPC,
  PP_MASTER_ADDR, PP_NAME, PP_NO_SYSTEMD, PP_PRERELEASE
EOF
}

# --- arg parse ---------------------------------------------------------------
while [[ $# -gt 0 ]]; do
  case "$1" in
    master|agent) ROLE="$1"; shift ;;
    -h|--help) usage; exit 0 ;;
    --version)      VERSION="$2"; shift 2 ;;
    --bin)          PP_BIN="$2"; shift 2 ;;
    --data-dir)     PP_DATA_DIR="$2"; shift 2 ;;
    --state-dir)    PP_STATE_DIR="$2"; shift 2 ;;
    --http)         PP_HTTP="$2"; shift 2 ;;
    --grpc)         PP_GRPC="$2"; shift 2 ;;
    --master)       PP_MASTER_ADDR="$2"; shift 2 ;;
    --name)         PP_NAME="$2"; shift 2 ;;
    --no-systemd)   PP_NO_SYSTEMD=1; shift ;;
    --prerelease)   PP_PRERELEASE=1; shift ;;
    --dry-run)      DRY_RUN=1; shift ;;
    *) die "unknown flag: $1 (try --help)" ;;
  esac
done

# env overrides for everything except role/version
[[ -n "${PP_VERSION:-}"   ]] && VERSION="$PP_VERSION"
# Normalize PP_NO_SYSTEMD / PP_PRERELEASE to either "1" or empty so downstream
# `[[ "$X" != "1" ]]` checks work the same for `1`, `true`, `yes`, `on`.
for var in PP_NO_SYSTEMD PP_PRERELEASE; do
  case "${!var:-}" in
    1|true|TRUE|yes|YES|on|ON) printf -v "$var" '%s' 1 ;;
    *)                          printf -v "$var" '%s' '' ;;
  esac
done

[[ -z "$ROLE" ]] && die "missing role. usage: $0 master|agent [flags]"

# --- preflight ---------------------------------------------------------------
case "$(uname -s)" in
  Linux)  OS="linux" ;;
  Darwin) OS="darwin" ;;
  *) die "unsupported OS: $(uname -s). This installer targets Linux (systemd) and macOS (binary only)." ;;
esac

ARCH="$(uname -m)"
case "$ARCH" in
  x86_64|amd64)   GOARCH="amd64" ;;
  aarch64|arm64)  GOARCH="arm64" ;;
  *) die "unsupported arch: $ARCH" ;;
esac

if [[ "$OS" == "linux" && "$PP_NO_SYSTEMD" != "1" && $EUID -ne 0 ]]; then
  die "must be root to install systemd units. re-run with sudo, or pass --no-systemd."
fi

if [[ "$OS" == "linux" && "$PP_NO_SYSTEMD" != "1" ]] && ! command -v systemctl >/dev/null 2>&1; then
  warn "systemctl not found — falling back to --no-systemd (binary only)."
  PP_NO_SYSTEMD=1
fi

# --- temp workspace ----------------------------------------------------------
TMP="$(mktemp -d -t pp-install.XXXXXX)"
trap 'rm -rf "$TMP"' EXIT

# --- resolve release version -------------------------------------------------
if [[ -z "$VERSION" ]]; then
  if [[ "$PP_PRERELEASE" == "1" ]]; then
    log "resolving latest release (incl. pre-releases)"
    api="https://api.github.com/repos/${GITHUB_OWNER}/${GITHUB_REPO}/releases?per_page=1"
  else
    log "resolving latest release"
    api="https://api.github.com/repos/${GITHUB_OWNER}/${GITHUB_REPO}/releases/latest"
  fi

  # Capture the HTTP status separately so a 4xx doesn't kill the pipeline
  # under `set -euo pipefail`. Auth header is optional; if GITHUB_TOKEN is
  # exported we use it to dodge the unauthenticated rate limit.
  auth=()
  [[ -n "${GITHUB_TOKEN:-}" ]] && auth=(-H "Authorization: Bearer ${GITHUB_TOKEN}")
  http_code="$(curl -sSL -o "$TMP/release.json" -w '%{http_code}' \
    -H 'Accept: application/vnd.github+json' \
    ${auth[@]+"${auth[@]}"} "$api" || echo 000)"

  case "$http_code" in
    200)
      VERSION="$(sed -n 's/.*"tag_name":[[:space:]]*"\([^"]*\)".*/\1/p' \
        "$TMP/release.json" | head -n1)"
      if [[ -z "$VERSION" ]]; then
        if [[ "$PP_PRERELEASE" == "1" ]]; then
          die "no releases (stable or pre-release) found in ${GITHUB_OWNER}/${GITHUB_REPO}. Cut one or pass --version vX.Y.Z."
        else
          die "no stable releases found. Add --prerelease to include RC/beta tags, or pass --version vX.Y.Z."
        fi
      fi
      ;;
    404)
      if [[ "$PP_PRERELEASE" == "1" ]]; then
        die "${GITHUB_OWNER}/${GITHUB_REPO} not found, or has no releases at all. Pass --version vX.Y.Z."
      else
        die "${GITHUB_OWNER}/${GITHUB_REPO} has no published stable releases. Add --prerelease to include RC/beta tags, or pass --version vX.Y.Z."
      fi
      ;;
    403)
      die "GitHub API rate-limited (HTTP 403). Export GITHUB_TOKEN, or pass --version vX.Y.Z."
      ;;
    000)
      die "could not reach api.github.com (offline?). Pass --version vX.Y.Z."
      ;;
    *)
      die "unexpected HTTP $http_code from $api. Pass --version vX.Y.Z."
      ;;
  esac
fi
# Normalize: tag URLs use the `v` prefix, but goreleaser's default
# `{{ .Version }}` strips it for asset filenames. Track both forms.
case "$VERSION" in v*) ;; *) VERSION="v$VERSION" ;; esac
VERSION_NO_V="${VERSION#v}"
log "installing ${PROJECT_NAME} ${VERSION} (${ROLE}) on ${OS}/${GOARCH}"

# --- download + verify --------------------------------------------------------
[[ -n "$DRY_RUN" ]] && log "DRY RUN: would download to $TMP"

ASSET="${PROJECT_NAME}_${VERSION_NO_V}_${OS}_${GOARCH}.tar.gz"
BASE_URL="https://github.com/${GITHUB_OWNER}/${GITHUB_REPO}/releases/download/${VERSION}"
TARBALL="${TMP}/${ASSET}"
CHECKSUMS="${TMP}/checksums.txt"

log "downloading ${ASSET}"
if [[ -z "$DRY_RUN" ]]; then
  curl -sSfL --retry 3 -o "$TARBALL"   "${BASE_URL}/${ASSET}"
  curl -sSfL --retry 3 -o "$CHECKSUMS" "${BASE_URL}/${PROJECT_NAME}_${VERSION_NO_V}_checksums.txt"

  log "verifying SHA-256"
  # The checksums.txt GoReleaser produces has the form:
  #   <sha>  pumpkinpie_vX.Y.Z_linux_amd64.tar.gz
  expected="$(awk -v a="$ASSET" '$2==a {print $1}' "$CHECKSUMS")"
  if [[ -z "$expected" ]]; then
    die "no checksum found for $ASSET in checksums.txt"
  fi
  if command -v sha256sum >/dev/null 2>&1; then
    actual="$(sha256sum "$TARBALL" | awk '{print $1}')"
  elif command -v shasum >/dev/null 2>&1; then
    actual="$(shasum -a 256 "$TARBALL" | awk '{print $1}')"
  else
    die "neither sha256sum nor shasum found; cannot verify $ASSET"
  fi
  [[ "$expected" == "$actual" ]] || die "checksum mismatch:
  expected: $expected
  actual:   $actual"
fi

# --- extract + install binary -------------------------------------------------
log "extracting"
if [[ -z "$DRY_RUN" ]]; then
  tar -xzf "$TARBALL" -C "$TMP"
fi
BIN_SRC="$TMP/$PROJECT_NAME"
if [[ -z "$DRY_RUN" && ! -f "$BIN_SRC" ]]; then
  die "binary not found in archive: $BIN_SRC"
fi

if [[ -n "$DRY_RUN" ]]; then
  log "DRY RUN: would install $BIN_SRC -> $PP_BIN"
  exit 0
fi

log "installing binary -> $PP_BIN"
install -d -m 0755 "$(dirname "$PP_BIN")"
install -m 0755 "$BIN_SRC" "$PP_BIN"

# Show what we just installed — useful for support tickets.
log "installed:"
"$PP_BIN" version || true

# --- systemd unit ------------------------------------------------------------
if [[ "$OS" == "linux" && "$PP_NO_SYSTEMD" != "1" ]]; then
  UNIT_DIR="/etc/systemd/system"
  SERVICE_NAME="pp-${ROLE}.service"
  UNIT_TPL="$TMP/contrib/systemd/pp-${ROLE}.service"

  # Fall back to the templates bundled in the repo if the tarball
  # didn't include them (older releases or trimmed archives).
  if [[ ! -f "$UNIT_TPL" ]]; then
    UNIT_TPL=""
    for candidate in \
      "./contrib/systemd/pp-${ROLE}.service" \
      "/usr/local/share/pumpkinpie/contrib/systemd/pp-${ROLE}.service"; do
      if [[ -f "$candidate" ]]; then UNIT_TPL="$candidate"; break; fi
    done
  fi
  [[ -n "$UNIT_TPL" ]] || die "systemd unit template not found in archive or repo"

  if [[ "$ROLE" == "master" ]]; then
    # Create the system user once. Idempotent.
    if ! id -u pp >/dev/null 2>&1; then
      useradd --system --home "$PP_DATA_DIR" --shell /usr/sbin/nologin pp
    fi
    install -d -m 0755 -o pp -g pp "$PP_DATA_DIR"
  else
    install -d -m 0700 "$PP_STATE_DIR"
  fi

  # Render via the shared helper so the unit text is always identical
  # to what contrib/systemd/install.sh would produce.
  export PP_BIN PP_DATA_DIR PP_STATE_DIR PP_MASTER_ADDR PP_NAME
  export PP_HTTP PP_GRPC
  if [[ -x "$TMP/render-unit.sh" ]]; then
    "$TMP/render-unit.sh" "$ROLE" "$UNIT_TPL" > "$UNIT_DIR/$SERVICE_NAME"
  else
    # Fallback: inline render (kept in sync with hack/render-unit.sh).
    if [[ "$ROLE" == "master" ]]; then
      sed -e "s|/usr/local/bin/pp|$PP_BIN|g" \
          -e "s|/var/lib/pp|$PP_DATA_DIR|g" \
          -e "s|--http=0.0.0.0:8080|--http=$PP_HTTP|g" \
          -e "s|--grpc=0.0.0.0:7000|--grpc=$PP_GRPC|g" \
          "$UNIT_TPL" > "$UNIT_DIR/$SERVICE_NAME"
    else
      sed -e "s|/usr/local/bin/pp|$PP_BIN|g" \
          -e "s|--state-dir=/var/lib/pp-agent|--state-dir=$PP_STATE_DIR|g" \
          -e "s|--master=pp-master.internal:7000|--master=$PP_MASTER_ADDR|g" \
          -e "s|--name=%H|--name=$PP_NAME|g" \
          "$UNIT_TPL" > "$UNIT_DIR/$SERVICE_NAME"
    fi
  fi

  systemctl daemon-reload
  systemctl enable "$SERVICE_NAME"
  systemctl restart "$SERVICE_NAME"

  log "started systemd unit $SERVICE_NAME"
  log "next steps:"
  printf '    systemctl status %s\n' "$SERVICE_NAME"
  printf '    journalctl -u %s -f\n'    "$SERVICE_NAME"
  if [[ "$ROLE" == "master" ]]; then
    printf '    open http://%s/console/\n' "${PP_HTTP##*:}"
  else
    printf '    add this agent on the master via the Nodes page\n'
  fi
elif [[ "$OS" == "darwin" ]]; then
  log "macOS does not have systemd. Run manually:"
  if [[ "$ROLE" == "master" ]]; then
    printf '    %s master --http=%s --grpc=%s --db=%s/pp.db\n' \
      "$PP_BIN" "$PP_HTTP" "$PP_GRPC" "$PP_DATA_DIR"
  else
    printf '    %s agent --master=%s --name=<unique>\n' \
      "$PP_BIN" "$PP_MASTER_ADDR"
  fi
fi

# --- cleanup temp ------------------------------------------------------------
# (trap above handles it on EXIT)
log "done. installed $PROJECT_NAME $VERSION to $PP_BIN"
