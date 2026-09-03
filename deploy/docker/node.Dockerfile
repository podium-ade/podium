# podium-node. GoReleaser builds the binary and copies it into this context.
#
# Two things differ from the server image and both are deliberate:
#
#  1. It runs as ROOT. The daemon's whole job is to drive /var/run/docker.sock, and anything
#     that can talk to that socket can start a privileged container and own the host. A
#     dedicated user in the docker group is the same power with a longer name. See
#     docs/security.md — a node is a machine you are willing to let arbitrary containers run
#     on, and the container boundary here is not a security boundary.
#  2. It needs ca-certificates, for an https:// control plane and for Tailscale. distroless
#     /static carries them.
#
# The two Linux podium-runner builds are embedded in the binary itself, so nothing else has
# to be copied in for tasks to run.
FROM gcr.io/distroless/static-debian12:latest@sha256:d75cdd72874d4790092fcb1b058493ecf6bb5bf2b2b897045b00ff01d91843f2

COPY podium-node /usr/local/bin/podium-node

# identity.json (the node key, issued once and never reissued), each task's state and the
# node's own tsnet state live here. Mount a volume: losing it means a new enrollment token.
VOLUME ["/var/lib/podium-node"]
ENV PODIUM_NODE_DATA_DIR=/var/lib/podium-node

# /healthz, /readyz and /metrics. Bound to loopback by default, so publish it deliberately or
# set PODIUM_NODE_METRICS_LISTEN=0.0.0.0:9091 if you scrape from elsewhere.
EXPOSE 9091

ENTRYPOINT ["/usr/local/bin/podium-node"]
