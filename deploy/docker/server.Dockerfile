# podium-server. GoReleaser builds the binary and copies it into this context, so there is no
# compile stage here: the image is the binary plus a certificate bundle.
#
# distroless/static has no shell, no package manager and no libc — a CGO_ENABLED=0 Go binary
# needs none of them — and it carries ca-certificates, which the server needs to reach an
# HTTPS object store and Tailscale's coordination server.
#
# Pinned by digest. A tag is a moving target and a base image is the part of a supply chain
# nobody looks at.
FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab

# The web UI is compiled into the binary (go:embed of web/dist), so there is nothing to serve
# from disk and no second stage to build it.
COPY podium-server /usr/local/bin/podium-server

# Unprivileged, unlike the node: the control plane touches Postgres, an object store and a
# network socket, and none of that wants root. 65532 is distroless's nonroot user.
USER 65532:65532

# The local transport's default listen address is loopback, which inside a container means
# nothing outside it can connect. A container deployment therefore needs BOTH
# PODIUM_LOCAL_LISTEN=0.0.0.0:8080 and PODIUM_LOCAL_ALLOW_UNSAFE_LISTEN=true — the transport
# trusts one static token and refuses a non-loopback address until an operator says the
# address is only reachable from inside a container — and it publishes the port on loopback
# of the host instead. The compose file does exactly that. Under the tailnet transport there
# is no host port at all: the server listens on 443 of its own Tailscale device.
EXPOSE 8080

ENTRYPOINT ["/usr/local/bin/podium-server"]
