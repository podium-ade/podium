# podium-agent-runtime — one turn of the Claude Agent SDK inside a Podium task container.
#
# Debian, not distroless, on purpose: the agent needs a shell, git and gh. Keep it slim. Do
# not add language toolchains "in case": this is the general-purpose image Podium ships, and
# a workflow that needs more builds its own `FROM podium-agent-runtime` (docs/agent.md).
# Dockerfile.dev beside it is the one exception, and it exists to build Podium itself.
#
# The base is pinned by digest. A tag is a moving target and a base image is the part of a
# supply chain nobody looks at.
#
# There is no CLAUDE_* or ANTHROPIC_* variable anywhere below, and no
# --dangerously-skip-permissions: the key arrives as a Podium secret with target: env, and
# the only permission decision is permissionMode in src/main.ts.
ARG NODE_IMAGE=node:22-bookworm-slim@sha256:83f487e0a63425e5b4d146fb5e5be574bcbe1b7b843d3ebafdd95eaf7767a7e5

# --------------------------------------------------------------------------------- build
FROM ${NODE_IMAGE} AS build

WORKDIR /src
RUN npm install --global pnpm@11.25.0

COPY package.json pnpm-lock.yaml pnpm-workspace.yaml ./
RUN pnpm install --frozen-lockfile

COPY tsconfig.json tsconfig.build.json ./
COPY src ./src
RUN pnpm run build

# Production dependencies only, hoisted so the image carries a plain node_modules rather
# than pnpm's symlink farm. Optional dependencies stay ENABLED: the SDK ships its native
# Claude Code binary as a platform optional dependency and the runtime cannot start without
# the one for this architecture.
RUN rm -rf node_modules && pnpm install --prod --frozen-lockfile --node-linker=hoisted

# ------------------------------------------------------------------------------- runtime
FROM ${NODE_IMAGE}

ARG VERSION=dev
ARG REVISION=none
LABEL org.opencontainers.image.title="podium-agent-runtime" \
      org.opencontainers.image.source="https://github.com/podium-ade/podium" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${REVISION}"

# git, gh, curl, jq and ripgrep are the agent's hands. gh comes from GitHub's own apt
# repository, with the keyring URL and repo line as documented at cli.github.com.
RUN set -eux; \
    apt-get update; \
    apt-get install -y --no-install-recommends ca-certificates curl git jq ripgrep; \
    install -d -m 0755 /etc/apt/keyrings; \
    curl -fsSL https://cli.github.com/packages/githubcli-archive-keyring.gpg \
        -o /etc/apt/keyrings/githubcli-archive-keyring.gpg; \
    chmod go+r /etc/apt/keyrings/githubcli-archive-keyring.gpg; \
    printf 'deb [arch=%s signed-by=/etc/apt/keyrings/githubcli-archive-keyring.gpg] https://cli.github.com/packages stable main\n' \
        "$(dpkg --print-architecture)" > /etc/apt/sources.list.d/github-cli.list; \
    apt-get update; \
    apt-get install -y --no-install-recommends gh; \
    rm -rf /var/lib/apt/lists/*; \
    git --version; gh --version; rg --version; jq --version

# uid 1000, called agent. The base image already owns 1000 (as `node`), so it is renamed
# rather than duplicated: two names for one uid is a trap, and every Podium agent image
# runs as 1000.
RUN set -eux; \
    existing="$(getent passwd 1000 | cut -d: -f1 || true)"; \
    if [ -z "$existing" ]; then \
        useradd --uid 1000 --create-home --shell /bin/bash agent; \
    elif [ "$existing" != agent ]; then \
        usermod --login agent --home /home/agent --move-home "$existing"; \
    fi; \
    install -d -o 1000 -g 1000 -m 0755 /workspace

COPY --from=build /src/dist /opt/podium-agent/dist
COPY --from=build /src/node_modules /opt/podium-agent/node_modules
COPY package.json /opt/podium-agent/package.json

# The harness. It is provider-agnostic — a turn picks its model with `provider/model` — and
# it is installed globally rather than as a dependency of the runtime because it is a
# program this code spawns, not a library it imports.
RUN set -eux; \
    npm install --global opencode-ai@1.18.29; \
    opencode --version

# Build-time assertion: if optional dependencies were skipped, or the wrong platform's
# package landed, this is where it shows rather than in a task that has already been
# scheduled.
RUN cd /opt/podium-agent && node -e "require('zod')"

ENV HOME=/home/agent \
    NODE_ENV=production

USER agent

# /workspace is the Podium workspace volume, mounted at run time.
# .podium/artifacts is created by main.ts on start.
WORKDIR /workspace

# No CMD: the Podium spec for an agent task names the image only, so the node runs the
# image's ENTRYPOINT behind podium-runner at PID 1 (docs/task-spec.md).
ENTRYPOINT ["node", "/opt/podium-agent/dist/main.js"]
