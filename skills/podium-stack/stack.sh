#!/usr/bin/env bash
# stack.sh — a complete, real Podium deployment built from THIS checkout, inside a podium-dev
# turn: podium-server, the conductor and a node in the turn's container; Postgres, the object
# store and every task the node runs in the turn's own `dind` sidecar.
#
#   stack.sh up        build the branch, start everything, open the UI at http://task:5173
#   stack.sh seed      Fernhill Cycle Co.: secrets, playbooks, routines, real conversations
#                      (one with image attachments), tasks in every state
#   stack.sh env       export lines for the CLI / Playwright
#   stack.sh status
#   stack.sh down
#
# Nothing is stubbed: the model is the real provider (this turn's own ANTHROPIC_API_KEY),
# artifacts go to a real bucket, tasks are real containers, turns run the branch's runtime.
# Only the business data is invented.
set -euo pipefail

die() { echo "stack: $*" >&2; exit 1; }
say() { echo "stack: $*"; }

root=$(git rev-parse --show-toplevel 2>/dev/null) || die "run inside the Podium checkout (/workspace/podium)"
[ -f "$root/deploy/run-host.sh" ] || die "$root is not a Podium checkout"
skill=$(cd "$(dirname "$0")" && pwd)
# Under /workspace on purpose: the node bind-mounts paths under its data dir into every task
# BY PATH, and the dind daemon resolves those in its own filesystem. /workspace is the one
# directory the turn and its dind sidecar both see at the same path.
state=${STACK_DIR:-/workspace/.stack}
envf=$state/.env
action=${1:-}

pg_image=pgvector/pgvector:pg16
s3_image=rustfs/rustfs:1.0.0-rc.5@sha256:c36b3efea3d1e503f1a2581abd0e7611e0e5820dd30e1850a52384b3fc52bda4
ui_port=5173

getenv() { sed -n "s/^$1=//p" "$envf" | tail -1; }
cli() { "$root/bin/podium" --server "$(getenv PODIUM_SERVER)" --token "$(getenv PODIUM_LOCAL_TOKEN)" "$@"; }
run_host() {
  env -i HOME="$HOME" PATH="$PATH" USER="${USER:-agent}" \
    PODIUM_ENV_FILE="$envf" PODIUM_STATE_DIR="$state" PODIUM_BIN_DIR="$root/bin" \
    sh "$root/deploy/run-host.sh" "$@"
}
# The turn container's own address on the task network. A task the nested node starts runs in
# dind's engine and leaves through dind's NAT onto that network; the name `task` does not
# resolve from there, the address does.
task_ip() { hostname -i | awk '{print $1}'; }

ensure_ctr() { # ensure_ctr <name> <docker run args…>
  local n=$1; shift
  docker ps --format '{{.Names}}' | grep -qx "$n" && return
  docker rm -f "$n" >/dev/null 2>&1 || true
  docker run -d --name "$n" "$@" >/dev/null
  say "started $n in dind"
}

up() {
  [ -n "${DOCKER_HOST:-}" ] && docker info >/dev/null 2>&1 ||
    die "no Docker daemon — this needs a podium-dev turn (docker: true), which sets DOCKER_HOST to the dind sidecar"
  command -v pgrep >/dev/null ||
    die "no pgrep — deploy/run-host.sh needs procps to find and stop its services; this image predates it (rebuild podium-agent-runtime-dev)"
  [ -n "${ANTHROPIC_API_KEY:-}" ] || say "warning: no ANTHROPIC_API_KEY in this turn — the stack comes up, conversations will not"
  mkdir -p "$state"

  # Postgres and the object store, as containers in dind, published on the sidecar: dind:5432
  # and dind:9000. They live and die with this turn; nothing outside it is touched.
  ensure_ctr podium-pg -p 5432:5432 -e POSTGRES_USER=podium -e POSTGRES_PASSWORD=podium \
    -e POSTGRES_DB=podium "$pg_image"
  ensure_ctr podium-s3 -p 9000:9000 -e RUSTFS_VOLUMES=/data -e RUSTFS_ADDRESS=0.0.0.0:9000 \
    -e RUSTFS_ACCESS_KEY=podium -e RUSTFS_SECRET_KEY=podiumdev -e RUSTFS_CONSOLE_ENABLE=false \
    -e RUSTFS_OBS_LOGGER_LEVEL=warn "$s3_image"
  local i
  for i in $(seq 60); do
    [ "$(docker exec podium-pg psql -qtA -U podium -d postgres -c 'select 1' 2>/dev/null)" = 1 ] && break
    [ "$i" = 60 ] && die "Postgres never answered — docker logs podium-pg"
    sleep 1
  done
  [ "$(docker exec podium-pg psql -qtA -U podium -d postgres -c "select 1 from pg_database where datname='podium_agent'")" = 1 ] ||
    docker exec podium-pg psql -qtA -U podium -d postgres -c 'create database podium_agent' >/dev/null
  for i in $(seq 30); do
    curl -s -o /dev/null -m 2 http://dind:9000/ && break
    [ "$i" = 30 ] && die "the object store never answered — docker logs podium-s3"
    sleep 1
  done

  # The branch's own binaries, UI and runtime. runner-embed first: a fresh clone has no
  # embedded runner and the node does not compile without one.
  say "building the branch (make runner-embed build agent-runtime-dist)…"
  (cd "$root" && make runner-embed build agent-runtime-dist) >"$state/build.log" 2>&1 ||
    die "build failed — tail $state/build.log"

  # The image delegated work runs in, built from this branch inside dind (there is no registry
  # to pull it from, and a turn changing the runtime must run its own). Rebuilt when HEAD moved.
  local rev have
  rev=$(git -C "$root" rev-parse --short HEAD)
  have=$(docker image inspect -f '{{ index .Config.Labels "org.opencontainers.image.revision" }}' podium-agent-runtime:dev 2>/dev/null || true)
  if [ "$have" != "$rev" ]; then
    say "building podium-agent-runtime:dev in dind at $rev (a few minutes the first time)…"
    docker build -q --build-arg VERSION=dev --build-arg REVISION="$rev" -t podium-agent-runtime:dev \
      -f "$root/agent/runtime/Dockerfile" "$root/agent/runtime" >"$state/runtime-image.log" 2>&1 ||
      die "runtime image build failed — tail $state/runtime-image.log"
  fi

  if [ ! -f "$envf" ]; then
    "$root/bin/podium-server" gen-master-key --out "$state/master.key" >/dev/null 2>&1
    tok() { head -c 24 /dev/urandom | od -An -tx1 | tr -d ' \n'; }
    (umask 077; cat >"$envf") <<EOF
# Nested Podium stack — generated by skills/podium-stack/stack.sh. Throwaway: it dies with the turn.
PODIUM_TRANSPORT=local
PODIUM_LOCAL_LISTEN=127.0.0.1:8080
PODIUM_SERVER=http://127.0.0.1:8080
PODIUM_MASTER_KEY_FILE=$state/master.key
PODIUM_DATABASE_URL=postgres://podium:podium@dind:5432/podium?sslmode=disable
PODIUM_AGENT_DATABASE_URL=postgres://podium:podium@dind:5432/podium_agent?sslmode=disable
PODIUM_LOCAL_TOKEN=$(tok)
PODIUM_AGENT_TOKEN=$(tok)
PODIUM_AGENT_API_TOKEN=$(tok)
PODIUM_S3_ENDPOINT=dind:9000
PODIUM_S3_BUCKET=podium
PODIUM_S3_ACCESS_KEY=podium
PODIUM_S3_SECRET_KEY=podiumdev
PODIUM_S3_USE_SSL=false
PODIUM_AGENT_HOST_RUNTIME=auto
PODIUM_AGENT_RUNNER_BIN=auto
PODIUM_NODE_DOCKER_HOST=$DOCKER_HOST
PODIUM_NODE_LABELS=nested
EOF
    say "wrote $envf"
  fi
  # The conductor listens on every interface so tasks in dind can reach it at this
  # container's address; the server still talks to it over loopback.
  local ip
  ip=$(task_ip)
  sed -i '/^PODIUM_AGENT_LISTEN=/d; /^PODIUM_AGENT_URL=/d; /^PODIUM_AGENT_TASK_URL=/d' "$envf"
  printf 'PODIUM_AGENT_LISTEN=0.0.0.0:8090\nPODIUM_AGENT_URL=http://127.0.0.1:8090\nPODIUM_AGENT_TASK_URL=http://%s:8090\n' "$ip" >>"$envf"

  run_host up server agent

  if [ ! -f "$state/node/identity.json" ]; then
    local enroll
    enroll=$(cli node enroll-token --label nested) || die "could not mint an enrollment token"
    sed -i '/^PODIUM_NODE_ENROLL_TOKEN=/d' "$envf"
    echo "PODIUM_NODE_ENROLL_TOKEN=$enroll" >>"$envf"
  fi
  run_host up node
  for i in $(seq 30); do
    cli nodes 2>/dev/null | awk 'NR > 1 && $3 == "online" { ok = 1 } END { exit !ok }' && break
    [ "$i" = 30 ] && die "the node never came online — see $state/log/node.log"
    sleep 2
  done
  sed -i '/^PODIUM_NODE_ENROLL_TOKEN=/d' "$envf"

  # The UI the browser drives: the branch's web/ under Vite, on every interface, its proxy
  # carrying the token so no prompt appears. The browser sidecar opens http://task:5173.
  if ! curl -s -o /dev/null -m 2 "http://127.0.0.1:$ui_port/"; then
    # Vite runs the branch's own config plus the one route its proxy may lack: GET
    # /artifacts/{id}, which every attachment and download uses. Without it Vite's SPA
    # fallback answers with index.html and every chat image renders broken — a defect of the
    # dev proxy, not of the change under test. Merged in, so a branch that has the route
    # already is unaffected. Under node_modules so it is never committed.
    local wrap=$root/web/node_modules/.podium-stack/vite.config.mts
    mkdir -p "$(dirname "$wrap")"
    cat >"$wrap" <<VITE
import { mergeConfig } from "vite";
import base from "$root/web/vite.config.ts";
const token = process.env.PODIUM_LOCAL_TOKEN ?? "";
export default mergeConfig(base, {
  root: "$root/web",
  server: { proxy: { "^/artifacts/": {
    target: process.env.PODIUM_SERVER,
    configure: (proxy) => proxy.on("proxyReq", (req) => { if (token) req.setHeader("authorization", "Bearer " + token); }),
  } } },
});
VITE
    # Detached with every descriptor redirected: a background child that keeps this
    # script's stdout makes whoever reads it — a turn's bash tool — wait for ever.
    (cd "$root/web" && exec env __VITE_ADDITIONAL_SERVER_ALLOWED_HOSTS=task \
      PODIUM_SERVER="$(getenv PODIUM_SERVER)" PODIUM_LOCAL_TOKEN="$(getenv PODIUM_LOCAL_TOKEN)" \
      setsid npx vite --config "$wrap" --host 0.0.0.0 --port $ui_port --strictPort) </dev/null >"$state/log/vite.log" 2>&1 &
    for i in $(seq 60); do
      curl -s -o /dev/null -m 2 "http://127.0.0.1:$ui_port/" && break
      [ "$i" = 60 ] && die "Vite never answered — tail $state/log/vite.log"
      sleep 1
    done
  fi
  say "up — UI for the browser: http://task:$ui_port   (API http://127.0.0.1:8080, logs $state/log/)"
}

case $action in
up) up ;;
seed)
  [ -f "$envf" ] || die "not up — stack.sh up first"
  STACK_STATE=$state STACK_ROOT=$root bash "$skill/seed.sh"
  ;;
env)
  [ -f "$envf" ] || die "not up"
  cat <<EOF
export PODIUM_SERVER=$(getenv PODIUM_SERVER)
export PODIUM_LOCAL_TOKEN=$(getenv PODIUM_LOCAL_TOKEN)
export PODIUM_TOKEN=$(getenv PODIUM_LOCAL_TOKEN)
export PODIUM_UI_URL=http://task:$ui_port
export PODIUM_CLI=$root/bin/podium
EOF
  ;;
status)
  [ -f "$envf" ] || die "not up"
  run_host status
  cli nodes 2>/dev/null | sed 's/^/  /' || true
  ;;
down)
  [ -f "$envf" ] && run_host down node agent server || true
  pkill -f "vite --host 0.0.0.0 --port $ui_port" 2>/dev/null || true
  docker rm -f podium-pg podium-s3 >/dev/null 2>&1 || true
  say "down"
  ;;
*) sed -n '2,13p' "$0" >&2; exit 2 ;;
esac
