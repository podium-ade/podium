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

# A DEFAULT AGENT PROFILE, at the path PODIUM_AGENT_PROFILE_DIR already defaults to. The
# conductor refuses to start without a profile directory holding profile.yaml, and there is no
# sensible way for a compose file to conjure a tree of YAML and prompts — which meant every
# deployment began by copying examples/agent out of a clone. Shipping the worked example in
# the image instead is what makes `docker compose up` enough.
#
# It is the example, not this repository's own bot: one playbook, on the one runtime image
# Podium publishes, holding no credential of its own. Mount your own over /etc/podium/agent to
# replace it, which is what a real deployment does — and note the conductor reads this
# directory and never writes it.
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
