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
: "${PODIUM_TRANSPORT:=local}"
: "${PODIUM_MASTER_KEY_FILE:=$env_dir/master.key}"
# The same default docker-compose.dev.yml gives POSTGRES_PASSWORD, so a .env copied from
# .env.example — where the line is present and empty — still reaches the database that
# compose file just started, instead of failing authentication with an empty password.
: "${PODIUM_PG_PASSWORD:=podium}"
: "${PODIUM_DATABASE_URL:=postgres://podium:${PODIUM_PG_PASSWORD}@${pg_host}/podium}"
: "${PODIUM_AGENT_DATABASE_URL:=postgres://podium:${PODIUM_PG_PASSWORD}@${pg_host}/podium_agent}"
# Only once there is a secret key, for the same reason as the conductor below: the server
# refuses to start with PODIUM_S3_ENDPOINT set and no credentials, and a .env copied from
# .env.example has none. Unset means artifacts and log rollup are off, which is supported —
# the server says so at startup and every upload answers failed_precondition.
if [ -n "${PODIUM_S3_SECRET_KEY:-}" ]; then
	: "${PODIUM_S3_ENDPOINT:=127.0.0.1:${PODIUM_S3_PORT:-9000}}"
	: "${PODIUM_S3_BUCKET:=podium}"
	: "${PODIUM_S3_ACCESS_KEY:=podium}"
	: "${PODIUM_S3_USE_SSL:=false}"
fi
# Only once there is a token to present. The server refuses to start with
# PODIUM_AGENT_URL set and PODIUM_AGENT_TOKEN empty — a proxy that forwards an
# unauthenticated request into the conductor is worse than no proxy — and a .env copied
# from .env.example has the line but no value. Without one the stack comes up as a task
# runner with no Agent tab, which is a supported configuration and the quickstart's.
if [ -n "${PODIUM_AGENT_TOKEN:-}" ]; then
	: "${PODIUM_AGENT_URL:=http://127.0.0.1:8090}"
fi
# The WORKED EXAMPLE, deliberately, and not the profile this repository's own bot runs.
# examples/agent loads and runs on any node: one playbook, the base image, no credential and
# no skill library. ../playbooks is the real bot — a privileged node, a Docker daemon, a
# browser, a GitHub token and a skill out of ../skills — so a first `make stack-up` pointed
# there would come up fine and then fail every turn at provisioning, naming a node flag the
# newcomer has never set. The real deployment names it in .env instead, together with
# PODIUM_AGENT_SKILLS_DIR; those two travel as a pair, because a playbook whose skill is
# missing fails its turns. See deploy/.env.example and playbooks/README.md.
: "${PODIUM_AGENT_PROFILE_DIR:=$root/examples/agent}"
: "${PODIUM_NODE_DATA_DIR:=$STATE/node}"
: "${PODIUM_TS_STATE_DIR:=$STATE/tsnet}"

# THE ASSISTANT. `auto` in the .env means "this checkout's own build", resolved here because
# only this script knows which checkout is running — a worktree's absolute paths in a shared
# .env break the moment the worktree moves or is deleted.
#
# It stays an OPT-IN. Neither variable is defaulted, because the assistant runs a model as a
# child of podium-agent with no container around it (docs/security.md#5): turning that on
# because a build artifact happens to exist would be a security decision made by a Makefile.
# Unset means every turn is a task, which is what it has always meant.
#
# An `auto` that resolves to nothing is a hard error rather than a silent fall-back to
# container turns. Losing the assistant quietly is the failure this whole block exists to
# prevent: the stack comes up, answers every chat, and costs a container per message.
if [ "${PODIUM_AGENT_HOST_RUNTIME:-}" = auto ]; then
	PODIUM_AGENT_HOST_RUNTIME=$root/agent/runtime/dist/main.js
	[ -f "$PODIUM_AGENT_HOST_RUNTIME" ] || {
		echo "PODIUM_AGENT_HOST_RUNTIME=auto found no $PODIUM_AGENT_HOST_RUNTIME — run: make agent-runtime-dist" >&2
		exit 1
	}
fi
if [ "${PODIUM_AGENT_RUNNER_BIN:-}" = auto ]; then
	PODIUM_AGENT_RUNNER_BIN=$BIN/podium-runner
	[ -x "$PODIUM_AGENT_RUNNER_BIN" ] || {
		echo "PODIUM_AGENT_RUNNER_BIN=auto found no $PODIUM_AGENT_RUNNER_BIN — run: make build" >&2
		exit 1
	}
fi

# Where the server is reached, which differs by transport: a tailnet device name, or the
# local transport's loopback listener.
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
	: "${PODIUM_SERVER:=http://${PODIUM_LOCAL_LISTEN:-127.0.0.1:8080}}"
fi
: "${PODIUM_AGENT_SERVER:=$PODIUM_SERVER}"
: "${PODIUM_NODE_SERVER:=$PODIUM_SERVER}"
: "${PODIUM_NODE_TRANSPORT:=$PODIUM_TRANSPORT}"
# The local transport has one token and three processes that need it, which is why
# docker-compose.yml fans PODIUM_LOCAL_TOKEN out into these two. Doing it here as well
# keeps the host-binary path configured by the same one line of .env. Under tailnet
# there is no shared token and both land empty, which is what that transport wants.
: "${PODIUM_NODE_LOCAL_TOKEN:=${PODIUM_LOCAL_TOKEN:-}}"
: "${PODIUM_AGENT_API_TOKEN:=${PODIUM_LOCAL_TOKEN:-}}"
export PODIUM_TRANSPORT PODIUM_MASTER_KEY_FILE PODIUM_DATABASE_URL PODIUM_AGENT_DATABASE_URL \
	PODIUM_S3_ENDPOINT PODIUM_S3_BUCKET PODIUM_S3_ACCESS_KEY PODIUM_S3_USE_SSL \
	PODIUM_AGENT_URL PODIUM_AGENT_PROFILE_DIR PODIUM_NODE_DATA_DIR PODIUM_TS_STATE_DIR \
	PODIUM_SERVER PODIUM_AGENT_SERVER PODIUM_NODE_SERVER PODIUM_NODE_TRANSPORT \
	PODIUM_NODE_LOCAL_TOKEN PODIUM_AGENT_API_TOKEN
# Exported explicitly rather than relying on the `set -a` that read the file: an `auto` above
# was reassigned after that, and a variable this script resolved must reach the child whether
# the .env named it or the operator did.
export PODIUM_AGENT_HOST_RUNTIME PODIUM_AGENT_RUNNER_BIN

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
		# Said here rather than left to the conductor, because the server has to be
		# holding the same token before the Agent tab appears at all.
		if [ "$svc" = agent ] && [ -z "${PODIUM_AGENT_TOKEN:-}" ]; then
			echo "the conductor needs PODIUM_AGENT_TOKEN in $ENV_FILE — the server presents it" >&2
			echo "on every proxied call and the conductor accepts nothing else, so set one" >&2
			echo "value for both and restart the server: make stack-down S=server" >&2
			exit 1
		fi
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
