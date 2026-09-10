# podium, the CLI. Published so a CI job can submit a task without installing anything:
#
#   docker run --rm -e PODIUM_SERVER -e PODIUM_TOKEN ghcr.io/podium-ade/podium:latest \
#     run --image alpine:3 -- echo hello
#
# The CLI never talks to Docker — everything it knows comes from the control plane — so this
# image needs no socket, no privileges and no state.
FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab

# goreleaser's dockers_v2 builds ONE multi-arch image, so it cannot stage both architectures'
# binaries at the same path: the build context holds linux/amd64/<binary> and
# linux/arm64/<binary>, and $TARGETPLATFORM is how a single Dockerfile picks its own. buildx
# sets it per platform; the ARG only has to be declared to be usable.
#
# This is also why a hand-rolled `docker build` needs the same layout — a flat context fails
# here with `"/<binary>": not found`. See ../../docs/quickstart.md#building-the-images-yourself.
ARG TARGETPLATFORM
COPY $TARGETPLATFORM/podium /usr/local/bin/podium

USER 65532:65532
ENTRYPOINT ["/usr/local/bin/podium"]
