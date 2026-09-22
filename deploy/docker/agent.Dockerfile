# podium-agent, the conductor. GoReleaser builds the binaries and copies them into this
# context, so there is no compile stage here: the image is two binaries on top of the runtime.
#
# FROM the runtime image, and NOT distroless/static as this image used to be. The conductor
# answers a CONVERSATION in its own process by forking the agent runtime — that is what
# PODIUM_AGENT_HOST_RUNTIME names, and it is a Node program. A distroless base carries no
# Node and no harness, so a conductor built on one can only ever schedule tasks: with
# default_playbook gone, a Slack thread it cannot infer a playbook for is refused with
# "no playbook matches this", and there is no configuration that fixes it. Basing this image
# on podium-agent-runtime is what makes host turns possible in a container deployment, and it
# is the same extension point docs/agent.md hands everybody else.
#
# The cost is honest and worth naming: a bigger image, with a shell and a package manager,
# where the distroless base had neither. The conductor still runs unprivileged, still never
# touches Docker and still never reads the master key.
#
# RUNTIME_IMAGE is pinned BY DIGEST by the release workflow, to the bytes the `agent-runtime`
# job just pushed — exactly what podium-agent-runtime-dev does, and for the same reason: a
# tag is whatever it resolves to by the time the next job starts. The default below is only
# for a hand-rolled build.
ARG RUNTIME_IMAGE=ghcr.io/podium-ade/podium-agent-runtime:latest
FROM ${RUNTIME_IMAGE}

# goreleaser's dockers_v2 builds ONE multi-arch image, so it cannot stage both architectures'
# binaries at the same path: the build context holds linux/amd64/<binary> and
# linux/arm64/<binary>, and $TARGETPLATFORM is how a single Dockerfile picks its own. buildx
# sets it per platform; the ARG only has to be declared to be usable.
#
# This is also why a hand-rolled `docker build` needs the same layout — a flat context fails
# here with `"/<binary>": not found`. See ../../docs/quickstart.md#building-the-images-yourself.
ARG TARGETPLATFORM
COPY $TARGETPLATFORM/podium-agent /usr/local/bin/podium-agent

# podium-runner is how a host turn says anything at all: the runtime writes its events through
# it. A task gets one bind-mounted by the node it runs on; a host turn has no node to do that,
# so the binary has to be in this image. PODIUM_AGENT_RUNNER_BIN below is its path, and a host
# runtime without it refuses to start.
COPY $TARGETPLATFORM/podium-runner /usr/local/bin/podium-runner

# A STARTER AGENT PROFILE, at the path PODIUM_AGENT_PROFILE_DIR already defaults to. The
# conductor refuses to start without a profile directory holding profile.yaml, and there is no
# sensible way for a compose file to conjure a tree of YAML and prompts. Shipping a starter
# in the image is what makes `docker compose up` enough on a machine that has never seen this
# repository.
#
# It is a blank bot, not this repository's own: one playbook, on the one runtime image Podium
# publishes, holding no credential of its own. Compose bind-mounts the operator's tree over
# /etc/podium/agent when PODIUM_AGENT_PROFILE_HOST is a path; unset, a named volume gets a
# copy of this starter on first up. The conductor reads the directory and never writes it.
#
# goreleaser puts examples/agent into the build context via `extra_files` in .goreleaser.yaml.
# Both have to change together or this COPY fails the build, which is the failure mode you
# want.
COPY examples/agent /etc/podium/agent

# Host turns, ON by default, because an image that carries the runtime and does not use it is
# the confusing half. The runtime image put its entrypoint at this path and leaves `node` on
# PATH, which is what PODIUM_AGENT_HOST_NODE defaults to. An operator who wants every turn to
# be a container task again sets PODIUM_AGENT_HOST_RUNTIME to the empty string.
ENV PODIUM_AGENT_HOST_RUNTIME=/opt/podium-agent/dist/main.js \
    PODIUM_AGENT_RUNNER_BIN=/usr/local/bin/podium-runner

# uid 1000, the `agent` user the runtime image already owns and every Podium agent image runs
# as. The distroless 65532 went with the old base; a bind-mounted profile directory has to be
# readable by 1000 now.
USER 1000:1000

# The runtime image's WORKDIR is /workspace, which is a task's mount and means nothing to a
# conductor. A host turn makes its own working directory under PODIUM_AGENT_HOST_DIR.
WORKDIR /

# PODIUM_AGENT_LISTEN defaults to loopback, which inside a container means nothing outside it
# can connect. A container deployment sets PODIUM_AGENT_LISTEN=0.0.0.0:8090 and publishes
# NOTHING on the host: podium-server reaches it over the compose network and proxies the
# AgentService behind its own identity middleware.
EXPOSE 8090

ENTRYPOINT ["/usr/local/bin/podium-agent"]
