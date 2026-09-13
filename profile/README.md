# Podium's own bot

The profile the real deployment runs: `PODIUM_AGENT_PROFILE_DIR` points at this directory,
and `PODIUM_AGENT_SKILLS_DIR` at [`../skills`](../skills) beside it.

```
profile.yaml            the bot's identity, model and default playbook
playbooks/general.yaml  answer the question in the thread
playbooks/podium.yaml   the dogfood: develop Podium itself
prompts/                one prompt per file above, resolved by `file:`
```

`playbooks/` inside a profile directory is `profiles.Load`'s layout and not a choice — a
profile directory is a `profile.yaml` and a `playbooks/` beside it. This directory is called
`profile/` so that the nested one is the only `playbooks/`: `profile/playbooks/podium.yaml`
names a thing, where `playbooks/playbooks/podium.yaml` only named a level. It is also what
`PODIUM_AGENT_PROFILE_DIR` is called, which is the variable that points here.
[`docs/agent.md`](../docs/agent.md#the-profile-directory) is the reference for every field.

**This is not the worked example.** [`examples/agent`](../examples/agent) is that, and the two
are separate directories on purpose: this profile clones this repository, holds a GitHub
token, asks for a privileged node and a browser, and none of that belongs in the first thing
a reader copies. `deploy/run-host.sh` therefore still defaults to `examples/agent`, so a first
`make stack-up` gets a bot that loads and runs anywhere; the operator of the real bot sets

```sh
PODIUM_AGENT_PROFILE_DIR=/path/to/podium/profile
PODIUM_AGENT_SKILLS_DIR=/path/to/podium/skills
```

in `deploy/.env`, and both are commented in [`../deploy/.env.example`](../deploy/.env.example).

Changing a file here needs the conductor to re-read this directory: **Re-read the files**, on the
Playbooks screen or on Agent → Assistant. No restart, and no SIGHUP reload. A skill in
`../skills` needs neither: the library is resolved at the start of every turn.

---

## Run Podium's own bot yourself

For a developer working on Podium. This directory plus [`../skills`](../skills) is a bot that
develops *this repository*: it clones Podium, runs `make test`, `make lint` and
`make test-integration` against a real Docker daemon, opens a draft pull request, and then
attacks it in a browser before saying it is done. `playbooks/podium.yaml` is that playbook and
`prompts/podium.md` is what it is told.

**Nothing installs it.** The `podium-agent` image ships `examples/agent` at `/etc/podium/agent`
and only that, and `deploy/run-host.sh` defaults there too — so a first `make stack-up` gets a
bot that runs anywhere and holds nothing. The four steps below are how this one arrives instead,
and they are four steps rather than a default because a privileged node, a GitHub token with
write access to Podium and 8 GB of RAM are things you should have decided to hand over.

### 1. Point the conductor at this directory

Two lines in `deploy/.env`, both commented in [`../deploy/.env.example`](../deploy/.env.example).
They travel as a pair: `podium.yaml` names a skill, and a playbook whose skill is missing fails
every turn rather than running without it.

```sh
PODIUM_AGENT_PROFILE_DIR=/path/to/your/podium/profile      # this directory
PODIUM_AGENT_SKILLS_DIR=/path/to/your/podium/skills    # ../skills
```

Point them at your clone and `git pull` keeps the bot current. A deployment that is not a
checkout copies both directories instead — `/srv/podium/profile` and `/srv/podium/skills` in the
example — and the conductor only ever reads them.

### 2. Give it a node it can actually run on

`podium.yaml` sets `docker: true`, which gets a real Docker daemon as a sidecar so that
`make test-integration` and `make e2e` have an engine to boot containers on. A daemon is
privileged, and Podium places tasks on **labels alone** — it knows nothing about which nodes
allow privilege — so the flag and the label are a pair you set together:

```sh
podium-node --allow-privileged-sidecars --label privileged   # …and the rest of your flags
```

`labels: [privileged]` in the playbook is what matches it. Get it wrong in this direction — a
label on a node without the flag — and the turn fails at provisioning naming the flag, which is
the failure you want. That node also needs **8 GB free for the task** and room for the image.

### 3. Give it the token it opens pull requests with

```sh
printf %s "$GITHUB_TOKEN" | ./bin/podium secret set podium.agent.github_token
```

`secrets:` in the playbook is what delivers it, as `GITHUB_TOKEN` in the task container. No
model credential goes there — the conductor attaches that to every turn itself. Scope this token
to Podium and nothing else: it is the one place a playbook hands a model's output write access
to your code, and [`../docs/security.md`](../docs/security.md) is the argument for treating that
as a decision rather than a setting.

### 4. Choose which `-dev` image it runs

`podium.yaml` names `podium-agent-runtime-dev:dev`, the tag `make agent-runtime` writes. That is
right when you are changing the agent runtime itself — the image then carries your working tree
— and it is why it is the default here. It is also **local to one machine**: a node runs tasks on
its own engine, so a tag built on your laptop is invisible to a worker anywhere else.

If you are not changing the runtime, name the published image instead and skip a ~1.7 GB build
per architecture. A `v*` tag publishes it from `.github/workflows/release.yml`, `FROM` the base
image of the same release, by digest:

```yaml
# profile/playbooks/podium.yaml
image: ghcr.io/podium-ade/podium-agent-runtime-dev:v0.1.0   # pin it; `latest` drifts
```

Pin it to the version of the conductor reading this file. A conductor and its runtime are a
matched pair, which is why the playbooks that name *no* image get the conductor's own version
filled in — this one cannot use that, because it needs the `-dev` variant and not the base.

### Then check it before you trust it

```sh
./bin/podium playbooks         # `podium` is listed, with its image and labels
```

Ask for it by name in a thread — `/podium <something small>` — and watch it open a draft pull
request. The first turn is what finds a missing token, an unlabelled node or an image the node
cannot pull; find those on a typo fix rather than on work you needed.

### What you get, and where each part lives

| | |
|---|---|
| `playbooks/podium.yaml` | the environment: the image, the daemon, the browser, 8 GB, the token, the repo |
| `prompts/podium.md` | the instructions: verify with `make`, open a draft PR with its full URL, then attack it |
| [`../skills/validate-pr`](../skills/validate-pr/SKILL.md) | the procedure that last paragraph reaches for, granted by the `skills:` line |
| `ghcr.io/podium-ade/podium-agent-runtime-dev` | the image: Go, make, pnpm, golangci-lint, the Docker **client**, the browser's MCP client |

The split between the last two is the one worth knowing. A **skill is a procedure** — how to
attack a change: empty states, oversized input, double submits, a phone-sized viewport, the
console on a screen that renders fine. A **playbook is the environment** that makes following it
possible: `browser: true`, and the `skills:` line that grants it at all, because a turn's harness
denies every skill it has not been told about by name. Neither is any use without the other.

And the image holds the Docker **client** only — no `dockerd`, which its smoke test asserts. An
image with an engine in it has to run privileged, and a privileged container is the wrong place
for a model's output to execute. The engine lives in a sidecar with no model attached to it, and
it dies with the task.
