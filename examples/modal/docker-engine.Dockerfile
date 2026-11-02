# An agent image with a Docker engine in it, for Modal's vm runtime: the sandbox starts the
# engine itself, so a turn that runs in the image can have sidecars beside it — a Docker
# daemon for `docker: true`, a headless Chrome for `browser: true`. Debian- or Ubuntu-based
# images; the engine is Docker's static build.
#
#   python examples/modal/publish_image.py examples/modal/docker-engine.Dockerfile \
#       --build-arg BASE=ghcr.io/podium-ade/podium-agent-runtime:0.8.0 \
#       --name podium-agent-runtime-docker:0.8.0
#
# A base in a private registry needs --registry-username; see publish_image.py.
ARG BASE=ghcr.io/podium-ade/podium-agent-runtime:0.8.0
FROM ${BASE}
USER root
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates curl iptables procps \
 && rm -rf /var/lib/apt/lists/*
RUN curl -fsSL https://download.docker.com/linux/static/stable/x86_64/docker-29.9.2.tgz | tar -xz -C /tmp \
 && install -m 0755 /tmp/docker/dockerd /tmp/docker/containerd /tmp/docker/containerd-shim-runc-v2 \
      /tmp/docker/runc /tmp/docker/ctr /tmp/docker/docker-init /tmp/docker/docker-proxy /usr/local/bin/ \
 && rm -rf /tmp/docker
