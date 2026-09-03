# podium, the CLI. Published so a CI job can submit a task without installing anything:
#
#   docker run --rm -e PODIUM_SERVER -e PODIUM_TOKEN ghcr.io/alvaroibarguen/podium:latest \
#     run --image alpine:3 -- echo hello
#
# The CLI never talks to Docker — everything it knows comes from the control plane — so this
# image needs no socket, no privileges and no state.
FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab

COPY podium /usr/local/bin/podium

USER 65532:65532
ENTRYPOINT ["/usr/local/bin/podium"]
