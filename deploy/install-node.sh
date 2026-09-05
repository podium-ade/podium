#!/usr/bin/env bash
#
# install-node.sh — turn a Linux machine into a Podium worker.
#
#   curl -fsSL https://raw.githubusercontent.com/alvaroibarguen/podium/main/deploy/install-node.sh \
#     | sudo PODIUM_SERVER=https://podium.example.ts.net \
#            PODIUM_ENROLL_TOKEN=... \
#            TS_AUTHKEY=tskey-auth-... \
#            PODIUM_LABELS=linux/amd64 \
#            bash
#
# It checks the prerequisites, downloads the release binary for this machine's architecture,
# verifies it against the release's checksums.txt, writes /etc/podium/node.yaml, installs the
# systemd unit and waits for the node to come online.
#
# Nothing here is idempotent by accident: re-running it upgrades the binary and rewrites the
# config, and it never touches /var/lib/podium-node, which holds the node's identity.
#
# Read docs/security.md first. This daemon gets the Docker socket, which is root on this host.

set -euo pipefail

VERSION="${PODIUM_VERSION:-latest}"
SERVER="${PODIUM_SERVER:-${PODIUM_NODE_SERVER:-}}"
ENROLL_TOKEN="${PODIUM_ENROLL_TOKEN:-${PODIUM_NODE_ENROLL_TOKEN:-}}"
DEV_TOKEN="${PODIUM_DEV_TOKEN:-${PODIUM_NODE_DEV_TOKEN:-}}"
TS_AUTHKEY="${TS_AUTHKEY:-${PODIUM_NODE_TS_AUTHKEY:-}}"
LABELS="${PODIUM_LABELS:-${PODIUM_NODE_LABELS:-}}"
MAX_TASKS="${PODIUM_MAX_TASKS:-${PODIUM_NODE_MAX_TASKS:-4}}"
DATA_DIR="${PODIUM_DATA_DIR:-${PODIUM_NODE_DATA_DIR:-/var/lib/podium-node}}"
# The node's own /healthz, /readyz and /metrics. wait_online below polls it, so the installer
# and the daemon have to agree on it: it is written into node.yaml rather than left to the
# daemon's default.
METRICS_LISTEN="${PODIUM_METRICS_LISTEN:-${PODIUM_NODE_METRICS_LISTEN:-127.0.0.1:9091}}"
REPO="${PODIUM_REPO:-alvaroibarguen/podium}"
BASE_URL="${PODIUM_RELEASE_BASE_URL:-https://github.com/${REPO}/releases}"
RAW_URL="${PODIUM_RAW_BASE_URL:-https://raw.githubusercontent.com/${REPO}/main}"
BIN_DIR="${PODIUM_BIN_DIR:-/usr/local/bin}"
CONFIG_DIR="${PODIUM_CONFIG_DIR:-/etc/podium}"
UNIT_DIR="${PODIUM_UNIT_DIR:-/etc/systemd/system}"
WAIT_SECONDS="${PODIUM_WAIT_SECONDS:-90}"

# Set by download_and_verify, read by install_unit, removed on exit.
tmp=""
# Set by detect_platform and check_inputs.
GOOS=""; GOARCH=""; TRANSPORT=""

log()  { printf '==> %s\n' "$*" >&2; }
warn() { printf 'warning: %s\n' "$*" >&2; }
die()  { printf 'error: %s\n' "$*" >&2; exit 1; }

need() { command -v "$1" >/dev/null 2>&1 || die "$1 is required and is not installed"; }

# ---------------------------------------------------------------------------- prerequisites

check_root() {
  [ "$(id -u)" -eq 0 ] || die "run this as root (sudo)"
}

detect_platform() {
  local os arch
  os="$(uname -s)"
  [ "$os" = "Linux" ] || die "podium-node runs on Linux; this is $os. \
On macOS run ./bin/podium-node from a build instead."

  arch="$(uname -m)"
  case "$arch" in
    x86_64|amd64)  GOARCH=amd64 ;;
    aarch64|arm64) GOARCH=arm64 ;;
    *) die "unsupported architecture $arch (podium-node ships linux/amd64 and linux/arm64)" ;;
  esac
  GOOS=linux
  log "platform: ${GOOS}/${GOARCH}"
}

# Docker 24 is the floor: the node negotiates API 1.43, which 24.0 was the first to serve.
check_docker() {
  need docker
  local version major
  version="$(docker version --format '{{.Server.Version}}' 2>/dev/null || true)"
  [ -n "$version" ] || die "the Docker daemon is not reachable. \
Install Docker Engine 24+ and make sure it is running: https://docs.docker.com/engine/install/"

  major="${version%%.*}"
  case "$major" in
    ''|*[!0-9]*) warn "cannot parse Docker version '$version'; continuing" ;;
    *) [ "$major" -ge 24 ] || die "Docker $version is too old; podium-node needs 24 or newer \
(it negotiates Engine API 1.43)" ;;
  esac
  log "docker: $version"
}

# cgroup v2 is not optional: memory limits, PID limits and OOM reporting all read the
# unified hierarchy, and the node refuses to start without it rather than silently ignoring
# every resource limit a task asks for.
check_cgroup_v2() {
  local driver
  driver="$(docker info --format '{{.CgroupVersion}}' 2>/dev/null || true)"
  if [ "$driver" = "2" ]; then
    log "cgroup: v2"
    return
  fi
  if [ -f /sys/fs/cgroup/cgroup.controllers ]; then
    log "cgroup: v2 (from /sys/fs/cgroup)"
    return
  fi
  die "this machine is on cgroup v1. podium-node needs cgroup v2 — every resource limit and \
the OOM report depend on it. On most distributions: add systemd.unified_cgroup_hierarchy=1 to \
the kernel command line and reboot."
}

check_systemd() {
  [ -d /run/systemd/system ] || die "systemd is not running here; install the binary by hand \
and supervise it however this machine does that"
  need systemctl
}

# ---------------------------------------------------------------------------------- download

resolve_version() {
  if [ "$VERSION" != "latest" ]; then
    log "version: $VERSION"
    return
  fi
  # GitHub redirects /releases/latest to the tag; the tag is the last path segment.
  local location
  location="$(curl -fsSLI -o /dev/null -w '%{url_effective}' "${BASE_URL}/latest" 2>/dev/null || true)"
  VERSION="${location##*/}"
  [ -n "$VERSION" ] && [ "$VERSION" != "latest" ] \
    || die "cannot work out the latest release. Pass PODIUM_VERSION=vX.Y.Z explicitly."
  log "version: $VERSION (latest)"
}

# download_and_verify fetches the release archive and checks it against the release's own
# checksums.txt BEFORE anything is unpacked or installed.
download_and_verify() {
  need curl
  need tar
  need sha256sum

  local stripped archive url
  stripped="${VERSION#v}"
  archive="podium_${stripped}_${GOOS}_${GOARCH}.tar.gz"
  url="${BASE_URL}/download/${VERSION}"

  # Global, and cleaned on exit: install_unit takes the systemd unit out of the same
  # verified archive, so the unit and the binary are always the same release.
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' EXIT

  log "downloading ${archive}"
  curl -fsSL -o "${tmp}/${archive}" "${url}/${archive}" \
    || die "cannot download ${url}/${archive}"
  curl -fsSL -o "${tmp}/checksums.txt" "${url}/checksums.txt" \
    || die "cannot download ${url}/checksums.txt — refusing to install an unverified binary"

  log "verifying checksum"
  ( cd "$tmp" && grep " ${archive}\$" checksums.txt > expected.txt \
      && sha256sum --check --status expected.txt ) \
    || die "${archive} failed its checksum. Do not install it; something is wrong with the \
download or with the release."

  tar -xzf "${tmp}/${archive}" -C "$tmp"
  install -m 0755 "${tmp}/podium-node" "${BIN_DIR}/podium-node"
  install -m 0755 "${tmp}/podium" "${BIN_DIR}/podium"
  log "installed ${BIN_DIR}/podium-node and ${BIN_DIR}/podium"
}

# ------------------------------------------------------------------------------ configuration

check_inputs() {
  [ -n "$SERVER" ] || die "PODIUM_SERVER is required, for example \
PODIUM_SERVER=https://podium.<tailnet>.ts.net (or http://127.0.0.1:8080 for a dev server)"

  case "$SERVER" in
    https://*)
      TRANSPORT="${PODIUM_TRANSPORT:-tailnet}"
      [ -n "$TS_AUTHKEY" ] || warn "TS_AUTHKEY is not set. A tailnet worker needs a Tailscale \
auth key — reusable, pre-approved, tagged tag:podium-node — on its first run. This is NOT the \
same thing as PODIUM_ENROLL_TOKEN."
      ;;
    http://*)
      TRANSPORT="${PODIUM_TRANSPORT:-dev}"
      [ -n "$DEV_TOKEN" ] || die "an http:// control plane is the dev transport, which needs \
PODIUM_DEV_TOKEN (the server's own PODIUM_DEV_TOKEN)"
      ;;
    *) die "PODIUM_SERVER must be an http:// or https:// URL, got '$SERVER'" ;;
  esac

  if [ -z "$ENROLL_TOKEN" ] && [ ! -f "${DATA_DIR}/identity.json" ]; then
    die "PODIUM_ENROLL_TOKEN is required for a machine that has never enrolled. \
Mint one on the control plane: podium node enroll-token --label ${LABELS:-linux/${GOARCH}}"
  fi
}

write_config() {
  mkdir -p "$CONFIG_DIR"
  local out="${CONFIG_DIR}/node.yaml"

  # The config file holds the enrollment token and possibly the dev token, so it is 0600 and
  # it is written atomically: a half-written node.yaml would fail to parse and the daemon
  # would not start.
  local tmp
  tmp="$(mktemp "${CONFIG_DIR}/node.yaml.XXXXXX")"
  {
    echo "# Written by deploy/install-node.sh on $(date -u '+%Y-%m-%dT%H:%M:%SZ')."
    echo "# Every key here can also be set as PODIUM_NODE_<KEY>; see deploy/.env.example."
    echo "server: ${SERVER}"
    echo "transport: ${TRANSPORT}"
    echo "data_dir: ${DATA_DIR}"
    echo "max_tasks: ${MAX_TASKS}"
    echo "metrics_listen: ${METRICS_LISTEN}"
    if [ -n "$LABELS" ]; then
      echo "labels:"
      echo "$LABELS" | tr ',' '\n' | while IFS= read -r label; do
        [ -n "$label" ] && echo "  - ${label}"
      done
    fi
    if [ -n "$DEV_TOKEN" ]; then
      echo "dev_token: ${DEV_TOKEN}"
    fi
    if [ -n "$TS_AUTHKEY" ]; then
      echo "# Read on the first run only; ${DATA_DIR}/ts is the device identity afterwards."
      echo "ts_auth_key: ${TS_AUTHKEY}"
    fi
    if [ -n "$ENROLL_TOKEN" ]; then
      echo "# Single use. Remove it once the node has enrolled; ${DATA_DIR}/identity.json"
      echo "# is the identity from then on."
      echo "enroll_token: ${ENROLL_TOKEN}"
    fi
  } > "$tmp"
  chmod 0600 "$tmp"
  mv "$tmp" "$out"
  log "wrote ${out} (mode 0600)"
}

install_unit() {
  local unit="${UNIT_DIR}/podium-node.service"
  if [ -f "${tmp}/deploy/systemd/podium-node.service" ]; then
    # The release archive ships it, so it matches the binary that was just installed.
    install -m 0644 "${tmp}/deploy/systemd/podium-node.service" "$unit"
  elif [ -f "./deploy/systemd/podium-node.service" ]; then
    install -m 0644 ./deploy/systemd/podium-node.service "$unit"
  else
    log "fetching the systemd unit"
    curl -fsSL -o "$unit" "${RAW_URL}/deploy/systemd/podium-node.service" \
      || die "cannot fetch the systemd unit from ${RAW_URL}/deploy/systemd/podium-node.service"
    chmod 0644 "$unit"
  fi
  log "installed ${unit}"

  mkdir -p "$DATA_DIR"
  chmod 0700 "$DATA_DIR"

  systemctl daemon-reload
  systemctl enable podium-node >/dev/null
  systemctl restart podium-node
  log "started podium-node"
}

# ---------------------------------------------------------------------------------- readiness

# wait_online polls the node's own /readyz, which is 200 only while its stream to the control
# plane is up. That is the node's own answer to "am I online", and it needs no credential.
wait_online() {
  local deadline
  deadline=$(( $(date +%s) + WAIT_SECONDS ))
  log "waiting for the node to come online (up to ${WAIT_SECONDS}s)"
  while [ "$(date +%s)" -lt "$deadline" ]; do
    if curl -fsS -o /dev/null "http://${METRICS_LISTEN}/readyz" 2>/dev/null; then
      log "node is online"
      printf '\n'
      printf 'Done. Check it from the control plane:\n\n'
      printf '    podium nodes\n\n'
      printf 'Logs:    journalctl -u podium-node -f\n'
      printf 'Config:  %s/node.yaml\n' "$CONFIG_DIR"
      return 0
    fi
    if ! systemctl is-active --quiet podium-node; then
      journalctl -u podium-node --no-pager -n 40 >&2 || true
      die "podium-node stopped. The last 40 log lines are above."
    fi
    sleep 2
  done

  journalctl -u podium-node --no-pager -n 40 >&2 || true
  die "podium-node did not come online within ${WAIT_SECONDS}s (polling \
http://${METRICS_LISTEN}/readyz). The last 40 log lines are above."
}

main() {
  check_root
  detect_platform
  check_docker
  check_cgroup_v2
  check_systemd
  check_inputs
  resolve_version
  download_and_verify
  write_config
  install_unit
  wait_online
}

main "$@"
