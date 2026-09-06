#!/bin/sh
# Run podium-server, podium-agent and podium-node as HOST BINARIES, from the same .env
# `podium-server init` writes for the compose files.
#
#   make build
#   docker compose -f deploy/docker-compose.dev.yml --profile memory up -d --wait
#   ./bin/podium-server init --dir deploy
#   make stack-up
#
# Why this exists at all, when there are three compose files: under
# PODIUM_TRANSPORT=tailnet the server listens only on :443 of its own Tailscale device and
# has no port on the compose network, so the conductor cannot reach it from a sibling
# container. docker-compose.tailnet.yml says so where it defines the agent service, and
# leaves "run podium-agent on the host" as the instruction — this is that instruction, in a
# file. It is also the shortest loop when you are changing Go code, because it runs what
# `make build` just produced instead of an image.
#
# The .env is the compose-shaped one: it holds PODIUM_PG_PASSWORD, not PODIUM_DATABASE_URL,
# so the derivations the compose files do in YAML are done here in shell. Anything already
# exported wins, so a one-off `PODIUM_AGENT_PROFILE_DIR=... make stack-up` still works.
set -e

usage() {
	cat >&2 <<EOF
usage: deploy/run-host.sh (up|down|status) [server|agent|node ...]

  up      start the services (default: all three), waiting for each to be ready
  down    stop them
  status  what is running, and what each one answers

  PODIUM_ENV_FILE    default deploy/.env
  PODIUM_BIN_DIR     default bin
  PODIUM_STATE_DIR   default .podium — master.key is read from beside the .env
EOF
	exit 2
}

action=${1:-}
[ -n "$action" ] || usage
shift || true
services=${*:-server agent node}

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
# The one value that cannot come from the file, because it names the file.
ENV_FILE=${PODIUM_ENV_FILE:-$root/deploy/.env}
if [ ! -r "$ENV_FILE" ]; then
	echo "no .env at $ENV_FILE — run: ${PODIUM_BIN_DIR:-$root/bin}/podium-server init --dir $(dirname "$ENV_FILE")" >&2
	exit 1
fi
# Values first, so anything already in the environment overrides the file.
saved=$(export -p)
set -a
. "$ENV_FILE"
set +a
eval "$saved"

# Read after the file, so the file can set them like anything else.
BIN=${PODIUM_BIN_DIR:-$root/bin}
STATE=${PODIUM_STATE_DIR:-$root/.podium}
RUN=$STATE/run
LOG=$STATE/log
# How long `down` waits for a service to actually exit. The server is the slow one: it tears
# down a tsnet device, which takes seconds rather than milliseconds.
STOP_TIMEOUT=30

# --- the derivations the compose files do in YAML -------------------------------------------
env_dir=$(dirname "$ENV_FILE")
pg_host=127.0.0.1:${PODIUM_PG_PORT:-5432}
: "${PODIUM_TRANSPORT:=dev}"
: "${PODIUM_MASTER_KEY_FILE:=$env_dir/master.key}"
: "${PODIUM_DATABASE_URL:=postgres://podium:${PODIUM_PG_PASSWORD}@${pg_host}/podium}"
: "${PODIUM_AGENT_DATABASE_URL:=postgres://podium:${PODIUM_PG_PASSWORD}@${pg_host}/podium_agent}"
: "${PODIUM_S3_ENDPOINT:=127.0.0.1:${PODIUM_S3_PORT:-9000}}"
: "${PODIUM_S3_BUCKET:=podium}"
: "${PODIUM_S3_ACCESS_KEY:=podium}"
: "${PODIUM_S3_USE_SSL:=false}"
: "${PODIUM_AGENT_URL:=http://127.0.0.1:8090}"
: "${PODIUM_AGENT_PROFILE_DIR:=$root/examples/agent}"
: "${PODIUM_NODE_DATA_DIR:=$STATE/node}"
: "${PODIUM_TS_STATE_DIR:=$STATE/tsnet}"

# Where the server is reached, which differs by transport: a tailnet device name, or the
# dev transport's loopback listener.
if [ "$PODIUM_TRANSPORT" = tailnet ]; then
	# PODIUM_TAILNET is only ever used to build this URL, so a deployment that names the
	# server outright — a CNAME, a device that is not called `podium` — needs neither.
	if [ -z "${PODIUM_SERVER:-}" ]; then
		[ -n "${PODIUM_TAILNET:-}" ] || {
			echo "PODIUM_TRANSPORT=tailnet needs PODIUM_TAILNET (or PODIUM_SERVER) in $ENV_FILE" >&2
			exit 1
		}
		PODIUM_SERVER=https://podium.${PODIUM_TAILNET}.ts.net
	fi
else
	: "${PODIUM_SERVER:=http://${PODIUM_DEV_LISTEN:-127.0.0.1:8080}}"
fi
: "${PODIUM_AGENT_SERVER:=$PODIUM_SERVER}"
: "${PODIUM_NODE_SERVER:=$PODIUM_SERVER}"
: "${PODIUM_NODE_TRANSPORT:=$PODIUM_TRANSPORT}"
export PODIUM_TRANSPORT PODIUM_MASTER_KEY_FILE PODIUM_DATABASE_URL PODIUM_AGENT_DATABASE_URL \
	PODIUM_S3_ENDPOINT PODIUM_S3_BUCKET PODIUM_S3_ACCESS_KEY PODIUM_S3_USE_SSL \
	PODIUM_AGENT_URL PODIUM_AGENT_PROFILE_DIR PODIUM_NODE_DATA_DIR PODIUM_TS_STATE_DIR \
	PODIUM_SERVER PODIUM_AGENT_SERVER PODIUM_NODE_SERVER PODIUM_NODE_TRANSPORT

# health_url names where a service says it is ready. The node has no HTTP surface unless it
# was given a metrics listener, so it is started and not waited on.
health_url() {
	case $1 in
	server) echo "$PODIUM_SERVER/healthz" ;;
	agent) echo "$PODIUM_AGENT_URL/readyz" ;;
	*) echo "" ;;
	esac
}

start_one() {
	svc=$1
	bin=$BIN/podium-$svc
	[ -x "$bin" ] || { echo "no $bin — run: make build" >&2; exit 1; }
	if pgrep -f "^$bin" >/dev/null 2>&1; then
		echo "  $svc already running"
		return
	fi
	mkdir -p "$RUN" "$LOG"
	# nohup so it outlives this shell; the log is the only place its output goes.
	#
	# The subshell is the background job and execs the service over itself, rather than
	# backgrounding inside it: `( cmd & )` leaves the subshell running as the service's
	# parent, holding whatever stdout this script was given. Under `make stack-up` that is
	# a pipe, and a pipe with a writer still open is a `make` that never returns — three
	# services up, healthy, and the command hanging until the last one exits. Redirecting
	# stdin as well keeps a service from reading the terminal it no longer has.
	( cd "$root" && exec nohup "$bin" $EXTRA_ARGS </dev/null >>"$LOG/$svc.log" 2>&1 ) &
	pgrep -f "^$bin" >"$RUN/$svc.pid" 2>/dev/null || true
	url=$(health_url "$svc")
	if [ -n "$url" ]; then
		# tsnet needs ~15s of warm-up before its device answers, which the retries cover.
		if curl -fsS --retry 45 --retry-delay 2 --retry-all-errors --retry-connrefused \
			-m 10 -o /dev/null "$url"; then
			echo "  $svc ready ($url)"
		else
			echo "  $svc did not become ready — see $LOG/$svc.log" >&2
			exit 1
		fi
	else
		echo "  $svc started (pid $(cat "$RUN/$svc.pid" 2>/dev/null))"
	fi
}

case $action in
up)
	for svc in $services; do
		EXTRA_ARGS=
		[ "$svc" = node ] && EXTRA_ARGS=${PODIUM_NODE_ARGS:-}
		start_one "$svc"
	done
	echo "up. logs in $LOG"
	;;
down)
	# Stopped newest-first so the node deregisters before the server goes. Matched on the
	# full path, so another checkout's build of the same binary is left alone.
	for svc in $(echo "$services" | tr ' ' '\n' | tail -r 2>/dev/null || echo "$services"); do
		if pkill -f "^$BIN/podium-$svc" 2>/dev/null; then
			# pkill returns when the signal is sent, not when the process is gone, and a
			# service still shutting down still matches the pattern `up` tests. Waiting is
			# what makes `stack-down && stack-up` a restart: without it `up` finds the
			# dying process, reports "already running", starts nothing, and leaves the
			# conductor with no control plane behind it.
			waited=0
			while pgrep -f "^$BIN/podium-$svc" >/dev/null 2>&1; do
				waited=$((waited + 1))
				if [ "$waited" -gt "$STOP_TIMEOUT" ]; then
					echo "  $svc did not exit within ${STOP_TIMEOUT}s" >&2
					exit 1
				fi
				sleep 1
			done
			echo "  stopped $svc"
		else
			echo "  $svc was not running"
		fi
		rm -f "$RUN/$svc.pid"
	done
	;;
status)
	for svc in $services; do
		if pgrep -f "^$BIN/podium-$svc" >/dev/null 2>&1; then
			url=$(health_url "$svc")
			code=$([ -n "$url" ] && curl -s -o /dev/null -w '%{http_code}' -m 5 "$url" || echo "-")
			printf '  %-7s running  %s %s\n' "$svc" "${url:-no http surface}" "$code"
		else
			printf '  %-7s stopped\n' "$svc"
		fi
	done
	;;
*) usage ;;
esac
