# The dev kit: a playbook that writes code, and the skill that attacks it

A bot that answers questions needs a prompt. A bot that **changes a repository** needs a
container with a toolchain in it, a Docker daemon to run the tests against, a browser to look
at what it built, a credential that can push a branch, and a procedure for not believing
itself. This directory is those five things, as files you copy.

It is what [`../../playbooks`](../../playbooks) — Podium's own bot — runs against this
repository, with the repository and the prompt left blank for yours.

> **Nothing installs this.** The `podium-agent` image ships [`../agent`](../agent) at
> `/etc/podium/agent` and only that, and no compose file, env default or release artifact
> points here. A playbook that wants a privileged node, 8 GB and a GitHub token should arrive
> because somebody copied it in, which is the whole of the setup below.

| | |
|---|---|
| [`playbooks/dev.yaml`](playbooks/dev.yaml) | the environment: image, sidecars, resources, the token, the repo |
| [`prompts/dev.md`](prompts/dev.md) | the instructions: verify with the repo's own targets, open a draft PR, then attack it |
| [`../../skills/validate-pr`](../../skills/validate-pr/SKILL.md) | the procedure the prompt's last paragraph reaches for |
| `ghcr.io/podium-ade/podium-agent-runtime-dev` | the image: Go, make, pnpm, golangci-lint, the Docker client, the browser's MCP client |

## Set it up

Everything below is `cp`. `$PROFILE` is your profile directory —
`PODIUM_AGENT_PROFILE_DIR`, the one holding `profile.yaml` — and `$SKILLS` is
`PODIUM_AGENT_SKILLS_DIR`. Both are set in `deploy/.env`; see
[`../../deploy/.env.example`](../../deploy/.env.example).

```sh
# 1. The playbook and its prompt, into the profile you already run.
cp examples/agent-dev/playbooks/dev.yaml "$PROFILE/playbooks/"
cp examples/agent-dev/prompts/dev.md     "$PROFILE/prompts/"

# 2. The skill the playbook names. A playbook whose skill is missing fails EVERY turn,
#    so these two travel together.
mkdir -p "$SKILLS"
cp -R skills/validate-pr "$SKILLS/"
```

Then edit `$PROFILE/playbooks/dev.yaml` — the two lines that cannot have a useful default:

- **`repos:`** — your repository, its URL and its default branch.
- **`image:`** — pin `:latest` to the version of the conductor that reads this file.

And write the credential the playbook names, once:

```sh
printf %s "$GITHUB_TOKEN" | ./bin/podium secret set podium.agent.github_token
```

Scope that token to the repositories in `repos:` and nothing else. It is the one place a
playbook hands a model's output write access to your code — [`docs/security.md`](../../docs/security.md)
is the argument for why that is a decision and not a setting.

Finally, restart the conductor. A profile directory is read at start-up and there is no
SIGHUP reload, so a new playbook file is not live until then. The skills directory is the
exception: it is resolved at the start of every turn.

## What the node has to be

`docker: true` gets a real Docker daemon as a sidecar, and a daemon is privileged. Podium
places tasks on **labels alone** and knows nothing about which nodes allow privilege, so the
two are an operator's pair:

```sh
podium-node --allow-privileged-sidecars --label privileged   # …and the rest of your flags
```

`labels: [privileged]` in the playbook is what matches it. Get this wrong in the safe
direction — a label on a node without the flag — and the turn fails at provisioning naming
the flag, rather than hanging. See [`docs/node-setup.md`](../../docs/node-setup.md).

The node also has to be able to **pull the image**: 8 GB of memory for the task, and the
`-dev` runtime is ~1.7 GB per architecture.

## Check it before you trust it

```sh
podium playbooks                      # `dev` is listed, with its image and labels
```

Then ask for it by name in a thread — `/dev <something small>` — and watch it open a draft
pull request. The first turn is the one that finds a missing token, an unlabelled node or an
image the node cannot reach; find those on a typo fix rather than on work you needed.

## The parts, and why each one is separate

**The image is not this repository's to guess at.** `-dev` carries the toolchain *Podium*
builds with, at the versions *Podium* pins — Go from `go.mod`, golangci-lint from
`ci-go.yml`, and `agent/runtime/images/dev.test.ts` fails the build when the image drifts
from either. That makes it right for a Go + pnpm project and roughly right for anything else.
For another stack the answer is your own image `FROM ghcr.io/podium-ade/podium-agent-runtime`,
named in `image:`; [`../agent/README.md`](../agent/README.md#the--dev-image-and-extending-the-base-yourself)
is the worked example of building one, and `agent/runtime/Dockerfile.dev` is a real one to read.

**The daemon is a sidecar, not a layer.** The image holds the Docker *client* — `docker`,
`compose`, `buildx` — and no `dockerd` at all; the smoke test asserts its absence. An image
with an engine in it has to run privileged, and a privileged container is the wrong place for
a model's output to execute. The engine lives in a container with no model attached to it,
and it dies with the task.

**The skill is a procedure; the playbook is the environment it runs in.** `validate-pr` says
*how* to attack a change — empty states, oversized input, double submits, a phone-sized
viewport, the console on a screen that renders fine. `dev.yaml` is what makes that possible:
`browser: true`, and the `skills:` line that grants it at all. Neither is any use without the
other, which is why the setup above copies both and why the playbook's `skills:` entry is the
one grant that has to be spelled out — a turn's harness denies every skill it has not been
told about by name.

**The prompt is where the honesty lives.** Open the pull request, then attack it, then report
— and name what you could not prove. That ordering is deliberate: the evidence goes on the
pull request, and a turn that reports green on a change it never tried to break has verified
nothing.
