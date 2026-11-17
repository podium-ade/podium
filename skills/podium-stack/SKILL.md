---
name: podium-stack
description: "Run the real Podium you just changed — podium-server, the conductor and a node built from this branch, with Postgres, the object store and every task in this turn's own dind sidecar — seeded with a fictional business (Fernhill Cycle Co.: secrets, playbooks, routines, real model conversations including one with image attachments, tasks in every state with artifacts), and the branch's UI at http://task:5173 for the browser. Use before validate-pr, or whenever a change has to be seen working in Podium itself rather than in a test or a mock page."
---

# Run the real Podium, from this branch

You are inside a `podium-dev` turn: a container with the repository at `/workspace/podium`, a
Docker daemon in the `dind` sidecar (`DOCKER_HOST` is set), a browser in the `chrome` sidecar,
and this turn's own `ANTHROPIC_API_KEY`. That is everything a Podium deployment needs. This
skill turns it into one.

```sh
# The skill's files are mounted read-only; work from a copy so you can extend the seed.
cp -r <this skill's directory> /workspace/podium-stack && S=/workspace/podium-stack
cd /workspace/podium
bash $S/stack.sh up              # build the branch, start everything — 5–10 min the first time
bash $S/stack.sh seed            # Fernhill Cycle Co.
eval "$(bash $S/stack.sh env)"   # PODIUM_SERVER, PODIUM_TOKEN, PODIUM_CLI, PODIUM_UI_URL
bash $S/stack.sh status
```

Then the browser opens **`http://task:5173`** — the branch's `web/` under Vite, its proxy
carrying the token, so no token prompt. Never `localhost`: the browser is another container.

## Validation means this stack — not a mock

A throwaway page that renders the component you changed, a fake endpoint, a fixture HTML file:
**none of that is validation.** It proves the component renders the props you chose, in a page
you wrote, with data you invented — exactly the things a real deployment differs from. The
screen you changed has to be reached the way a person reaches it: in this stack, through its
navigation, showing data the product itself produced.

If the data the change needs is not in the fixture, **add it to `$S/seed.sh`** through the API, the
way the rest is written, and re-run `bash $S/stack.sh seed`. If the stack will not come up, fix that or
say plainly that the change was **not validated** and why. Never substitute a harness and report
it as a pass.

## What is where

| | Where | Notes |
|---|---|---|
| podium-server | this container, `127.0.0.1:8080` | `log/server.log` |
| conductor | this container, `0.0.0.0:8090` | conversations run here on the branch's `agent/runtime/dist`; `log/agent.log` |
| node | this container, engine = `dind` | labels `nested`; `log/node.log` |
| Postgres | container `podium-pg` in dind, `dind:5432` | `podium` / `podium` |
| object store | container `podium-s3` in dind, `dind:9000` | artifacts and log roll-up on |
| runtime image | `podium-agent-runtime:dev` in dind | built from this branch; rebuilt when HEAD moves |
| UI | Vite, `http://task:5173` | the branch's `web/`, hot-reloads your edits |

State, logs and the `.env` are in `/workspace/.stack/`. Everything dies with the turn.

## The fixture: Fernhill Cycle Co.

| Kind | What |
|---|---|
| Model | this turn's Anthropic key; seeded turns use `claude-haiku-5-5` (`STACK_MODEL` to change) |
| Secrets | `fernhill.shop_api_token`, `.supplier_api_key`, `.warehouse_db_password`, `.slack_webhook` — placeholders |
| Personalities | `fern` (support), `ledger` (finance) |
| Playbooks | `order-triage`, `inventory-reorder`, `refund-review` (a long prompt), `sales-charts` (3 PNGs + a CSV), `quarterly-charts` (8 PNGs, a non-image `.png`, a CSV) |
| Routines | *Morning order triage* (enabled), *Weekly inventory reorder* (paused, with a note) |
| Conversations | *Black Friday email*, *Late order from Kelowna*, and two that delegate, so their replies carry real attachments a few minutes after the seed: *Weekly sales charts* (**three images of different shapes and a CSV**) and *Quarterly chart pack* (**eight images** from a 48px icon to 2400×1600 and a 300×1200 tall one, a **120-character file name**, `forecast-q4.png` that is **not an image**, and a CSV) |
| Tasks | succeeds with two artifacts, fails (exit 3), env vars, a 400-line log, running for 6 h, cancelled |

Conversations are answered asynchronously. Wait for them (`$PODIUM_CLI tasks`, or the chat in
the UI) before you judge a screen that shows them.

## Rebuilding after you change code

- `web/` — nothing; Vite hot-reloads.
- Go — `bash $S/stack.sh down && bash $S/stack.sh up`; data in Postgres is kept, the build is incremental.
- `agent/runtime/` — the same; `up` rebuilds the runtime image when HEAD moved, so commit first.

## Known limits

- **Sidecars and previews of tasks the nested node runs fail readiness.** The node dials a
  sidecar's address on dind's inner bridge, which this container cannot route to. Plain tasks
  are fine. Say so instead of reporting it as a product bug.
- No Slack, GitHub or Linear connections, MCP servers or memory: they need real accounts.
