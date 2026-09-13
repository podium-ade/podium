# podium-agent, the conductor. GoReleaser builds the binary and copies it into this context,
# so there is no compile stage here: the image is the binary plus a certificate bundle.
#
# distroless/static has no shell, no package manager and no libc — a CGO_ENABLED=0 Go binary
# needs none of them — and it carries ca-certificates, which the conductor needs to reach
# Slack over TLS and, from step 20, Linear.
#
# Pinned by digest, to the same base as the server. A tag is a moving target and a base image
# is the part of a supply chain nobody looks at.
FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab

# goreleaser's dockers_v2 builds ONE multi-arch image, so it cannot stage both architectures'
# binaries at the same path: the build context holds linux/amd64/<binary> and
# linux/arm64/<binary>, and $TARGETPLATFORM is how a single Dockerfile picks its own. buildx
# sets it per platform; the ARG only has to be declared to be usable.
#
# This is also why a hand-rolled `docker build` needs the same layout — a flat context fails
# here with `"/<binary>": not found`. See ../../docs/quickstart.md#building-the-images-yourself.
ARG TARGETPLATFORM
COPY $TARGETPLATFORM/podium-agent /usr/local/bin/podium-agent

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

# Unprivileged. The conductor talks to Postgres, the Podium API and Slack, and none of that
# wants root. It never touches Docker and never reads the master key. 65532 is distroless's
# nonroot user, and it is why PODIUM_AGENT_PROFILE_DIR is mounted read-only.
USER 65532:65532

# PODIUM_AGENT_LISTEN defaults to loopback, which inside a container means nothing outside it
# can connect. A container deployment sets PODIUM_AGENT_LISTEN=0.0.0.0:8090 and publishes
# NOTHING on the host: podium-server reaches it over the compose network and proxies the
# AgentService behind its own identity middleware.
EXPOSE 8090

ENTRYPOINT ["/usr/local/bin/podium-agent"]
