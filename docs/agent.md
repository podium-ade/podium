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
  ↓  Select                                  which skill? the chip, /skill, the channel, the defaults
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

**Accounting.** `turns.num_turns` and `turns.cost_usd` come from the runtime's own summary, which
leaves the container by **two** routes carrying the same document: an `accounting` message, emitted
after the final, and the `turn.json` artifact. The message is what the conductor reads; the
artifact is the fallback, and only fetched when no message arrived. Two routes because the object
store is optional (`PODIUM_S3_*` unset disables artifacts) while the accounting is not — with only
the artifact, a host with no store recorded a successful turn with both columns null and said
nothing about it. The message is emitted *after* the final so that a conductor resuming a turn
across a restart, which follows from the last seq it relayed, is sent it again; its seq is
deliberately not claimed in the `relayed` ledger, because accounting is never said out loud. A turn
that succeeds having reported neither is logged at `Warn` with the task id and counted in
`podium_agent_turns_without_accounting_total`.

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
| `PODIUM_AGENT_LINEAR_API_KEY` | for Linear | — | the bot user's **personal** API key. Empty means no Linear source; set and broken means the process exits at boot |
| `PODIUM_AGENT_LINEAR_POLL_INTERVAL` | no | `30s` | how often assigned issues are asked for. Floor **10s** |
| `PODIUM_AGENT_LINEAR_URL` | no | `https://api.linear.app/graphql` | the GraphQL endpoint; a test seam and an egress hook, **not** a "which Linear" knob |
| `PODIUM_AGENT_UI_URL` | no | `PODIUM_AGENT_SERVER` | the Podium web UI as a **human** reaches it. Used only for the link a Linear comment falls back to when an attachment cannot be uploaded |
| `PODIUM_AGENT_ANTHROPIC_BASE_URL` | no | `https://api.anthropic.com` | where a pasted provider key is validated; a test seam and an egress hook, **not** a BYOK knob |
| `PODIUM_AGENT_MEMORY_URL` | no | — | the shared memory as **this process** reaches it. Empty turns memory off entirely |
| `PODIUM_AGENT_MEMORY_TASK_URL` | no | `http://host.docker.internal:8888` | the same service as a **task container** reaches it |
| `PODIUM_AGENT_MEMORY_BANK` | no | `podium` | the one bank every turn shares |
| `PODIUM_AGENT_MEMORY_API_KEY` | with the URL | — | the bearer the memory service requires. It has **no authentication** without one |
| `PODIUM_AGENT_DEV_SOURCE` | no | false | **TEST ONLY**, see below |

Two more live on **`podium-server`**, not here, and they are what lets a browser reach the
conductor at all:

| env (on `podium-server`) | required | meaning |
|---|---|---|
| `PODIUM_AGENT_URL` | for the web UI | where the conductor listens, `scheme://host:port` with no path. Unset means no conductor: the prefix is not mounted, `WhoAmI.agent_enabled` is false and the UI hides its Agent screen |
| `PODIUM_AGENT_TOKEN` | with `PODIUM_AGENT_URL` | the **same value** as above. The server refuses to start with a URL and no token |

Both Slack tokens or neither: one alone is a startup error naming the other. With no Slack tokens
and no Linear key, no source is started and the conductor listens to nothing — it says so at
startup, which is a fine shape to run it in while you set the provider key in the UI.

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

Losing this database costs turn records, not conversations: the conversations are in Slack — and
now also every skill made in the web UI, which lives in its `skills` table. Skills that came out
of the profile directory are unaffected. Back it up if the UI is where your skills are defined.

The shared memory has a third database, `podium_memory`, on the same Postgres — see *Memory*
below. Losing **that** one does lose something: it is the only copy.

### Health

`/healthz` (the process is up), `/readyz` (its database, the Podium API's `WhoAmI`, and the
shared memory's `/health` when one is configured) and `/metrics` on `PODIUM_AGENT_LISTEN`,
unauthenticated like the server's. Everything else on that listener is behind
`Authorization: Bearer $PODIUM_AGENT_TOKEN`.

A memory service that is down makes `/readyz` 503 — and turns still run and still answer
through it. That is deliberate: a bot with no memory is a worse bot, not a broken one.

Metrics: `podium_agent_turns_total{source,skill,status}`,
`podium_agent_turn_duration_seconds{skill}`, `podium_agent_relayed_messages_total{type}`,
`podium_agent_source_events_total{source}`, `podium_agent_follow_reconnects_total`,
`podium_agent_turns_without_accounting_total`,
`podium_agent_memory_retain_total{result}` (`ok`, `error`, `redacted`).

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
chat_default_skill: analyst  # optional; the skill /agent/chat starts with.
                             # Must name a loaded skill; unset means default_skill.
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
linear: false                                                # this is the skill Linear tickets run
env: {}                                                      # plain env, verbatim into the spec
```

`secrets`, `resources`, `env` and `labels` are validated by exactly the code that validates a
task spec, because that is where they end up. Two rules of the conductor's own:

- **`podium.agent.anthropic_api_key` may not appear in `secrets:`.** The conductor attaches it to
  every turn itself. A skill listing it is an error.
- **`env:` may not set `PODIUM_AGENT_TURN` or `ANTHROPIC_API_KEY`.** The first is the brief; the
  second comes from the secret.
- **At most one skill may set `linear: true`.** Two is a start-up error: a ticket has no channel
  and no `/skill` prefix, so there would be nothing to choose between them with. Zero is fine —
  most bots take no tickets — until a Linear key is set, and then the conductor refuses to start.

### Which skill runs

In order:

1. **A skill the source knows** is right, which no rule below may second-guess: the web chat's
   skill chip (`SendChatMessage.skill`) and the `linear: true` skill a ticket runs. A ticket's
   text is not a command line, so a `/word` in its description is left alone — and a chip
   chosen after typing `/other` is the later intent, so it wins.
2. The message starts with `/<skill>` followed by whitespace or the end — that skill, prefix
   stripped. An **unknown** `/name` is not an error: it is left in the text and falls through, so
   somebody typing `/shrug` does not break the bot. `/etc/hosts` is not a skill selector either.
3. The channel is in a skill's `slack_channels`. Two skills claiming one channel is a startup
   error.
4. **The source's own default**: the web chat's `profile.yaml: chat_default_skill`. Every chat
   message carries it, which is exactly why it is only a default — a `/skill` a human typed is
   more specific than a per-source preference, and wins.
5. `profile.default_skill`.

Rule 1 is knowledge and rules 4 and 5 are fallbacks, and keeping them apart is the whole of the
order: Linear names the skill because it genuinely knows it, while the web chat merely *prefers*
one. Slack says neither and starts at rule 2.

**One session, one skill**, fixed when the thread's session is created. A later `/other` in the
same thread is refused politely: start a new thread. A default is not somebody naming a skill,
so it never triggers that refusal.

Changing a skill **file** needs a restart. There is no SIGHUP reload. A skill made in the web
UI does not — see *Skills in the web UI* below.

### Skills in the web UI

A profile does not have to live only on the conductor's host. **Agent → Skills** in the web UI
creates, edits and deletes skills, and **Agent → Profile** sets the display name, the model and
the two default skills, so a skill's image, prompt, tools, limits, environment and the secrets
it names are defined in a browser instead of by editing YAML over SSH.

The three decisions worth knowing before you use it:

**Where it is stored.** A UI-defined skill is a row in the conductor's own database
(`podium_agent`), table `skills`, one row per skill. The `definition` column holds the same
document a `skills/<name>.yaml` holds, as JSON — same keys, same validation, same defaults. The
profile overrides are one row in `settings`, under the key `profile.overrides`. Nothing is
written to the profile directory: `PODIUM_AGENT_PROFILE_DIR` is mounted read-only in the shipped
compose file and stays that way.

**The files win.** A `skills/<name>.yaml` is authoritative for the name it holds:

| | |
|---|---|
| a name only the files define | the file's skill runs; the UI shows it **read-only**, because the file is where it is defined |
| a name only the database holds | the stored skill runs; the UI can edit and delete it |
| a name **both** define | the **file** runs. The stored row is shown as **shadowed**, says so, never runs, and the only thing you can do to it is delete it |

Creating a skill whose name a file already defines is refused outright, so the shadowed state is
only ever reached by adding a file for a name the database already had. The rule is deliberately
not "the most recent write wins": which of two definitions runs must never depend on which was
saved last, and a GitOps deployment must stay the authority over the names it ships. Editing a
file-defined skill means editing the file and restarting the conductor, exactly as before.

Profile *settings* work the other way round, because they are not definitions with a name but
single values with one writer: `profile.yaml` supplies the default and a field set in the UI
overrides it. The screen shows the file's value beside each field, marks which are overridden,
and clearing a field returns it to the file's.

**How a change reaches a running conductor.** Immediately, with no restart and no signal. The
conductor holds its profile in a live holder (`profiles.Live`) that every reader takes a snapshot
from per use; a write through the API validates the change, stores it, rebuilds the whole profile
and swaps the new one in atomically. The next turn is routed against the new profile. A turn
already in flight is untouched — it took its skill by value when it started, so nothing about it
can change under it. Every conductor also re-reads the stored half every 15 seconds, which is
what makes a second conductor on the same database, or a row changed with `psql`, land as well.

The profile directory itself is still read **once, at start**. That half is a deploy artefact and
re-reading a file somebody is half way through saving is not an improvement.

**What is refused.** A skill made in a browser is validated by exactly the code that validates a
skill file — same rules, same messages — so nothing is accepted here that a file could not say,
and nothing is stored that would fail to load at the next restart:

- everything in the table above (`image`, `allowed_tools`, `max_turns`, `timeout`, `resources`,
  `env`, `labels` and `secrets` are checked by the task-spec validator, because that is where
  they end up);
- the two reserved secret names and the three reserved env vars, below;
- `system_prompt` must be the prompt itself. `file:` works only in a `skills/<name>.yaml`, which
  has a file beside it to resolve the path against;
- anything that would make the merged profile ambiguous: two skills claiming one Slack channel,
  two setting `linear: true`, a default naming a skill that is not loaded. Deleting the skill
  `default_skill` names is refused for the same reason.

**What is not restricted.** A skill may name **any registered secret**, exactly as a task spec
may. There is no allow-list and there will not be one: `CreateTask` checks only that a named
secret exists, so anyone who can reach the control plane can already mount any secret into an
image of their choosing — restricting the skill path alone would be theatre. See
[`security.md`](security.md#5-the-conductor-and-the-bot). The UI shows secret **names** only;
there is no way to read a value back through any API in Podium.

The **image is free text you supply**. Podium ships no picker and assumes no catalogue: the only
requirement is that the image implements the turn-brief protocol, and `FROM
ghcr.io/alvaroibarguen/podium-agent-runtime` is the easy way to get one that does.

### Reserved secret names

Set with `podium secret set`, except the first two: the web UI sets the Anthropic key (see
*Setting the provider key* below) and the conductor writes the memory key at startup out of
its own environment.

| name | lands as | who needs it |
|---|---|---|
| `podium.agent.anthropic_api_key` | `ANTHROPIC_API_KEY` | every turn; the conductor attaches it. Set it in the web UI, or with the CLI |
| `podium.agent.github_token` | `GITHUB_TOKEN` | the `coder` skill — the only one whose file names it. See *The coder skill* |
| `podium.agent.memory_api_key` | `PODIUM_MEMORY_API_KEY` | every turn on a host with memory; the conductor attaches it, **and writes the secret itself** from `PODIUM_AGENT_MEMORY_API_KEY` |
| `podium.agent.warehouse_url` | `WAREHOUSE_URL` | the `analyst` skill, for a Postgres-compatible warehouse. See *The analyst skill* |
| `podium.agent.warehouse_credentials` | `/podium/secrets/warehouse.json` | the `analyst` skill, for BigQuery. **Set one of these two and delete the other line from the skill file** |

The Anthropic key must **exist** before any turn can run, even a dry run: the task spec names it
and the control plane refuses a task that names a secret it does not have. That failure reaches
the thread as "This bot is missing a credential".

A skill file may not name either of the first two. They are added by the conductor to every
turn: no skill decides whether the bot can talk to the model, and no skill can opt out of
memory — only the operator can, by leaving `PODIUM_AGENT_MEMORY_URL` empty.

---

## Setting the provider key

`podium.agent.anthropic_api_key` is the one credential every turn needs, dry run included, and
the web UI is where an operator sets it.

Open the UI, click **Agent** in the header (it is only there when `PODIUM_AGENT_URL` is set on
the server) and you land on **Agent → Settings**.

<!-- screenshot: the Agent → Settings tab with the Anthropic card, key not set -->

Paste the key and press **Validate & save**. What happens, in order:

1. The browser calls `SetProviderKey` on `podium-server`, which proxies it to the conductor.
2. The conductor calls **`GET {PODIUM_AGENT_ANTHROPIC_BASE_URL}/v1/models`** with the pasted key
   in an `x-api-key` header and `anthropic-version: 2023-06-01`. There is no token cost.
3. **Only if that succeeds** is the key stored, as the Podium secret
   `podium.agent.anthropic_api_key`, through the ordinary `SecretService` — so it is encrypted
   at rest under the control plane's master key like every other secret.
4. The conductor then writes a metadata row of its own: the **last four characters**, the login
   that set it, and the time. That row is what the card shows afterwards, and it is the only
   part of the key that is ever read back.

So **"Saved" means "Anthropic agreed this key works"**, which is the point: an operator who sees
it has to be able to trust that turns will run. The three failures are three different
sentences on the card:

| what the card says | what happened | was anything saved |
|---|---|---|
| *Anthropic rejected this key* | the provider refused it (HTTP 400 with an `authentication_error`, or 401/403) | no |
| *Couldn't reach Anthropic to validate* | a 429, a 5xx, a timeout or a network failure | no |
| *podium-agent is not reachable* | the conductor is down; the server's proxy said so | no |

The first two also carry **what the provider itself said**, in a second line under the headline:
`Anthropic said: …` for a refusal, `Details: …` for a provider that could not be reached. That
sentence is the actionable half. "Anthropic rejected this key" is true of a key that has been
revoked and equally true of a key that is fine but needs something Podium did not send — an
identity-linked key wants an `anthropic-workspace-id` header, and **Podium supports standard
workspace API keys only**, so it sends none. Only the provider's own words tell those apart.

The text comes from Anthropic over TLS, not out of a task container, so it is not the untrusted
task output [`security.md`](security.md) is about — but the conductor still bounds it, flattens
it to one line and scrubs anything key-shaped out of it, and the browser renders it as text and
never as markup. **The key never appears in it.**

A key with an unfamiliar prefix is **not** refused — Anthropic has changed prefixes before — but
the card says `key format looks unusual; validated anyway` next to the success line.

**Remove key** deletes the secret and the metadata row. It asks for an inline confirmation
first, because every turn fails until a key is set again. Doing it twice is not an error.

The CLI equivalents, for a host with no browser:

```sh
podium secret set podium.agent.anthropic_api_key      # value on stdin; NO validation
podium secret ls                                       # names, versions and who set them
podium secret rm podium.agent.anthropic_api_key
```

`podium secret set` skips step 2 entirely, so a typo is stored happily and the first turn is
where you find out. The UI path is the one to prefer. Either way there is **no way to read a
stored secret back**, by design — see [`security.md`](security.md#secrets).

**The two paths cannot drift.** The secret is the control plane's and the metadata row is the
conductor's, so `GetSettings` asks the control plane whether the secret exists rather than
believing its own row. Which means:

| what you did | what the Settings card says |
|---|---|
| `podium secret rm podium.agent.anthropic_api_key` | **Not set** — the stale row is ignored, not shown |
| `podium secret set …` over a key the UI had saved | **Connected**, and *set outside this UI*: there is a key, and the stored hint is about the one it replaced, so it is withheld rather than shown beside a key it is not about |
| `podium secret set …` on a conductor that never saw the UI | **Connected**, *set outside this UI* |
| the control plane is unreachable | the last known state, and the conductor logs that its answer may be stale |

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

## Linear

Assign an issue to the bot and it picks the work up. Comment on that issue and it takes another
turn. Nothing else is a trigger, and nothing about Linear reaches this host inbound: the
conductor **polls**, on a timer, dialling out. Podium does not accept a connection it did not
already accept, and a webhook would be one.

### Setting it up

1. **Make the bot a user in your workspace.** Invite an address you control —
   `podium-agent@yourcompany.com` — and accept the invitation. It needs a normal seat, not an
   integration: this track uses a personal API key, and Linear's OAuth *agent* seats change the
   assignment field (see the caveat below).
2. **Signed in AS THE BOT USER**, go to **Settings → Security & access → Personal API keys → New
   API key**. Copy it into `PODIUM_AGENT_LINEAR_API_KEY`. A key made from *your own* account
   would make the bot read your issues and comment as you.
3. **Exactly one skill must set `linear: true`.** That is the skill every ticket runs. The
   example is [`../examples/agent/skills/coder.yaml`](../examples/agent/skills/coder.yaml). Two
   skills claiming it is a start-up error; zero is fine until a Linear key is set, and then the
   conductor refuses to start and says so.
4. **Name a state `In Progress`** on the teams the bot works in, or accept the fallback (below).
5. Start the conductor. `linear source connected` in the log names the user the key belongs to.
   A key that is set and does not work makes `podium-agent` **exit non-zero at boot**, naming
   Linear — a misconfigured key must not be discovered an hour later by nobody picking a ticket
   up.

Then assign an issue to the bot. Within the poll interval it moves to **In Progress**, a
`👀 working…` comment appears, and when the turn ends the comment says `✅ Done — see below` and a
second comment carries the answer.

### What happens per tick

Every `PODIUM_AGENT_LINEAR_POLL_INTERVAL` (default 30s, floor 10s) the conductor asks for issues
assigned to the bot and updated since its watermark, 50 per page, following `hasNextPage` inside
the same tick. Per issue:

| the issue | what happens |
|---|---|
| its state's type is `completed` or `canceled` | nothing, ever. A finished ticket gets no turns however it was edited. |
| no session exists for it yet | **an assignment**: one turn, with the identifier, the title and the description as the instruction. |
| a session exists | **a follow-up**, if a human commented after the last turn started. Several new comments in one tick are joined into **one** instruction, oldest first, and are therefore one turn. |

The bot's own comments never start a turn. Linear reports a comment's author in `user`, which is
**null** for anything written by an integration or a bot without a user association, and sets
`isMe` on anything written through this key — all three cases are excluded.

The watermark (`linear_cursor.issues_updated_at`) is written **once per tick, after every page's
events have been handed over**. A crash in between replays the tick, which the session gate and
the last-turn comparison both absorb; the alternative loses tickets. On the first run of a fresh
database it starts 24 hours back, so an assignment made while the conductor was down for a day is
still picked up and nothing older is.

### What it says back, and where

Two comments per turn, at most, however long the turn:

- The first progress post creates the **working comment**. Later progress edits it, at most one
  edit every two seconds. When the turn ends that same comment becomes `✅ Done — see below` or
  `❌ Failed`.
- The answer is a **second comment**: the final text verbatim, then any attachments, then a
  footer line naming the Podium task. Screenshots are uploaded to Linear's asset store and
  embedded in that comment, so six screenshots are still one comment.
- A turn that fails also gets the conductor's own plain-words explanation as that second comment.
  A raw error never reaches it.

Attachments over **50 MB**, or an upload Linear refuses, fall back to a link to
`<PODIUM_AGENT_UI_URL>/tasks/<task_id>`. The reader has to be signed in to Podium to open it.

**`React(done)` does not move the ticket.** Whether a ticket is finished is decided by a human
reading the pull request, not by the bot having opened one. The only state change Podium makes is
the move to In Progress when a turn starts.

### The `In Progress` convention

The state a turn moves an issue into is resolved per team, once per process:

1. the state whose **name** is `In Progress` (case-insensitively);
2. failing that, the lowest-positioned state whose **type** is `started` — a renamed column is
   still the column that means started;
3. failing that, nothing: the log says so and the turn runs anyway. A board Podium does not
   understand is not a reason to refuse the work.

### Why polling, and what would be better

Linear's own documentation discourages polling and points at webhooks, and it is right: an
`AppUserNotification` webhook carries an **`issueAssignedToYou`** action, which is exactly the
predicate this code has to reconstruct, and `AgentSessionEvent` fires on assignment to an agent.
Both require an OAuth `actor=app` application, which this track deliberately does not build, and
an inbound HTTP endpoint, which this host deliberately does not expose. So: polling, at one query
per tick against a budget of 2,500 requests an hour — two orders of magnitude inside it. If a
future step builds the OAuth app, the webhook is the better mechanism and this is the place to say
so.

Two consequences of using a personal API key, both worth knowing:

- **There is no `assignedAt` field and no assignment-time filter in Linear's API.** An
  `updatedAt` watermark fires on *any* change to an assigned issue, so "newly assigned" is
  reconstructed from Podium's own `sessions` table, not from the query.
- **An OAuth `actor=app` bot would be the issue's `delegate`, not its `assignee`.** A personal
  key on a normal seat is the `assignee`, which is what this code filters on. If the bot is ever
  moved to an app seat, the filter has to change with it.

---

## The coder skill

The one skill that writes code:
[`../examples/agent/skills/coder.yaml`](../examples/agent/skills/coder.yaml), with its working
agreement in [`../examples/agent/prompts/coder.md`](../examples/agent/prompts/coder.md). It is
what Linear tickets run, and it is reachable from Slack as `/coder write a PR that …`.

A coder turn reads the request, works on a branch, runs the repository's own tests, verifies a UI
change by taking a screenshot of it, opens a **draft** pull request, and ends with the PR URL on
the last line of its answer. Screenshots it names are attached to the reply.

### The GitHub token

The skill file names one secret, and a skill only ever gets the secrets its own file names:

```yaml
secrets:
  - { name: podium.agent.github_token, target: env, key: GITHUB_TOKEN }
```

Set it once, as an operator:

```sh
podium secret set podium.agent.github_token      # the value on stdin
```

Make it a **fine-grained personal access token**, scoped to exactly the repositories in the
skill's `repos:` list, with **Contents: read and write** and **Pull requests: read and write** and
nothing else. Not `repo` on a classic token, which is every repository the owner can see. Not
`workflow`. Rotate it on a schedule; a token that never expires is a token nobody will notice the
loss of.

- It reaches `coder` tasks and no others, because no other skill file names it. The `general`
  skill's turns do not have it.
- Inside the container it never appears in an argument vector or in `.git/config`: git gets it
  through a credential helper that reads the environment at the moment git asks, and `gh` reads
  `GH_TOKEN`, which the runtime sets from it.
- Log redaction covers it, as it covers every injected secret — as defence in depth, not as the
  control. See [`security.md`](security.md#redaction--what-it-does-and-does-not-guarantee).

### Repositories

`repos:` is copied into the brief and the runtime shallow-clones each one into
`/workspace/repos/<name>` at the start of the turn, on its `default_branch`, with `user.name
podium-agent` and `user.email podium-agent@users.noreply.github.com`. Only `https` with a token is
supported: no SSH, no GitHub App, no GitLab.

There is no working tree carried between turns. Every turn clones again. A follow-up comment that
says "now also do X" starts from the default branch, and the agent has to find its own earlier
branch if it wants it — the branch naming convention in the prompt is what makes that possible.

### The `browser` label and Chromium

The coder skill runs the **browser** runtime image and asks for the `browser` label. That label is
a convention, not a schema: put it on the nodes that have enough free memory to run Chromium.
**2 GB free is the floor**, and the skill asks for a 4096 MB limit.

`/opt/podium-agent/bin/screenshot URL OUT.png [--width N] [--height N] [--full-page]` is the
helper the prompt tells the agent to use. It launches Chromium headless with
`--disable-dev-shm-usage`, waits for `networkidle` with a 15-second timeout, writes the PNG and
prints its path. A task container's `/dev/shm` is Docker's default 64 MB and a Podium task spec
has no knob for it, so an agent writing its own Playwright must pass the same flag. Chromium
refuses a full-page capture of a very long page on its own account — take the viewport instead.

---

## The analyst skill

`examples/agent/skills/analyst.yaml` and `examples/agent/prompts/analyst.md`. It is what the web
chat starts with (`profile.yaml: chat_default_skill: analyst`) and it is reachable from Slack as
`/analyst …`. It answers a question from the data warehouse, shows the SQL it ran, and attaches a
CSV or a PNG when the answer does not fit in a bubble.

It runs `podium-agent-runtime-data`, which carries `psql`, `bq`, the `duckdb` CLI, and `python3`
with `matplotlib` and `pandas` (`MPLBACKEND=Agg`, so a chart needs no display). It deliberately
does **not** inherit the browser image: a warehouse query has no business carrying Chromium.

### The two credential modes

A deployment sets **one** of these and **deletes the other line from the skill file**. A task
naming a secret the control plane does not have is refused before it reaches a node, so leaving
both in place means no turn of this skill ever runs.

```sh
# Postgres-compatible: one connection string.
podium secret set podium.agent.warehouse_url        # postgres://podium_analyst:…@host/warehouse
# BigQuery: a service-account key file.
podium secret set podium.agent.warehouse_credentials --file sa.json
```

The first lands as `WAREHOUSE_URL` in the container's environment; the second as
`/podium/secrets/warehouse.json` on a tmpfs, and the prompt exports
`GOOGLE_APPLICATION_CREDENTIALS` at it. The prompt checks which one is there rather than assuming.

### The read-only role is the control, not the prompt

The prompt says not to modify data. **That is a courtesy.** The thing that actually stops a turn
writing to your warehouse is the credential it is given, so give it one that cannot write:

```sql
create role podium_analyst login password '…';
grant connect on database warehouse to podium_analyst;
grant usage on schema public to podium_analyst;
grant select on all tables in schema public to podium_analyst;
alter default privileges in schema public grant select on tables to podium_analyst;
alter role podium_analyst set statement_timeout = '60s';
alter role podium_analyst set default_transaction_read_only = on;
```

With that role, an `update` fails with `ERROR: cannot execute UPDATE in a read-only transaction`
before it touches a row, and a runaway query is stopped by the statement timeout rather than by
somebody noticing. `alter default privileges` covers tables created after the grant; run the
`grant select on all tables` again after a schema migration adds one to an existing schema.

For BigQuery, the equivalent is a service account with `roles/bigquery.dataViewer` on the
datasets it may read plus `roles/bigquery.jobUser` on the project so it can run a query at all —
and **not** `dataEditor`, `admin` or `roles/bigquery.user`.

### Why the row limits

The prompt keeps a result table in the answer to 50 rows and writes anything longer to a CSV
under `/workspace/.podium/artifacts/`, which Podium attaches to the message. That is not a
formatting preference:

- **Everything in an answer is stored.** A chat answer is a row in `chat_messages` and a Slack
  answer is a message in a channel, both for ever.
- **The transcript is fed back.** Every later turn of the same conversation reads the whole
  transcript, so a thousand-row dump eats the 256 KiB brief and crowds out the actual question.
- An attachment is a file behind `GET /artifacts/{id}`, which is behind the same identity as
  everything else, and it is not in the transcript.

### What it retains

One memory per answered question: the metric, the definition used, and the shape of the query.
**Never the numbers** — they go stale and a stale number read back as fact is worse than no
memory — and **never row-level data**. That rule is in the prompt, and like the "do not modify
data" rule it is a courtesy rather than a control: see
[`security.md`](security.md#the-analyst-and-your-warehouse).

---

## The limits that bite

- **The brief is capped at 256 KiB encoded.** The conductor drops the oldest transcript entries
  until it fits and sets `transcript_truncated: true`, which the runtime tells the model about. A
  brief that does not fit even with an empty transcript fails the turn with "this conversation is
  too large" — start a new thread with just the question.
- **One turn per session at a time.** A busy thread queues rather than parallelises; the web
  chat refuses the second message outright, because a browser can be told before it tries.
- **32 KiB per chat message** from a human. The whole conversation has to fit the brief.
- **50 rows** in an analyst's answer, by prompt; longer results become a CSV attachment.
- **4000 characters per Slack message.** Longer answers arrive as several messages.
- **25 MB per attachment** out of Podium, and **50 MB** into Linear's asset store; over either,
  the reply carries a link to the task page instead of the file.
- **Two comments per Linear turn.** Progress edits one of them; it is not a running commentary.
- **The Linear poll interval** is how long an assignment waits before anything happens: up to 30
  seconds by default, and never less than 10.
- **No RBAC, anywhere.** See below.

---

## No RBAC

**Whoever can tag the bot, or assign it a ticket, can run code on a worker with that skill's
credentials.** There is no allowlist of users, no roles, and no read-only mode. Keep `secrets:`
minimal per skill, and do not put a credential in a skill that anybody in a public channel can
reach — but do not mistake that for a boundary around the secret store. `CreateTask` checks only
that a named secret **exists**, so anyone who can reach the control plane can already mount any
registered secret into an image and a command of their own. See
[`security.md`](security.md#5-the-conductor-and-the-bot).

The two Slack tokens are as sensitive as `PODIUM_DEV_TOKEN`. So are the Linear API key (full
read/write of everything that user can see) and the GitHub token (write access to the listed
repositories). So is `PODIUM_AGENT_TOKEN`, which is the only thing guarding every session the bot
has had.

Text relayed out of a task is **untrusted content**. The conductor posts it verbatim and acts on
none of it: it never parses an answer for a command, a channel name or a user ID. See
[`security.md`](security.md#5-the-conductor-and-the-bot).

The agent runtime runs the SDK with `bypassPermissions` and `settingSources: []`. That means no
tool call inside a turn asks anybody anything, and nothing on the worker's disk — no `~/.claude`,
no `CLAUDE.md` out of a cloned repository — changes what the agent does. The container is the
sandbox; the permission prompt is not. `docs/security.md#3-a-task-container--untrusted` is the
boundary being relied on.

---

## Memory

Every turn, whatever its skill or source, shares **one** memory that outlives the container it
ran in. It is [Hindsight](https://github.com/vectorize-io/hindsight): one container on the
control-plane host, its own database inside the Postgres Podium already runs, and an MCP server
the agent talks to directly. **Podium stores no memory of its own** — the conductor wires a URL
and a key into each turn, retains one item after each successful turn, and the web UI reads and
forgets memories through the REST API.

Memory is optional. Leave `PODIUM_AGENT_MEMORY_URL` empty and every turn runs without one:
briefs carry no `memory` block, nothing is retained, `/readyz` does not probe it, and the Memory
tab says so.

### What an agent can do with it

The runtime adds three MCP tools to every turn's allow-list, whatever the skill file says, and a
**Memory** section to the system prompt telling the model when to use them:

| tool | for |
|---|---|
| `mcp__memory__recall` | before working on anything that could have history — a repository, a person, a recurring question, a decision already taken |
| `mcp__memory__retain` | when it learns something durable and organisation-wide: a decision and the reason, how a system fits together, who owns what, a convention |
| `mcp__memory__list_tags` | seeing how the bank is organised |

The prompt also says, in these words: never retain a secret, a credential, or personal data
about an individual; and this memory is shared by every agent, so anything you retain another
agent will read.

### What the conductor retains

One item per **successful** turn that actually answered:

```
content:      "<author> asked: <instruction>\n\n<display_name> answered: <final text>"
context:      "podium agent, skill <skill>"
tags:         ["source:<kind>", "skill:<name>"]
metadata:     {session_id, turn_id, task_id, source_ref, source_url}
document_id:  <turn_id>
```

The exchange, not the transcript: the memory engine extracts facts from prose, and a tool log
would produce noise. The answer is capped at 8 KiB — the whole transcript is already an artifact
of the task. `document_id = turn_id` is the idempotency key: retaining the same turn again
replaces what was there rather than adding a duplicate.

**Nothing is retained** for a turn that failed, was lost, was cancelled, ran out of turns, said
nothing, came from the test-only dev source, or whose answer contains a redaction marker
(`[redacted:…]`) — a marker means the node caught a secret on its way out of the container, and
a memory every future turn reads is the last place that belongs.

A retain happens **after** the answer is posted and the outcome is on the triggering message, on
its own goroutine with a 10-second budget. A memory outage is a log line and a bump on
`podium_agent_memory_retain_total{result="error"}`. It is never said in the conversation, and it
never fails or delays a turn.

### Reading and forgetting, in the UI

`/agent/memory` lists what is remembered, newest first, with a search box over it. Every card
carries its provenance — the source (linked back to the Slack thread or the ticket where the
source URL is known), the skill, when it was learned, and the task it came out of — and a
**Forget** button behind an inline confirm.

**Forgetting is a tombstone, not a row deletion.** The memory engine has no single-memory
delete: `DeleteMemory` sets the memory's curation state to `invalidated`, which excludes it from
every future recall, from consolidation and from the entity graph, prunes the observations
derived from it, and keeps the row in an archive for audit. What the operator asked for holds —
no future turn sees it and neither does this UI — but the record of it having existed is not
destroyed. It is reversible through the memory engine's own API, which Podium does not surface.

Only `world` and `experience` facts are curatable this way. An `observation` is derived by
consolidation and disappears when the facts under it do.

### Why the Memory tab exists

**Shared memory is a prompt-injection amplifier.** A fact planted by one poisoned turn — out of
a Slack message, a ticket, a README in a cloned repository — is recalled by every later turn,
including turns for other people in other channels. There is no automated defence against that
in this track. What there is:

- provenance on every item, so a human can trace a memory back to the conversation that made it;
- this list, where a human can see what the organisation "knows";
- the Forget button;
- and the runtime prompt's rules, which a determined injection will talk past.

Read the list occasionally. Forget anything that looks wrong.

### Deployment

The `hindsight` service in [`../deploy/docker-compose.yml`](../deploy/docker-compose.yml), pinned
by tag **and** digest. Notes an operator needs:

- **It has no authentication by default.** `HINDSIGHT_API_TENANT_EXTENSION` plus
  `HINDSIGHT_API_TENANT_API_KEY` is what turns it on, for the REST API and the MCP endpoint
  alike, and the compose file makes both mandatory. The value is `PODIUM_AGENT_MEMORY_API_KEY`,
  and it is **full read/write of every memory the organisation has**.
- **Port 8888 is published on all interfaces**, because a turn's container reaches the host
  through the Docker bridge gateway and a service on `127.0.0.1` is not reachable from there.
  Firewall it down to the bridge and tailnet ranges — see
  [`networking.md`](networking.md#reaching-the-shared-memory-from-a-worker).
- **Its own Anthropic key**, `PODIUM_MEMORY_LLM_API_KEY`, read at container start, so it comes
  from `.env` rather than from the web UI's secret store. It may be the same key the agents use,
  and its calls are billed like any other. `PODIUM_MEMORY_LLM_MODEL` chooses the model: this is
  background work over short prose, so a cheaper model is a reasonable choice.
- **The models are baked into the image.** It comes up in seconds with no HuggingFace egress and
  needs no volume of its own — every byte of state is in `podium_memory`.
- **Hindsight's own web UI (port 9999) is not published.** Podium's UI is the front door.
- `podium_memory` needs the `pgvector` extension in `public`, which
  [`../deploy/postgres/init.sql`](../deploy/postgres/init.sql) creates. Postgres runs init
  scripts only on an empty data directory; the recipe for an existing install is in
  [`operations.md`](operations.md).

### Backing it up

`podium_memory` is the only copy of everything the agents have learned. It is not a cache and it
cannot be rebuilt: the conversations it was extracted from are in Slack, but the extraction cost
money and the facts are not in Podium anywhere else.

```sh
docker compose exec -T postgres pg_dump -U podium -Fc podium_memory > podium_memory.dump
```

See [`operations.md`](operations.md#backup-and-restore) for the rest of the databases.

### Webhooks and other things not built

Per-user or per-skill scoping (one bank, everyone sees everything — the tags are recorded for a
later step and never filtered on), mental models, knowledge pages and `reflect` are all memory
engine features Podium does not surface.

---

## Chat

`/agent/chat` in the web UI is the third place a turn can start from, and the only one whose
conversation Podium itself holds: Slack has threads and Linear has issues, and a browser has
nothing, so the `chats` and `chat_messages` tables in `podium_agent` **are** the conversation.

- **A chat belongs to the login that created it**, and `ListChats` returns nobody else's. There
  is no RBAC in this track and this is not one — it is a partition, and it is free. Knowing
  another login's chat id gets you `not_found`, not access.
- **One turn at a time per chat.** The composer is disabled while a turn runs and
  `SendChatMessage` answers `failed_precondition` if something tries anyway. It is the same
  turn-based rule as everywhere else: a turn ends with an answer and exits.
- **Progress is not stored.** While a turn runs, the line under the last question is the latest
  `progress` message the runtime sent, pushed to whoever is watching and then forgotten. Reload
  and you see the question and the answer, which is what actually happened.
- **A failure is stored**, so a turn that died leaves words behind rather than a question that
  looks ignored.
- **Attachments come from the task's artifacts.** An answer that names a file it wrote under
  `/workspace/.podium/artifacts/` gets that file attached: the conductor resolves the name to an
  artifact id and stores the id, and the browser fetches `GET /artifacts/{id}` with its own
  credential. Raster images render inline (capped at 480px); everything else, SVG included,
  is a download chip. Note that **a Podium artifact usually has no content type**: the node
  records one only when a task calls `podium-runner artifact add --content-type`, and a file
  the agent simply writes into the artifacts directory is collected with none — so the
  chat decides from the file's extension when the store has nothing to say.
- **Which skill a message runs**: the skill chip beside the composer, which starts at
  `profile.yaml: chat_default_skill` (falling back to `default_skill`). Typing `/analyst …` works
  too — it moves the chip in the browser, and on the wire a typed `/skill` beats the chat
  default even when the chip is left unset, as an API client leaves it. The chip itself still
  wins over a prefix: it is the last thing the human touched. A conversation keeps the skill it
  started with — the same one-session-one-skill rule as a Slack thread — so switching the chip
  in an existing chat is refused with a sentence saying to start a new one.
- **`StreamChat` is a server-streaming RPC** and it never ends on its own: it replays everything
  after `from_seq`, then follows. The browser reconnects with the highest seq it has seen, which
  is exactly once — no gap and no repeat. Frames are fanned out in process; the conductor is one
  process by design and the two tables are the durable half.

Answers are rendered through a deliberately small markdown subset — paragraphs, fenced code,
inline code, bold, italic, `- ` lists, and `http(s)` links. **No raw HTML is ever emitted and any
other link scheme renders as literal text**, because an answer is content a task wrote out of
material somebody else supplied. Ask for a table and you get a fenced block, which is what the
analyst prompt asks the model for.

What is deliberately not built: renaming or deleting a chat, sharing one, a model-written title,
uploading a file into the chat, and streaming the model's tokens. The unit of streaming is the
`progress` message the runtime sends, not a token.

---

## The conductor's API

One Connect service, `podium.agent.v1.AgentService`, served on `PODIUM_AGENT_LISTEN` under
`/podium.agent.v1.AgentService/` behind the bearer:

| rpc | what it is for |
|---|---|
| `ListSessions`, `GetSession`, `ListTurns` | the Sessions tab: every conversation and every turn |
| `GetSettings`, `SetProviderKey`, `ClearProviderKey` | the Settings tab: the provider key |
| `ListMemories`, `SearchMemories`, `DeleteMemory` | the Memory tab: what the agents remember, and forgetting one |
| `ListSkills` | the chat's skill chip: name, image, prompt hint, which is the chat default |
| `CreateChat`, `ListChats`, `SendChatMessage` | the Chat tab: the caller's own conversations |
| `StreamChat` (server-streaming) | one chat, replayed from a seq and then followed live |

**A browser reaches it only through `podium-server`.** With `PODIUM_AGENT_URL` set, the server
mounts that one prefix behind its own identity middleware and reverse-proxies it, and on the way
it deletes any client-supplied `Authorization` and `X-Podium-Login` and sets its own: the
conductor's bearer, and the login of the caller it authenticated. So the web UI keeps one
origin, one login and one CSP — `connect-src 'self'` is unchanged — and the conductor never sees
the dev token. A node identity is refused with 403 before anything is forwarded.

The conductor's `/healthz`, `/readyz` and `/metrics` are deliberately **not** proxied: they are
its own operational surface and they are unauthenticated on its own listener.

The server's `/readyz` does **not** probe the conductor. A control plane whose bot is down is
still a working task runner, and `/readyz` is what a load balancer gates on. Check the
conductor's own `/readyz` instead.

**Keep `PODIUM_AGENT_LISTEN` on loopback.** `X-Podium-Login` is a plain header and the bearer is
the only proof of where it came from; see [`security.md`](security.md).

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

# Optional: the shared memory, behind the dev compose's `memory` profile.
PODIUM_MEMORY_LLM_API_KEY=$ANTHROPIC_API_KEY \
  docker compose -f deploy/docker-compose.dev.yml --profile memory up -d --wait hindsight

make build agent-runtime
# Start podium-server as in docs/quickstart.md, plus the two variables that mount the proxy:
#   PODIUM_AGENT_URL=http://127.0.0.1:8090 PODIUM_AGENT_TOKEN=agenttoken
# then podium-node, then the conductor below, and set the key in the UI at /agent/settings.
# The CLI way, if you would rather not open a browser:
podium secret set podium.agent.anthropic_api_key            # value on stdin, no validation

PODIUM_AGENT_SERVER=http://127.0.0.1:8080 \
PODIUM_AGENT_API_TOKEN=devtoken \
PODIUM_AGENT_DATABASE_URL=postgres://podium:podium@127.0.0.1:5432/podium_agent \
PODIUM_AGENT_TOKEN=agenttoken \
PODIUM_AGENT_PROFILE_DIR=examples/agent \
PODIUM_AGENT_SLACK_APP_TOKEN=xapp-... \
PODIUM_AGENT_SLACK_BOT_TOKEN=xoxb-... \
PODIUM_AGENT_LINEAR_API_KEY=... \
PODIUM_AGENT_UI_URL=http://127.0.0.1:8080 \
PODIUM_AGENT_MEMORY_URL=http://127.0.0.1:8888 \
PODIUM_AGENT_MEMORY_API_KEY=memtoken \
./bin/podium-agent
```

The Slack, Linear and memory variables are all optional; drop any of them to run without that
source or without a memory. A `coder` turn also needs `podium secret set
podium.agent.github_token` and a node labelled `browser` — `PODIUM_NODE_LABELS=browser` on the
node daemon, or `--label browser` on the enrollment token. Note that
`PODIUM_AGENT_MEMORY_TASK_URL` keeps its default (`http://host.docker.internal:8888`) even here:
the conductor reaches the service on loopback, and a turn's container reaches it through the
bridge gateway. The dev compose publishes it on loopback only, which is fine for the conductor
and **not** for a task — set `PODIUM_MEMORY_BIND=0.0.0.0` if you want a real turn to recall.

Without the Slack tokens and the Linear key it starts and listens to nothing, which is a fine way
to check the profile loads and the database migrates — and it is enough for the Agent screen in the UI: the
Settings tab needs no source at all.

To drive one turn with no Slack and no model at all, see
[`../examples/agent/README.md`](../examples/agent/README.md), which runs the runtime image
directly, or add `PODIUM_AGENT_DEV_SOURCE=true` and post to `/dev/inbound`.
