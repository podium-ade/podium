# The conductor (`podium-agent`)

`podium-agent` is a second long-lived process beside `podium-server`. It holds the bot's identity
in Slack, turns a message into **one Podium task running the agent runtime image**, relays what
the agent says back into the thread while the task runs, and records the turn.

It is an ordinary API client of the control plane. It never opens the server's database, never
sees the master key, and never touches Docker.

## What it is not

- **Not a resident model session.** Every inbound message becomes one task that reads its
  context, works, reports and exits. Nothing about the model is long-lived. Identity, personality
  and (from step 19) memory are; the model context is not.
- **Not interactive.** A turn ends with an answer, a pull request, or a question for the human.
  There is no stdin, exec, attach or port-forward into a running task — see
  [`concepts.md#what-a-task-is-not`](concepts.md).
- **Not the owner of the conversation.** A Slack thread *is* the conversation. The conductor
  fetches it and hands it to the turn; it stores turn records, not transcripts.
- **Not a log consumer.** It reads a task's `message` events and nothing else. If you want the
  agent's raw output, every failure message carries the task id: `podium logs TASK_ID`.

---

## How a turn works

```
somebody says something
  ↓  source (Slack)                          normalises it into an InboundEvent
  ↓  Select                                  which skill? /skill, then the channel, then the default
  ↓  UpsertSession                            by source key — one thread, one session, one skill
  ↓  React 👀  +  post "👀 working…"          before any work starts
  ↓  FetchTranscript                          the thread so far
  ↓  brief                                    base64 JSON on PODIUM_AGENT_TURN, capped at 256 KiB
  ↓  CreateTask                               image + brief + the skill's secrets + the Anthropic key
  ↓  StreamTaskEvents                         relay every `message` event, exactly once
  ↓  GetTask                                  the terminal status decides what is said last
  ↓  FinishTurn  +  React ✅ or ❌
```

**One turn per session at a time.** A message that arrives while a turn is running is not lost
and does not start a second task: it is in the thread, so it is in the next turn's transcript,
and the next turn starts from it as soon as the running one ends.

**Progress.** The placeholder is exactly `👀 working…`. Each `progress` message the runtime emits
replaces it by an edit, prefixed `⏳ `, at most one edit every two seconds — the newest text wins,
and a held edit is flushed when the answer arrives. After a restart the placeholder's message id
is gone, so a resumed turn posts progress as new messages instead of editing.

**The answer.** A `final` message is posted as a new message, verbatim. The runtime splits an
answer over 32 KiB into several consecutive `final` messages; they are posted in order. Text over
Slack's own 4000-character limit is split again, at the last newline that fits.

**Attachments.** A `final`'s `attachments` are artifact *names*. They are resolved once the task
is terminal, because an artifact named in a message may still have been uploading when the
message arrived. A name that matches nothing gets one line in the thread; anything over
**25 MB** is not relayed and the thread says where to find it instead.

### What is said when a turn does not succeed

The raw `failure_reason` is **never** posted. It goes to the conductor's log at `Warn` with the
task id.

| task status | `turns.status` | posted |
|---|---|---|
| `succeeded` | `succeeded` | nothing beyond the answer |
| `failed`, exit 3 (the runtime ran out of turns) | `failed` | "I ran out of turns before finishing. Task `task_…`." |
| `failed`, `failure_reason: timeout` | `timeout` | "I hit the 15m limit for this skill. Task `task_…`." |
| `failed`, a missing secret | `failed` | "This bot is missing a credential (`NAME`). An operator needs to set it. Task `task_…`." |
| `failed`, anything else | `failed` | "Something went wrong on my side. Task `task_…`." |
| `lost` | `lost` | "The machine running this went away. Task `task_…`. I did not retry." |
| `cancelled` | `cancelled` | "This was cancelled. Task `task_…`." |

A turn that already posted an answer and then fails — an attachment that would not upload, say —
posts the failure line as well. The human should know something was cut short.

`retry_on_node_loss` is deliberately **false** for every turn: a turn may already have posted an
answer, and running it again would say it twice. A lost node is surfaced to the human instead.

### Cancelling a turn

From the CLI: `podium task cancel TASK_ID`. There is no reaction-to-cancel. The conductor sees
the task go `cancelled` and says so in the thread.

---

## Configuration

Environment only. Every variable is in [`../deploy/.env.example`](../deploy/.env.example), and a
test fails if one is read by the code and missing from that file.

| env | required | default | meaning |
|---|---|---|---|
| `PODIUM_AGENT_SERVER` | yes | — | the Podium API base URL |
| `PODIUM_AGENT_API_TOKEN` | with `http://` | — | the server's `PODIUM_DEV_TOKEN`; empty on a tailnet, where WhoIs names the caller |
| `PODIUM_AGENT_DATABASE_URL` | yes | — | the conductor's **own** database, `podium_agent` |
| `PODIUM_AGENT_LISTEN` | no | `127.0.0.1:8090` | its Connect API, health and metrics |
| `PODIUM_AGENT_TOKEN` | yes | — | the bearer `podium-server` presents on proxied `AgentService` calls |
| `PODIUM_AGENT_PROFILE_DIR` | no | `/etc/podium/agent` | `profile.yaml`, `skills/`, `prompts/` |
| `PODIUM_AGENT_SLACK_APP_TOKEN` | for Slack | — | `xapp-…`, Socket Mode |
| `PODIUM_AGENT_SLACK_BOT_TOKEN` | for Slack | — | `xoxb-…` |
| `PODIUM_AGENT_DEV_SOURCE` | no | false | **TEST ONLY**, see below |

Both Slack tokens or neither: one alone is a startup error naming the other. With neither, no
source is started and the conductor listens to nothing — it says so at startup.

### Its own database

`podium_agent`, on the same Postgres as the control plane's, and a different database — not a
schema in the same one. The conductor migrates it on start; two instances starting at once apply
the migration exactly once (an advisory lock).

`deploy/postgres/init.sql` creates it, but Postgres runs init scripts **only on an empty data
directory**. On an existing install:

```sh
docker compose exec -T postgres createdb -U podium podium_agent
# or, for the dev compose:
docker exec podium-dev-postgres createdb -U podium podium_agent
```

Losing this database costs turn records, not conversations: the conversations are in Slack.

### Health

`/healthz` (the process is up), `/readyz` (its database **and** the Podium API's `WhoAmI`) and
`/metrics` on `PODIUM_AGENT_LISTEN`, unauthenticated like the server's. Everything else on that
listener is behind `Authorization: Bearer $PODIUM_AGENT_TOKEN`.

Metrics: `podium_agent_turns_total{source,skill,status}`,
`podium_agent_turn_duration_seconds{skill}`, `podium_agent_relayed_messages_total{type}`,
`podium_agent_source_events_total{source}`, `podium_agent_follow_reconnects_total`.

---

## The profile directory

One profile per conductor. [`../examples/agent`](../examples/agent) is a working one.

```
profile.yaml
prompts/profile.md
skills/general.yaml
prompts/general.md
```

Every file is decoded with unknown keys **rejected**, the same rule `pkg/spec` follows for a task
spec: a misspelt key is a startup error naming the file, not a field that silently does nothing.

### `profile.yaml`

```yaml
name: podium                 # required; ^[a-z][a-z0-9-]{0,31}$
display_name: Podium         # required
system_prompt: file:./prompts/profile.md   # required; inline, or file: relative to THIS file
model: claude-opus-5         # required
default_skill: general       # required; must name a loaded skill
```

### `skills/<name>.yaml`

The file name is the skill name and must match `^[a-z][a-z0-9-]{0,31}$`.

```yaml
image: ghcr.io/alvaroibarguen/podium-agent-runtime:latest   # required
system_prompt: file:../prompts/general.md                    # required
allowed_tools: [Read, Grep, Glob, WebFetch, Bash]            # required, non-empty
max_turns: 50                                                # default 50
timeout: 30m                                                 # default 30m
model: ""                                                    # default: the profile's
labels: []                                                   # node labels, verbatim into the spec
resources: {cpu: 2, memory_mb: 4096}                         # verbatim into the spec
secrets:                                                     # verbatim into the spec
  - {name: podium.agent.github_token, target: env, key: GITHUB_TOKEN}
repos: []                                                    # [{name, url, default_branch}] → brief.repos
slack_channels: []                                           # channel IDs this skill is the default for
env: {}                                                      # plain env, verbatim into the spec
```

`secrets`, `resources`, `env` and `labels` are validated by exactly the code that validates a
task spec, because that is where they end up. Two rules of the conductor's own:

- **`podium.agent.anthropic_api_key` may not appear in `secrets:`.** The conductor attaches it to
  every turn itself. A skill listing it is an error.
- **`env:` may not set `PODIUM_AGENT_TURN` or `ANTHROPIC_API_KEY`.** The first is the brief; the
  second comes from the secret.

### Which skill runs

In order:

1. The message starts with `/<skill>` followed by whitespace or the end — that skill, prefix
   stripped. An **unknown** `/name` is not an error: it is left in the text and falls through, so
   somebody typing `/shrug` does not break the bot. `/etc/hosts` is not a skill selector either.
2. The channel is in a skill's `slack_channels`. Two skills claiming one channel is a startup
   error.
3. `profile.default_skill`.

**One session, one skill**, fixed when the thread's session is created. A later `/other` in the
same thread is refused politely: start a new thread.

Changing a skill file needs a restart. There is no SIGHUP reload.

### Reserved secret names

Set with `podium secret set`, except the first, which the web UI will set (step 18).

| name | lands as | who needs it |
|---|---|---|
| `podium.agent.anthropic_api_key` | `ANTHROPIC_API_KEY` | every turn; the conductor attaches it |
| `podium.agent.github_token` | `GITHUB_TOKEN` | a skill with `repos:` |
| `podium.agent.memory_api_key` | (step 19) | a brief with `memory` |
| `podium.agent.warehouse_url` | (step 21) | the analyst skill |
| `podium.agent.warehouse_credentials` | (step 21) | the analyst skill |

The Anthropic key must **exist** before any turn can run, even a dry run: the task spec names it
and the control plane refuses a task that names a secret it does not have. That failure reaches
the thread as "This bot is missing a credential".

---

## Setting up the Slack app

The app is checked in as [`../deploy/slack-app-manifest.yaml`](../deploy/slack-app-manifest.yaml).

1. Go to <https://api.slack.com/apps> → **Create New App** → **From an app manifest**. Pick the
   workspace, paste the manifest in, create the app.
2. **Basic Information → App-Level Tokens → Generate Token and Scopes.** Name it anything, add
   the **`connections:write`** scope, generate. That is the `xapp-…` token →
   `PODIUM_AGENT_SLACK_APP_TOKEN`.
3. **Install App → Install to Workspace**, and approve the scopes. The **Bot User OAuth Token**
   on that page is the `xoxb-…` token → `PODIUM_AGENT_SLACK_BOT_TOKEN`.
4. **Socket Mode** is already on (the manifest sets it) and there is no Request URL to configure:
   the conductor dials out over a WebSocket, so nothing about Podium has to be reachable from the
   internet.
5. **Invite the bot to every channel you want it in**: `/invite @Podium`. It cannot see a channel
   it is not in, whatever its scopes say.
6. Put both tokens in `.env` and start the conductor. The startup log names the sources it
   enabled and the skills it loaded.

Then, in a channel the bot is in:

```
@Podium what does this repo do?
```

👀 appears on your message, a `👀 working…` reply appears in a thread, it turns into `⏳ …` as the
agent works, the answer replaces it, and 👀 becomes ✅.

### What the bot listens to

- **`app_mention`** in a channel: a mention starts a thread at its own message, and the answer
  goes into that thread.
- **A reply in a thread the bot is already in** continues the conversation with **no mention
  needed**.
- **A DM** is a conversation of its own, keyed the same way.
- Everything else is ignored: anything from a bot (this one included), anything with a subtype
  (`message_changed`, `message_deleted`, `channel_join`, …), and any channel message that is not
  a reply in a thread the bot knows.

Slack delivers a channel mention twice — once as `app_mention`, once as `message` — so
`(channel, ts)` is deduplicated for a few minutes and one message starts one turn.

Text is posted as **plain text**. `mrkdwn` conversion and Block Kit are out of scope, so the
model's Markdown arrives as the model wrote it.

Web API calls go through one limiter at 1/s per conductor, and a 429's `Retry-After` is honoured
once before the call fails.

---

## The limits that bite

- **The brief is capped at 256 KiB encoded.** The conductor drops the oldest transcript entries
  until it fits and sets `transcript_truncated: true`, which the runtime tells the model about. A
  brief that does not fit even with an empty transcript fails the turn with "this conversation is
  too large" — start a new thread with just the question.
- **One turn per session at a time.** A busy thread queues rather than parallelises.
- **4000 characters per Slack message.** Longer answers arrive as several messages.
- **25 MB per attachment.**
- **No RBAC, anywhere.** See below.

---

## No RBAC

**Whoever can tag the bot can run code on a worker with that skill's credentials.** There is no
allowlist of users, no roles, and no read-only mode. The skill file is the only boundary: keep
`secrets:` minimal per skill, and do not put a credential in a skill that anybody in a public
channel can reach.

The two Slack tokens are as sensitive as `PODIUM_DEV_TOKEN`. So is `PODIUM_AGENT_TOKEN`, which is
the only thing guarding every session the bot has had.

Text relayed out of a task is **untrusted content**. The conductor posts it verbatim and acts on
none of it: it never parses an answer for a command, a channel name or a user ID. See
[`security.md`](security.md#5-the-conductor-and-the-bot).

The agent runtime runs the SDK with `bypassPermissions` and `settingSources: []`. That means no
tool call inside a turn asks anybody anything, and nothing on the worker's disk — no `~/.claude`,
no `CLAUDE.md` out of a cloned repository — changes what the agent does. The container is the
sandbox; the permission prompt is not. `docs/security.md#3-a-task-container--untrusted` is the
boundary being relied on.

---

## The conductor's API

One Connect service, `podium.agent.v1.AgentService`, served on `PODIUM_AGENT_LISTEN` under
`/podium.agent.v1.AgentService/` behind the bearer. This step has three read RPCs —
`ListSessions`, `GetSession`, `ListTurns` — and later steps add settings (18), memory (19) and
chat (21).

Nothing reaches it from a browser yet. When it does, it will be through `podium-server`, which
will proxy that prefix behind its own identity middleware and add the bearer plus an
`X-Podium-Login` header naming the operator. The conductor already reads that header for audit
logging and already rejects a request without the bearer. Keep `PODIUM_AGENT_LISTEN` on loopback.

---

## The dev source — TEST ONLY

`PODIUM_AGENT_DEV_SOURCE=true` mounts an in-process source and two routes on the same listener,
behind the same bearer:

| | |
|---|---|
| `POST /dev/inbound` | `{"channel":"C1","thread":"1.1","author":"alice","text":"…"}` → an inbound event, as if a human had sent it. Returns the session's source key. |
| `GET /dev/outbound?since=N` | every `Post`, `Edit`, `Attach` and `React` the conductor has made, in order, as JSON. |

`POST /dev/inbound` also accepts `"dry_run": true`, `"dry_run_sleep_ms"` and `"dry_run_exit"`,
which become `PODIUM_AGENT_DRY_RUN`, `PODIUM_AGENT_DRY_RUN_SLEEP_MS` and
`PODIUM_AGENT_DRY_RUN_EXIT` on the turn's task spec. Those three knobs make the runtime skip the
model entirely and emit a canned answer — they are the seam the whole test suite runs on, and the
conductor honours them for the dev source and no other.

**With the env unset the routes do not exist and the source is not started.** When it is set, the
startup log says `DEV SOURCE ENABLED — TEST ONLY` in capitals. Never set it outside a test:
anything that can present the bearer can then make the bot say and do things as if a human had
asked.

---

## Running it by hand

```sh
docker compose -f deploy/docker-compose.dev.yml up -d --wait postgres
docker exec podium-dev-postgres createdb -U podium podium_agent      # once

make build agent-runtime
# ... start podium-server and podium-node as in docs/quickstart.md, then:
podium secret set podium.agent.anthropic_api_key            # value on stdin

PODIUM_AGENT_SERVER=http://127.0.0.1:8080 \
PODIUM_AGENT_API_TOKEN=devtoken \
PODIUM_AGENT_DATABASE_URL=postgres://podium:podium@127.0.0.1:5432/podium_agent \
PODIUM_AGENT_TOKEN=agenttoken \
PODIUM_AGENT_PROFILE_DIR=examples/agent \
PODIUM_AGENT_SLACK_APP_TOKEN=xapp-... \
PODIUM_AGENT_SLACK_BOT_TOKEN=xoxb-... \
./bin/podium-agent
```

Without the Slack tokens it starts and listens to nothing, which is a fine way to check the
profile loads and the database migrates.

To drive one turn with no Slack and no model at all, see
[`../examples/agent/README.md`](../examples/agent/README.md), which runs the runtime image
directly, or add `PODIUM_AGENT_DEV_SOURCE=true` and post to `/dev/inbound`.
