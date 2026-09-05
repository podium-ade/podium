# Running the agent runtime by hand

`agent/runtime/` builds a container image that runs **one turn** of the Claude Agent SDK inside an
ordinary Podium task. It reads a *turn brief* from `PODIUM_AGENT_TURN`, reports what the agent says
through `podium-runner message`, leaves a transcript and a summary as artifacts, and exits with a
code that says how the turn ended. It knows nothing about Slack, Linear or sessions — the conductor
(`podium-agent`, step 17) is what writes the briefs; this directory is how you drive it yourself.

## Build the image

```sh
make agent-runtime        # podium-agent-runtime:dev and -dev:dev, host arch
make agent-runtime-test   # typecheck + vitest in agent/runtime
```

`podium-agent-runtime` is the **base**, and it is the turn-brief contract and nothing else — no
browser, no database clients, no Python. Podium ships one image beside it, `-dev`, which builds
Podium itself. A workflow that needs any other tools builds its own image `FROM
podium-agent-runtime`; see *The -dev image, and extending the base yourself* below.

The tag is `:dev` and local on purpose. A node runs tasks on its own Docker engine, so an image
built on the same machine is visible to a task without a registry in between. Nothing here pushes to
GHCR; publishing the images is still a TODO in `.goreleaser.yaml`.

## The brief

`examples/agent/brief.sh "instruction"` prints the smallest brief that validates: a `chat` source,
the `podium` profile, the `general` skill, an empty transcript, no repos and no memory.

```sh
examples/agent/brief.sh "hello" | base64 -d | jq .
```

`agent/runtime/src/brief.ts` is the schema and the single source of truth for the shape;
`agent/runtime/testdata/brief.example.json` is a full brief with a transcript, a repo and memory.
The brief is base64(JSON), capped at 256 KiB, and the schema rejects unknown keys — a misspelt field
fails the turn rather than silently changing it.

## Dry run

`PODIUM_AGENT_DRY_RUN=1` skips the model entirely: the runtime validates the brief, clones nothing,
calls nothing, writes both artifacts and emits one `final` message reading `dry run: <instruction>`.
This is the seam every automated test uses.

In a running dev stack:

```sh
./bin/podium run --image podium-agent-runtime:dev \
  --env PODIUM_AGENT_DRY_RUN=1 \
  --env PODIUM_AGENT_TURN=$(examples/agent/brief.sh hello)
# → message (final): dry run: hello
# → finished exit 0

./bin/podium artifacts $(./bin/podium tasks --limit 1 | awk 'NR==2{print $1}')
# turn.json and transcript.jsonl
```

Note there is no `--` and no command: the spec names the image only, so the image's own
`ENTRYPOINT` runs behind `podium-runner` at PID 1.

Straight on Docker, with no Podium around it, is useful for checking the image itself. The runtime
then complains that it cannot reach `/podium/runner` — there is no node to bind-mount it — and still
exits with the right code:

```sh
docker run --rm podium-agent-runtime:dev; echo "exit=$?"
# podium-agent: PODIUM_AGENT_TURN is not set
# exit=2

docker run --rm -e PODIUM_AGENT_DRY_RUN=1 \
  -e PODIUM_AGENT_TURN=$(examples/agent/brief.sh hello) podium-agent-runtime:dev; echo "exit=$?"
# exit=0
```

### Test-only knobs

Two more variables are honoured **only** when `PODIUM_AGENT_DRY_RUN=1`, and ignored otherwise. They
exist for tests, not for operators, and they are not deployment configuration — like everything else
here they go in a task spec's `env:` block:

| Variable | Effect |
|---|---|
| `PODIUM_AGENT_DRY_RUN_SLEEP_MS=N` | sleep N ms before emitting the final, so a turn can be overlapped or interrupted |
| `PODIUM_AGENT_DRY_RUN_EXIT=N` | exit with code N after emitting the final, to exercise a failure relay |

```sh
# Cancel a turn mid-flight: the runtime catches SIGTERM, says it was cancelled, and exits 0.
TASK=$(./bin/podium run --detach --image podium-agent-runtime:dev \
  --env PODIUM_AGENT_DRY_RUN=1 --env PODIUM_AGENT_DRY_RUN_SLEEP_MS=30000 \
  --env PODIUM_AGENT_TURN=$(examples/agent/brief.sh "take your time"))
./bin/podium task cancel $TASK --reason "by hand"
./bin/podium task get $TASK --json | jq '.status, .exitCode'   # "TASK_STATUS_CANCELLED", 0
```

## For real

The model credential reaches the container as a Podium secret with `target: env`, and that is the
only path — the runtime reads no file, no mount and nothing in the brief. Which one it is follows
the brief's `profile.agent`: `podium.agent.anthropic_api_key` for `claude`, and
`podium.agent.xai_api_key` for `grok`.

```sh
printf %s "$ANTHROPIC_API_KEY" | ./bin/podium secret set podium.agent.anthropic_api_key

./bin/podium run --image podium-agent-runtime:dev \
  --secret podium.agent.anthropic_api_key:env:ANTHROPIC_API_KEY \
  --env PODIUM_AGENT_TURN=$(examples/agent/brief.sh "Reply with the single word pong")
```

Add `--secret podium.agent.github_token:env:GITHUB_TOKEN` when the brief has `repos`, and
`--secret podium.agent.memory_api_key:env:PODIUM_MEMORY_API_KEY` when it has `memory`. A brief that
names a memory env var which is not set is refused (exit 2): the conductor promised it.

For a Grok turn — a brief whose `profile.agent` is `grok` and which carries a `provider` block,
as `testdata/brief.example.json` does — swap the credential for the one that backend spends:

```sh
printf %s "$XAI_API_KEY" | ./bin/podium secret set podium.agent.xai_api_key

./bin/podium run --image podium-agent-runtime:dev \
  --secret podium.agent.xai_api_key:env:XAI_API_KEY \
  --env PODIUM_AGENT_TURN=$(examples/agent/brief.sh "Reply with the single word pong")
```

`brief.sh` writes a `claude` brief, so that command needs a hand-edited brief to be a real Grok
turn. The runtime reads `provider.base_url` and `provider.api_key_env` out of the brief and sets
`ANTHROPIC_BASE_URL` and `ANTHROPIC_AUTH_TOKEN` for the SDK from them; `ANTHROPIC_API_KEY` is
removed from the SDK's environment so a stale one cannot shadow the token.

Nothing is set in the image for either of these — `docker inspect` shows no `CLAUDE_*` or
`ANTHROPIC_*` variable, and there is no `--dangerously-skip-permissions` anywhere. The only
permission decision is `permissionMode` in `agent/runtime/src/main.ts`.

### The manual smoke test, and why it is not automated

Once per meaningful change to `main.ts`, run the real thing:

```sh
./bin/podium run --image podium-agent-runtime:dev \
  --secret podium.agent.anthropic_api_key:env:ANTHROPIC_API_KEY \
  --env PODIUM_AGENT_TURN=$(examples/agent/brief.sh "Reply with the single word pong")
```

Expect a `final` message reading `pong`, exit 0, and a non-zero `total_cost_usd` in `turn.json`.
This is not in the test suite because it spends money, needs a credential CI does not have, and its
output is not deterministic.

## What comes out

| | |
|---|---|
| `message{type: progress}` | each assistant text block that precedes further tool use, coalesced to at most one per 5 s |
| `message{type: final}` | the turn's answer, always exactly once, even on failure; split into several messages above 32 KiB, with `attachments` on the last |
| `message{type: accounting}` | the same document as `turn.json`, emitted after the final. It is for the reader, never for a human; the conductor reads `num_turns` and `total_cost_usd` from it and posts nothing |
| `.podium/artifacts/transcript.jsonl` | one JSON line per SDK message |
| `.podium/artifacts/turn.json` | `{session_id, turn_id, sdk_session_id, num_turns, total_cost_usd, exit_code, started_at, finished_at}` |

Both files sit in the node's auto-collection directory, so they are collected without any
`podium-runner artifact add` call. Anything else the agent leaves there is an artifact too, and
becomes an attachment on the final message when the agent names it by file name.

Exit codes, and never any others:

| | |
|---|---|
| `0` | the turn finished — including a turn cancelled by SIGTERM |
| `2` | the brief was invalid |
| `3` | the skill's `max_turns` was reached |
| `4` | an SDK or API error, a missing key, a failed clone, a network failure |

## The -dev image, and extending the base yourself

`podium-agent-runtime-dev:dev` is the one image Podium ships beside the base, and it exists to
build Podium itself: Go, the Docker **client** and golangci-lint, for the `podium` skill. It
carries no daemon — the skill sets `docker: true` and the conductor attaches one as a sidecar,
which is what lets a turn run `make test-integration` against a daemon that dies with the task.

It is also the **worked example** of everything below. Podium ships no image for somebody else's
workflow — every workflow differs — so `agent/runtime/Dockerfile.dev` is what a real one looks
like: pinned versions, a smoke test that fails when the image drifts, and a deliberate list of
what it does *not* carry. Read it, then build your own.

An agent image is not just a bag of tools: it has to implement the turn-brief protocol above —
read `PODIUM_AGENT_TURN`, drive one SDK turn, talk to `podium-runner`, write the two artifacts,
exit 0/2/3/4. That is roughly a thousand lines of TypeScript in `agent/runtime/src`, so the sane
route is not to reimplement it:

```dockerfile
FROM podium-agent-runtime:dev        # or a pinned ghcr.io/... tag

USER root
RUN set -eux; \
    apt-get update; \
    apt-get install -y --no-install-recommends postgresql-client; \
    rm -rf /var/lib/apt/lists/*; \
    psql --version
USER agent
```

Three things to know:

- The base is **Debian 12 bookworm, glibc, Node 22**. Use `apt-get` and bookworm package sources,
  and glibc wheels or binaries — not musl ones.
- **End with `USER agent`** (uid 1000). The runner socket and the secrets tmpfs are set up for
  that uid; a container that stays root does not get them the same way. The base image renames
  the stock `node` user rather than adding a second name for uid 1000.
- Do not override `ENTRYPOINT`. It is `node /opt/podium-agent/dist/main.js`, and the Podium spec
  for an agent task names the image only.

**Watch what `apt-get` drags in.** Debian's `python3-matplotlib` on bookworm hard-depends on
`gcc-12`, `g++-12`, `libboost1.74-dev` and `libopenblas-dev` — **1.05 GB of C++ toolchain** in a
runtime image, for a chart. The alternative is pip's own manylinux wheels at exact versions, and
Debian 12's interpreter is marked `EXTERNALLY-MANAGED` (PEP 668) and ships no pip, so that means
`pip3 install --break-system-packages`, pip removed again in the same layer, and knowing that
nothing else in your image installs a Python package. `apt-cache depends --recurse` before you
commit to a package, and check the image size after.

Then point a skill at it:

```yaml
# skills/dba.yaml
image: registry.example.com/agent-warehouse:2026-09-05
```

A skill's `image:` is **any reference the node's own Docker engine can resolve**. A tag you built
locally works only on the machine that built it, so a fleet needs a registry every node can pull
from. Podium has **no registry authentication**: a private registry that requires a login is not
supported today, and a node either pulls anonymously or already has the image.
