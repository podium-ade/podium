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

COPY podium-agent /usr/local/bin/podium-agent

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
