# Running the agent runtime by hand

`agent/runtime/` builds a container image that runs **one turn** of the Claude Agent SDK inside an
ordinary Podium task. It reads a *turn brief* from `PODIUM_AGENT_TURN`, reports what the agent says
through `podium-runner message`, leaves a transcript and a summary as artifacts, and exits with a
code that says how the turn ended. It knows nothing about Slack, Linear or sessions — the conductor
(`podium-agent`, step 17) is what writes the briefs; this directory is how you drive it yourself.

## Build the images

```sh
make agent-runtime        # podium-agent-runtime:dev, -browser:dev, -data:dev, host arch
make agent-runtime-test   # typecheck + vitest in agent/runtime
```

The tag is `:dev` and local on purpose. A node runs tasks on its own Docker engine, so an image
built on the same machine is visible to a task without a registry in between. Nothing here pushes to
GHCR; publishing the three images is still a TODO in `.goreleaser.yaml`.

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

The Anthropic key reaches the container as a Podium secret with `target: env`, and that is the only
path — the runtime reads no file, no mount and nothing in the brief. Its reserved name is
`podium.agent.anthropic_api_key`.

```sh
printf %s "$ANTHROPIC_API_KEY" | ./bin/podium secret set podium.agent.anthropic_api_key

./bin/podium run --image podium-agent-runtime:dev \
  --secret podium.agent.anthropic_api_key:env:ANTHROPIC_API_KEY \
  --env PODIUM_AGENT_TURN=$(examples/agent/brief.sh "Reply with the single word pong")
```

Add `--secret podium.agent.github_token:env:GITHUB_TOKEN` when the brief has `repos`, and
`--secret podium.agent.memory_api_key:env:PODIUM_MEMORY_API_KEY` when it has `memory`. A brief that
names a memory env var which is not set is refused (exit 2): the conductor promised it.

Nothing is set in the image for either of these — `docker inspect` shows no `CLAUDE_*` or
`ANTHROPIC_*` variable in any of the three images, and there is no
`--dangerously-skip-permissions` anywhere. The only permission decision is `permissionMode` in
`agent/runtime/src/main.ts`.

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

## The other two images

`podium-agent-runtime-browser:dev` adds Playwright and Chromium (the `coder` skill).
`podium-agent-runtime-data:dev` adds `psql`, `bq`, `duckdb`, and `python3` with `matplotlib` and
`pandas` (the `analyst` skill — see [`docs/agent.md`](../../docs/agent.md#the-analyst-skill)). It
does **not** inherit the browser image: a warehouse query has no business carrying Chromium. All
three run the same `dist/` and the same `node_modules` as the base image — the layer is copied out of it
rather than rebuilt — so they cannot drift, and both take exactly the same brief.

Check the data image the way its acceptance item does — note the `--entrypoint`, without which
`sh -c …` is passed to the agent runtime as arguments and the probe silently runs the agent:

```sh
docker run --rm --user agent --entrypoint sh podium-agent-runtime-data:dev \
  -c 'psql --version && bq version && duckdb --version && python3 -c "import matplotlib, pandas"'
```

### The screenshot helper

The browser image carries one extra thing an agent calls directly:

```
/opt/podium-agent/bin/screenshot URL OUT.png [--width N] [--height N] [--full-page]
```

Chromium headless, one navigation with a 15-second `networkidle` timeout, one PNG, exit 0 and the
absolute path on stdout. Anything Playwright complains about goes to stderr and exits 1. It exists
so a turn can verify a UI change without writing Playwright code every time.

```sh
./bin/podium run --image podium-agent-runtime-browser:dev --label browser -- \
  sh -c 'python3 -m http.server 8000 >/dev/null 2>&1 & sleep 1;
         /opt/podium-agent/bin/screenshot http://127.0.0.1:8000/ /workspace/.podium/artifacts/shot.png'
./bin/podium artifacts <task_id>          # shot.png
```

The driver is `playwright-core`, pinned to the same version as the base image's browsers and
installed in `/opt/podium-agent/browser/node_modules` — not in the runtime's, so the other two
images do not carry it. `agent/runtime/src/screenshot.ts` is the argument parsing (typechecked and
unit-tested); `agent/runtime/browser/screenshot.mjs` is the part that drives the browser.

`pnpm test:images` in `agent/runtime` drives the helper inside a real container and skips with a
message when Docker or the image is missing.
