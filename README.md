<p align="center">
  <img src="docs/assets/podium-logo.png" alt="" width="200">
</p>

<h1 align="center">Podium</h1>

<p align="center">
  <b>Your own AI orchestration system. One deployment, every team, on machines you own.</b>
</p>

<p align="center">
  <a href="LICENSE"><img alt="MIT licence" src="https://img.shields.io/badge/licence-MIT-3b2fd4.svg"></a>
  <a href="go.mod"><img alt="Go 1.27+" src="https://img.shields.io/badge/go-1.27%2B-00ADD8.svg"></a>
  <a href=".github/workflows/ci-go.yml"><img alt="ci / go" src="https://github.com/podium-ade/podium/actions/workflows/ci-go.yml/badge.svg"></a>
  <a href=".github/workflows/ci-web.yml"><img alt="ci / web" src="https://github.com/podium-ade/podium/actions/workflows/ci-web.yml/badge.svg"></a>
</p>

Podium is how an organization puts AI to work on the jobs that actually move the business, and
then watches it finish them.

A bug lands in Slack or Linear, and Podium reads the code, runs what it needs to run, and posts
the cause back on the thread. A pull request needs a review, and Podium reads the diff and
writes the review. A change needs writing, and Podium edits the repository, runs the tests,
opens the pull request, and can check the result in a browser before it says it is done.
Someone asks what the numbers did last week, and Podium queries the warehouse and answers in
the same conversation. Those are four jobs. The next one is a playbook you write, for whatever
part of the organization needs it.

One deployment covers all of them. Chat, Slack, Linear, and GitHub are doors into the same
system. Each team gets its own tools, repositories, model, and credentials. When the work needs
a machine, it runs in a sandbox on a worker you enrolled, and the answer comes back where the
question started, with the logs and the files to show for it.

You run the whole thing. The control plane, the conductor, the secrets, and the workers are
yours. The model keys are yours.

---

## What it drives

A **playbook** is one job: an image, the tools that job may use, the repositories it may
touch, the model it runs on, and the secrets it is allowed to hold. A turn that needs a
machine is one run of one playbook, a **task**, and it ends when that run ends. A
conversation that only needs an answer stays with the **assistant**, on the conductor, and
does not start a container. That split is what you get with the host runtime on. Without it,
every turn is a task. The [Deploy](#deploy) section says which one a given install is.

The jobs are yours to define. Four that the shape is built for:

- **Investigate a bug.** A Slack thread or a Linear ticket hands a playbook the repository
  and a shell. It reads the code, runs what it needs to run, and posts the findings back on
  the thread that asked.
- **Review code.** A GitHub App you create in your own org turns a pull request you tag into
  a turn, and posts the review as that App. The same review can be asked for from Slack. It
  is one conversation either way.
- **Write code.** A playbook with write access to a repository, a toolchain, and, when the
  change has a UI, a browser. It edits, runs the tests, opens the pull request, and can
  check the result before it says it is done. The playbook that develops Podium itself is
  that job, pointed at this repository: [profile/README.md](profile/README.md).
- **Answer an analytics question.** A playbook whose image has the warehouse client, and
  whose secret list is that warehouse's credential. The question is answered in the thread.
  That credential is not mounted into any other job.

A playbook's `secrets:` list is what that job receives. The assistant picks the playbook for
the piece of work in front of it, and may start more than one in a single answer. A thread
can also name one (`/playbook`), and a Linear assignment runs the playbook that claims
Linear. [docs/agent.md](docs/agent.md) is the full account of assistant, playbook, and task.

## Where it runs

Each task is one container, on one worker, from one image, to one exit code.

- Its own private network, shared only with the sidecars that task started.
- A fresh workspace, mounted at `/workspace`, deleted when the task ends. What you meant to
  keep is an artifact in the object store.
- Every capability dropped, `no-new-privileges` set, and the Docker socket never mounted
  in. A task does not get a daemon unless the playbook asks for one, and then the daemon is
  a sidecar the operator allowed on that node.
- Secrets decrypted by the control plane, delivered onto a tmpfs at `/podium/secrets`, and
  shredded with the container. They are encrypted at rest under a master key you hold.

Workers dial out. A node opens one stream to the control plane and listens for nothing, so a
worker on another continent is the same operation as a worker on the same desk. The control
plane never touches Docker. A worker never touches Postgres or the object store.

That sandbox is the boundary around the job. It is not a boundary against the host kernel: a
`podium-node` holds the Docker socket, which is root-equivalent on that machine. Run workers
on machines that do nothing else, and read [docs/security.md](docs/security.md) before you
pick one.

## The pieces

| | |
|---|---|
| `podium-server` | The control plane. API, scheduler, node registry, secrets, log ingest, and the web UI. Needs Postgres. An S3-compatible store keeps artifacts and rolled-up logs. |
| `podium-agent` | The conductor. One assistant for the organization, the playbooks behind it, and the record of every turn. It is an API client of the server: its own database, its own token, and it never touches Docker. See [docs/agent.md](docs/agent.md). |
| `podium-node` | One per worker. Runs tasks on the local Docker engine. **Root-equivalent on its host.** |
| `podium` | The CLI. Submits and follows tasks, and exits with the task's own exit code. Talks only to the server. |
| `podium-runner` | PID 1 inside every task container. Forwards signals, reaps orphans, reports events. Embedded in `podium-node`; never installed by hand. |

The same workers also run an ordinary container task (a build, a batch, a sidecar database)
with no agent in it. `podium run` is that path. The agent layer is what turns a conversation
into those tasks and brings the result back.

---

## Deploy

> **No tagged release yet.** There is no binary to download and no image to pull until a `v*`
> tag is pushed. Until then, build the images and set `PODIUM_IMAGE_REPO` to a registry you
> can reach. The recipe is in
> [docs/quickstart.md](docs/quickstart.md#building-the-images-yourself). Read
> [docs/security.md](docs/security.md) before putting a deployment anywhere that matters.

A control plane on this machine. Workers anywhere that can already reach it: a LAN, a
WireGuard mesh, a corporate VPN. The deployment is one compose file,
[`deploy/docker-compose.host.yml`](deploy/docker-compose.host.yml), and one `.env`.

```sh
mkdir podium && cd podium
curl -fsSLo docker-compose.yml \
  https://raw.githubusercontent.com/podium-ade/podium/main/deploy/docker-compose.host.yml

docker run --rm -v "$PWD:/out" --user "$(id -u):$(id -g)" \
  ghcr.io/podium-ade/podium-server:latest \
  init --dir /out
```

`init` writes `master.key` and `.env` with fresh credentials, and never overwrites either.
Fill `PODIUM_SERVER`, this machine's address on your network, for example
`http://10.8.0.2:8080`. `0.0.0.0` is a bind address, not a URL.

```sh
docker compose up -d --wait
```

Linux Engine. On a Mac, clone the repo and `make stack-up`. The same `.env` either way.

That brings up Postgres, an object store, the control plane, the conductor, and the web UI on
8080. The Agent screen is the conductor, reverse-proxied behind the server so there is one
origin and one login. No Go toolchain and no Node on the host. It fails closed: nothing has a
default password.

**A worker** is the one thing that stays opt-in. It mounts the host's Docker socket. Without
one, a task sits in `queued` and says why.

```sh
echo "PODIUM_NODE_ENROLL_TOKEN=$(docker compose run --rm cli \
  node enroll-token --label demo)" >> .env
docker compose --profile node up -d
```

The enrollment token is single-use. A worker on another machine is the same command pointed
at `PODIUM_SERVER`. [docs/node-setup.md](docs/node-setup.md).

**A model key** is set in the UI, on purpose, so it lands in the encrypted secret store
instead of in `docker inspect`. **Shared memory** (Hindsight) comes up with the stack and
waits for a key of its own:

```sh
echo 'PODIUM_MEMORY_LLM_API_KEY=...' >> .env
docker compose up -d
```

The full walkthrough (the master key, tearing it down, building the images) is
[docs/quickstart.md](docs/quickstart.md). Tailscale, if you want the control plane on a
MagicDNS name with no shared token, is [docs/networking.md](docs/networking.md).

Two credentials the conductor does not invent, and both are how the organization reaches it:

- Slack, Linear, and the GitHub App are off until their tokens are set. Each is one group of
  variables in [deploy/.env.example](deploy/.env.example). The GitHub App is one you create
  in your own org. Podium does not ship a Marketplace listing.
- The assistant, the process that answers a conversation and delegates machine work,
  runs when the conductor is started with its host runtime. The published `podium-agent`
  image is a static binary and runs every turn as a task instead. [docs/agent.md](docs/agent.md)
  is which one you have, and what each door (chat, Slack, Linear, GitHub) does with it.

The playbook directory the image ships is a starter that holds nothing. Replace it with your
own (one `profile.yaml`, one file per playbook) and re-read it from the UI. No restart.

---

## Documentation

**Start here**

- **[docs/agent.md](docs/agent.md)**: the product: the assistant, playbooks, and how a turn runs
- **[docs/quickstart.md](docs/quickstart.md)**: a control plane, a worker, and a first task
- **[docs/concepts.md](docs/concepts.md)**: the nouns, and what a task is not
- **[docs/security.md](docs/security.md)**: the trust model. Read before a node goes anywhere real

**Using it**

- [docs/task-spec.md](docs/task-spec.md): every spec field: secrets, sidecars, readiness, limits, hardening, artifacts
- [docs/cli.md](docs/cli.md): every command, its exit codes and its streams
- [examples/](examples): a task with a sidecar, secrets, limits, artifacts
- [examples/agent/](examples/agent): the starter profile a fresh install runs
- [profile/](profile): the playbook that develops this repository

**Running it**

- [docs/operations.md](docs/operations.md): backup, restore, upgrade, drain, metrics, and what to do when something is wrong
- [docs/storage.md](docs/storage.md): Postgres, the object store, a worker's data dir, the image cache
- [docs/networking.md](docs/networking.md): how clients reach the control plane, Tailscale, the ACL
- [docs/node-setup.md](docs/node-setup.md): setting up a worker
- [deploy/README.md](deploy/README.md): compose, the installer, the systemd unit
- [deploy/.env.example](deploy/.env.example): every `PODIUM_*` variable, commented

**Internals**

- [docs/protocol.md](docs/protocol.md): the node-to-server stream, event ordering, acks, reconciliation
- [docs/runner-events.md](docs/runner-events.md): `podium-runner` as PID 1 and its event socket
- [CONTRIBUTING.md](CONTRIBUTING.md): dev setup, house rules, the things that will confuse you

---

## Configuration

Every daemon is configured entirely by environment, and **one file is the whole of it**: a
`.env` beside the compose file. [`deploy/.env.example`](deploy/.env.example) documents every
variable, and `go test ./deploy/...` fails the build if the code reads one that file does not
mention.

`podium-server init` writes the credentials. What you set beyond that falls into three tiers.
[`deploy/README.md`](deploy/README.md#what-you-have-to-configure) has the full version:

| tier | | |
|---|---|---|
| **Required to start** | minted by `init` | `PODIUM_LOCAL_TOKEN`, `PODIUM_PG_PASSWORD`, `PODIUM_S3_SECRET_KEY`, `PODIUM_AGENT_TOKEN`. Fill `PODIUM_SERVER` yourself: this machine's address on your network |
| **Unlocks a feature** | one variable each, and without it only that feature is off | `PODIUM_MEMORY_LLM_API_KEY` (shared memory), `PODIUM_NODE_ENROLL_TOKEN` (a worker's first run), `PODIUM_AGENT_SLACK_*` / `PODIUM_AGENT_LINEAR_API_KEY` / `PODIUM_AGENT_GITHUB_*` (those sources), `TS_AUTHKEY` + `PODIUM_TAILNET` (Tailscale) |
| **Just config** | ports, intervals, models, poll rates, labels, base URLs, all defaulted | `PODIUM_IMAGE_TAG` is the one to pin regardless: `latest` moves, and a control plane and worker from different releases can disagree about the wire |

The model key for a turn is not in this file. Set it in the UI so it is stored encrypted.

## Development

```sh
make build              # UI + runner-embed + all four binaries into bin/, host-native
make test               # unit tests
make test-integration   # + real Postgres and Docker via testcontainers
make e2e                # boots a full stack and drives the real CLI
make lint proto fmt
go build -tags noui ./...   # skip the embedded UI, no Node required
```

> **Stop any running `podium-node` before `make test-integration` or `make e2e`.** Both suites
> start real nodes against the host's Docker engine, and a node claims containers by the
> `podium.task` label alone, with no node scoping. Each side reports the other's containers to its
> own control plane, which has never heard of them, and tears them down. You lose the test run
> and whatever the live node was running, and it looks like flakiness or memory pressure. It is
> not. (`DOCKER_HOST` or `PODIUM_NODE_DOCKER_HOST` pointed at a second engine separates them too,
> if you have one.)

See [CONTRIBUTING.md](CONTRIBUTING.md).

---

## Security

Read [docs/security.md](docs/security.md) before deciding which machines run a node. The short
version: **a `podium-node` is root-equivalent on its host**, and **a task container is
untrusted**. Report a vulnerability privately. See [SECURITY.md](SECURITY.md).

## License

[MIT](LICENSE).
