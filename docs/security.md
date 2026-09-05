# Security

What Podium protects, what it does not, and where the boundaries actually are. Read the trust
model before you decide which machines run a node.

**Podium is pre-alpha and has never had a security review.** Nothing here has been tested by
anyone trying to break it. Treat the whole system as inside your perimeter, not as part of it.

---

## Trust model

Four things, in decreasing order of trust.

### 1. The control plane — trusted

`podium-server` holds the database, the secrets master key and the object store credentials.
Everything that can compromise a Podium deployment starts here. Run it on a machine you
administer, back up its Postgres and its master key, and give nothing else a login on it.

### 2. The node daemon — **root-equivalent on its host**

`podium-node` drives `/var/run/docker.sock`. Anything that can talk to that socket can start a
container with `--privileged` and `-v /:/host` and own the machine. That is true of the daemon,
and it is true of anyone who can make the daemon start a container.

Three consequences, and they are not negotiable:

- **Running the daemon as a non-root user in the `docker` group buys nothing.** It is the same
  power with a longer name. The shipped systemd unit runs as root and says so.
- **A node host is root-equivalent to whoever can submit tasks.** There is no RBAC (below), so
  in practice: everyone who can reach the API can run arbitrary code as root on every worker.
- **Run workers on machines that do nothing else.** A node is a machine you are willing to let
  arbitrary containers run on. Do not put one on a machine that also holds production data, a
  CI signing key, or somebody's laptop session.

The systemd unit's hardening (`ProtectSystem=strict`, `NoNewPrivileges`, and the rest) protects
the host from the daemon's *mistakes*. It does not protect the host from the daemon, and it
cannot: the socket is the whole job.

### 3. A task container — **untrusted**

A task is somebody else's code. Podium sandboxes it:

| | |
|---|---|
| Capabilities | every one dropped (`CapDrop: ALL`); `hardening.capabilities` adds back from a seven-entry allow-list |
| Privilege escalation | `no-new-privileges:true`, always |
| Seccomp | the engine's default profile. `unconfined` is deliberately not a spec field |
| Docker socket | never mounted into a task. Asserted by a test |
| `--privileged` | never set. Asserted by a test |
| Root filesystem | read-only on request (`hardening.read_only_rootfs`), with a 1 GB tmpfs on `/tmp` so images still work |
| Memory | `memory_mb`, with swap pinned to the same number, so exceeding it is an OOM kill and not a swapped-out machine |
| PIDs | `resources.pids`, default 4096 |
| Network | its own bridge network per task, shared only with its own sidecars |
| Groups | gid 0 as a supplementary group, so a non-root task image can still open the runner event socket — see below |

What that does **not** buy you:

- **The container boundary is not a security boundary.** A kernel exploit from inside a
  container is a compromise of the node, and the node is root-equivalent on its host.
- **There is no egress policy.** A task reaches its own sidecars by name, and it reaches the
  internet. Whether it can also reach the *host's* other networks — including a tailnet, or a
  database on the host's LAN — depends on the host's routing and firewall, and Docker's default
  bridge setup forwards it. **Assume it can.** This has never been tested and nothing in Podium
  restricts it. If a worker sits on a network that matters, firewall it at the host.
- **The host is now reachable by name from every task.** Every task container is created with
  `host.docker.internal` mapped to the engine's bridge gateway, because a turn has to reach the
  agents' shared memory on the control-plane host. It was already reachable *by IP* — the
  bullet above — so this is not new access; it is new convenience, and it applies to every
  task, not only an agent one. Sidecars deliberately do not get the entry.
- **Sidecars are not hardened like the task container.** They keep their capabilities and their
  writable root filesystem, because a stock database image chowns files and drops privileges on
  the way up and breaks under `CapDrop: ALL`. A sidecar image is as trusted as the task.
- **The runner event socket is reachable by every process in the container**, so that a task
  image running as a non-root user can report at all. The node sets the socket to mode 0666 on
  the host, which is what a native Linux engine carries into the container. Docker Desktop does
  not: it forwards a bind-mounted Unix socket through a proxy and presents it inside the
  container as `root:root` mode 0660 whatever the host mode is, which locks out every non-root
  image — so the task container also gets **gid 0 as a supplementary group**. That is the same
  remedy people use for `/var/run/docker.sock`. With every capability dropped and
  `no-new-privileges` set it buys nothing else than the group bit on root-group files, in an
  image the task chose anyway; it is still a widening, and it is listed here because it applies
  to every task, not only to an agent one.
  Either way, any process in the task container can forge `step`, `artifact` and `message`
  events. Today that only produces cosmetic log entries and an artifact upload the task could
  have made anyway; it becomes a real problem the moment those events drive server-side state.
- **A `message` event's text is untrusted content, and it is not redacted.** A task can forge a
  `message` of any type, with any text, naming any attachment. None of it drives server state —
  no status transition, no storage beyond the `task_events` row — but the text is written by an
  untrusted process, and secret redaction does not apply to it: the node's redactor is a
  log-chunk pipeline and never sees a message payload. **Every relay that posts a message
  somewhere — Slack, Linear, a web chat — must treat the text as untrusted content from an
  untrusted process, exactly as it would treat a log line.** Relay it; never interpret it, never
  execute it, never let it name the channel it is posted to.

### 4. Anyone who can reach the API — **fully trusted, because there is no RBAC**

There are no roles, no per-user permissions and no read-only mode. Under the tailnet transport
the server records who visited in a `users` table and then lets them do everything. Whoever can
reach the API can submit tasks (and therefore run code as root on every worker), drain nodes,
delete secrets and delete nodes.

### 5. The conductor and the bot

`podium-agent` (see [`agent.md`](agent.md)) widens exposure and fixes nothing about the above.

- **Whoever can tag the bot, or assign it a ticket, can run code on a worker with that skill's
  credentials.** Anybody in a channel the bot is in — including a channel somebody else invites
  it to — and anybody who can set the assignee on a Linear issue can start a turn. There is no
  allowlist of users and no roles. A turn gets exactly the secrets its own skill names, plus the
  reserved `podium.agent.anthropic_api_key` and `podium.agent.memory_api_key` the conductor
  attaches itself. Keep `secrets:` minimal per skill and do not put a credential in a skill a
  public channel can reach.
- **A skill is not a boundary around secrets, and never was.** It decides what *this bot* hands
  a turn, and that is worth keeping tight — but it stops nobody. `CreateTask` checks only that a
  named secret **exists**; there is no authorisation over which secrets a caller may name. So
  anyone who can reach the control plane can already submit a task that mounts any registered
  secret into an image and a command of their choosing, and print the value. That is section 4
  again: **no RBAC**. It is why a skill defined in the web UI may name any registered secret,
  exactly as a task spec may — restricting one path while the other is wide open would be
  theatre, not a control. The only names a skill may not use are the two reserved ones above,
  and that is a routing rule, not a privilege: the conductor supplies both itself.
  **The control is who can reach the API at all.** Put the control plane on a tailnet, keep the
  set of people who can reach it small, and treat every registered secret as readable by every
  one of them.
- **The `coder` skill has write access to your repositories, and a prompt injection can steer
  it.** It is the one skill that names `podium.agent.github_token`, so it is the one skill whose
  turns can push a branch and open a pull request. Everything a turn reads is untrusted: a
  ticket's description, a comment on it, a Slack message, and **a README, a `CONTRIBUTING.md` or
  a comment in the repository it just cloned**. Any of them can carry instructions, and the agent
  has no way to tell them from the request. The mitigations reduce this and do not remove it:
  - the pull request is opened as a **draft**, so no reviewer is paged and no automation merges
    it, and a human reads the diff before anything happens;
  - the prompt forbids committing to, pushing to, rebasing onto or force-pushing the default
    branch — but a prompt is guidance, not a control, so put **branch protection** on the default
    branch of every repository in `repos:` and require a review;
  - the token should be a **fine-grained** PAT scoped to exactly those repositories, with
    *Contents* and *Pull requests* read/write and nothing else — never a classic `repo` token,
    which is every repository its owner can see, and never `workflow`;
  - **do not point it at a repository that deploys on merge**, or at one whose CI runs on a
    branch push with credentials of its own. A draft PR is not a boundary if pushing the branch
    is already enough to run something.

  Said plainly: an attacker who can write a Linear comment on a ticket the bot is assigned, or
  post in a channel the bot is in, can attempt to make it commit code. Everything above makes
  that attempt visible and slow. None of it makes it impossible.
- **The Linear API key is as sensitive as `PODIUM_DEV_TOKEN`.** It is a *personal* API key on a
  user seat: full read and write of every issue, comment, project and document that user can
  see. Give the bot user access to only what it needs, the way you would a contractor. Podium
  never logs it and sends it in one header to one endpoint (`PODIUM_AGENT_LINEAR_URL`), and it
  is **never** injected into a task container — a turn cannot read the bot's Linear account.
- **The Slack tokens are as sensitive as `PODIUM_DEV_TOKEN`.** The `xoxb-` bot token can read and
  post in every channel the bot is in; the `xapp-` app-level token opens the event connection.
  `PODIUM_AGENT_TOKEN` is the only thing guarding the conductor's API, which lists every session
  and every answer the bot has given.
- **Text relayed out of a task is untrusted content.** The conductor posts a `final` message
  verbatim and interprets none of it: it never parses an answer for a command, a channel name or
  a user ID, and it cannot be talked into posting somewhere else. The reverse is also true and
  more dangerous: a turn's *instruction* is whatever a human typed, so anybody who can tag the
  bot can prompt-inject the agent inside its own container. The container is the boundary.
- **A `message` event is never redacted.** The log redactor is a log-chunk pipeline and never
  sees a message payload, so a task that puts a secret in its answer puts it in the database, in
  the web UI and in the Slack thread. See *Redaction* below.
- **The agent runtime runs with `bypassPermissions`.** No tool call inside a turn asks anybody
  anything. That is deliberate — a turn is not interactive and there is nobody to ask — and it
  means the sandbox in *3. A task container* is the whole of the protection.
- **The conductor holds no privilege of the control plane's.** It has its own database, its own
  API token, and no master key, no Docker socket and no node key. Compromising it gets an
  attacker the bot's Slack tokens and the ability to submit tasks — which is already everything,
  because there is no RBAC.
- **The provider key is a secret like any other, and the web UI can replace or remove it.**
  Whoever can reach the UI can paste a new Anthropic key over the current one, or remove it and
  stop every turn. There is no confirmation beyond an inline one and no audit of who did it
  beyond `set_by`, which records the login at the time of the last successful save and is
  overwritten by the next. Only the last four characters of the key are ever stored outside the
  secret store, and there is no read endpoint: `podium secret rm
  podium.agent.anthropic_api_key` is the CLI equivalent of the UI's Remove.
- **Shared memory is a prompt-injection amplifier.** Every turn reads and writes one memory
  bank, so a fact planted by one poisoned turn — out of a Slack message, a ticket, a README in a
  cloned repository — is recalled by every later turn, including turns for other people in other
  channels. Nothing in this track detects that. The mitigations are all human or advisory:
  provenance on every item (`session_id`, `turn_id`, `task_id`, `source_ref`, `source_url`), the
  Memory tab where a person can read what the organisation "knows" and forget an item, and the
  runtime prompt's rules — which a determined injection will talk past. A turn's own answer is
  what gets retained, so **an agent that can be talked into saying something can be talked into
  remembering it**.
- **The memory API key is full read/write of every memory.** One value,
  `PODIUM_AGENT_MEMORY_API_KEY`, guards the whole service, and there is nothing finer: no
  per-agent key, no read-only key, no per-bank key. It reaches three places — the memory
  container, the conductor, and **every task container**, as the Podium secret
  `podium.agent.memory_api_key`. So any turn can rewrite or wipe the whole bank, whatever its
  skill file says. As sensitive as `PODIUM_DEV_TOKEN`.
- **The memory service has NO authentication of its own by default.** It is switched on by
  `HINDSIGHT_API_TENANT_EXTENSION` + `HINDSIGHT_API_TENANT_API_KEY`, which
  `deploy/docker-compose.yml` makes mandatory. Run that image without them — by hand, or in
  somebody else's compose file — and port 8888 is an open read/write endpoint over everything
  the organisation has learned.
- **Port 8888 is published on all interfaces, and it has to be.** A turn's container reaches the
  host through the Docker bridge gateway, and a service bound to `127.0.0.1` is not reachable
  from there. `PODIUM_MEMORY_BIND` narrows it to a tailnet or bridge-gateway IP; otherwise
  firewall 8888 at the host to those ranges. See
  [`networking.md`](networking.md#reaching-the-shared-memory-from-a-worker).
- **The memory engine's own web UI is deliberately not published.** Its port 9999 control plane
  would be a second, unauthenticated front door onto the same data; `HINDSIGHT_ENABLE_CP=false`
  turns it off and no compose file maps the port. An operator who needs it can port-forward.
- **Forgetting a memory is a tombstone, not a deletion.** `DeleteMemory` sets the curation state
  to `invalidated`: the memory is excluded from every recall, from consolidation and from the
  graph, and the row is kept in an archive. No future turn sees it — which is what the operator
  asked for — but the text is still in `podium_memory`, and it is reversible through the memory
  engine's own API. If a memory must be *destroyed*, that is a database operation, not a UI one.
- **The memory service gets its own Anthropic key**, `PODIUM_MEMORY_LLM_API_KEY`, as a container
  environment variable — so it is visible in `docker inspect` and in `/proc` on the host, like
  any compose environment value. It is not stored in Podium's encrypted secret store, because it
  is read before anything Podium controls is running. Three things reduce what that costs
  you, none of which removes the exposure:
  - **Give it its own key, in its own workspace, with a spend limit.** It does one job —
    extracting facts from prose your own agents produced — so it never needs the agents' key.
    A separate key bounds the blast radius and makes rotation a non-event.
  - **Mount it rather than passing `-e`.** The service calls `load_dotenv(find_dotenv(usecwd=True))`
    at start-up and its working directory is `/app`, so a read-only bind of a mode-0600 file at
    `/app/.env` keeps the key out of `docker inspect`, out of shell history and out of any
    compose file that gets committed. It is still in the process environment inside the
    container: this shrinks the exposure, it does not end it.
  - **Or give it no key.** Reads need none — search and reranking run on models baked into the
    image — so a bank you only ever query works unauthenticated to Anthropic. Extraction is the
    only thing that calls a model, and the engine also supports local providers. Both routes cost
    extraction quality and neither has been tested here.
  Podium deliberately does **not** inject this key itself. Its secret injection is per task, and
  the conductor never sees the master key; templating a secret into a service Podium does not
  manage would breach that boundary to protect a narrower credential than the agents' own.
- **`X-Podium-Login` is trusted because the bearer proves where it came from.** `podium-server`
  reverse-proxies `/podium.agent.v1.AgentService/` to `PODIUM_AGENT_URL` behind its own identity
  middleware. On the way it **deletes** any client-supplied `Authorization` and
  `X-Podium-Login` and sets its own: the conductor's bearer, and the login of the caller the
  server authenticated. The conductor accepts the header only because the bearer is known to
  exactly one party — the server — and that party is the one that named the human. A node
  identity is refused with 403 before anything is forwarded, and the conductor's own
  `/healthz`, `/readyz` and `/metrics` are not proxied at all.

  **It is a plain header, not a signed assertion.** That is sound while the conductor listens on
  loopback or a compose network that only the server can reach, which is why
  `PODIUM_AGENT_LISTEN` defaults to `127.0.0.1:8090`. Expose that listener any wider and
  anything able to reach it that also learns `PODIUM_AGENT_TOKEN` can claim to be any operator;
  at that point the header has to become a signed assertion, and this document is the record
  that it is not one yet.

### The analyst and your warehouse

The `analyst` skill (see [`agent.md`](agent.md#the-analyst-skill)) is the third widening in this
track, and it is the one that touches data nobody wrote for a bot.

- **It reads everything its credential can read.** There is no table allowlist, no column
  masking and no row filter anywhere in Podium. Whatever `podium.agent.warehouse_url` or
  `podium.agent.warehouse_credentials` can select, a turn can select — and any question from
  anybody who can reach the chat, or the channel, can be the one that selects it. **Use a
  read-only role with a statement timeout**, and scope it to the schemas an analyst may see. The
  recipe is in [`agent.md`](agent.md#the-read-only-role-is-the-control-not-the-prompt).
- **The read-only role is the control. The prompt is not.** `prompts/analyst.md` tells the agent
  never to modify data, and says in as many words that the credential is the real control and
  the instruction is a courtesy. A prompt injection in a question, in a Slack thread, or in a
  memory a previous turn retained can talk past a prompt; it cannot talk past
  `default_transaction_read_only`.
- **Row-level data can end up in a chat transcript, in Hindsight memory, and (for the same skill
  via Slack) in a Slack channel.** Those are the words, and they mean exactly what they say. An
  answer is stored in `chat_messages` in `podium_agent` for ever; the same skill asked over Slack
  posts its answer into the channel, where everybody in it can read it and Slack keeps it under
  your workspace's retention; and the end-of-turn retain puts the answer into the shared memory
  bank, which every later turn of every skill reads. **A customer's name, email address or
  balance that reaches an answer has left the warehouse's access controls behind and is now
  governed by Slack's, by `podium_agent`'s and by `podium_memory`'s** — three places with no
  RBAC, no retention and no per-user scoping. If your warehouse holds personal data, that is the
  sentence to take to whoever owns your data-protection obligations before you point this skill
  at it.
- The prompt's "keep result tables to 50 rows, and never retain row-level data" rules exist for
  exactly that reason, and they are **a courtesy, not a control** — the same distinction as
  above. Nothing in Podium inspects an answer for personal data, and a `message` event is never
  redacted (see *Redaction* below). The only real controls are the credential's own grants and
  which channels the skill is reachable from.
- **The web UI's CSP allows `blob:` images, and that is new in this step.** A chat
  attachment cannot be an `<img src="/artifacts/{id}">`: that route is behind the identity
  middleware and an `<img>` carries no Authorization header. So the page fetches the bytes
  itself — a request `connect-src 'self'` already allowed — and displays them from a blob
  URL, which needs `img-src … blob:`. It widens nothing about what the page can *reach*: a
  blob's bytes came from a request this policy already permitted, and `blob:` in `img-src`
  cannot name a remote host. What it does mean is that **bytes a task produced are decoded
  by the browser**, so an image decoder bug is reachable from a turn's output. The
  compensating decision is in `ChatAttachments.tsx`: only raster types are ever rendered or
  handed to a tab, and `image/svg+xml` never is — an SVG is a scriptable document and a
  `blob:` URL inherits the app's origin, which is where the dev token lives.
- **A chat belongs to a login, and that is a partition rather than a permission.**
  `ListChats`, `SendChatMessage` and `StreamChat` refuse another login's chat with `not_found`,
  and the login is the one `podium-server` asserted. But every login is fully trusted — there is
  still no RBAC — so this stops an accident and one honest mistake, not an operator who wants to
  read somebody else's conversation: whoever can reach the API can read `podium_agent` directly,
  and the answers are also in `turns.final_text` with no login on them at all.

---

## Transports, and what crosses the wire

### `dev` — loopback, shared bearer token

- The listen address **must resolve to loopback**; the server refuses to start otherwise. That
  check is what makes the rest of this acceptable.
- Every RPC carries `Authorization: Bearer <PODIUM_DEV_TOKEN>`, compared in constant time.
  There is one token for everything and everyone. It has no identity: audit rows say `dev`.
- **The connection is unencrypted HTTP.** Everything crosses it in the clear, and that includes
  **resolved secret values**, which travel inside `Assign` from the server to the node. There is
  no TLS and no per-node key on the HTTP layer.
- The server logs a warning at startup when the dev transport is in use and any secret exists,
  for exactly that reason.
- The web UI keeps the token in `localStorage`.

Loopback is doing all the work. Do not publish a dev-transport port to anything but
`127.0.0.1`, and do not use the dev transport across a network under any circumstances.

### `tailnet` — the one to use for real workers

- The server embeds its own Tailscale device (tsnet) and serves HTTPS on its MagicDNS name.
  Traffic is WireGuard end to end; the certificate comes from Tailscale.
- **There is no token and no login page.** Identity comes from the connection: Tailscale's
  `WhoIs` names the caller, and the ACL decides who can open a connection at all.
- **There is no public ingress.** The server listens on port 443 of its own tailnet device.
  Workers dial out and listen for nothing.
- A device with `tag:podium-node` is a node. A device with `tag:podium-server` is refused (403)
  — control planes do not call control planes. Any *other* tag is also refused, because
  Tailscale reports the tag owner's profile for a tagged device and honouring it would let any
  tagged machine act as whoever created its tag. An untagged device is a user.
- `PODIUM_TS_ALLOW_UNTAGGED_NODES=true` removes the network-level proof that a caller is an
  authorised worker. It exists for a tailnet with no ACL tags yet. The server warns loudly.

**Unverified.** The tailnet transport has never been run against a real tailnet — that needs
tagged auth keys and HTTPS enabled, neither of which the build machine had. Everything above is
what the code does; none of it has been observed in the wild. `host` mode is likewise
implemented and never run.

### Ports

| Port | Who | Authentication |
|---|---|---|
| `127.0.0.1:8080` | server, `dev` transport | bearer token, except `/healthz`, `/readyz`, `/metrics` |
| `:443` on the server's tailnet device | server, `tailnet` transport | Tailscale identity |
| `:80` on the server's tailnet device | redirect to 443 | none |
| `127.0.0.1:9091` | node | **none.** `/healthz`, `/readyz`, `/metrics` |

`/healthz`, `/readyz` and `/metrics` are open on both daemons. They leak liveness, a Postgres
reachability bit, and Go runtime and process metrics — no task content, no identities, no
secret names. Keep them on loopback anyway; the node's default already is.

---

## Node identity

Two credentials, and people confuse them constantly. See
[`networking.md`](networking.md#the-two-keys-which-are-not-the-same-thing).

**Enrollment token** — Podium's own. 32 random bytes as URL-safe base64, **single use**, 1 hour
default TTL, only its SHA-256 reaches the database, rate-limited to 5 attempts per minute per
source IP *before* the token is looked at. Minted with `podium node enroll-token`.

**Node key** — what `Enroll` returns. 32 random bytes, **returned exactly once**; the database
holds only `sha256(key)`. It lands in `<data_dir>/identity.json`, mode 0600. There is no
recovery path and no reissue: a lost key means a fresh enrollment token.

`identity.json` is a bearer credential — whoever copies the file owns the node. Under the
tailnet transport that is backstopped by a **device binding**: `Enroll` records the Tailscale
`StableID` on the node row, and every later `Hello` is checked against it. A different device
presenting the same key is refused with a message naming `podium node rekey`. Do not rely on
the binding as your only control; protect the file.

`podium node rm` forgets a node but **does not stop its daemon**. A removed node whose
`identity.json` survives reconnects forever and is told its key is unknown, once per backoff.
There is no revocation push. Stop the daemon yourself.

---

## Secrets

### At rest

One 32-byte AES-256 key encrypts every stored secret. `podium-server gen-master-key` mints it.

- **The file's mode is enforced.** Anything a group or another account can read (`perm&0o077`)
  is refused, before the store is even opened, so the process exits with an actionable message
  and never touches Postgres. `0600` and `0400` pass.
- `PODIUM_MASTER_KEY` takes the key inline for development. An environment variable is visible
  in `/proc` and in `docker inspect`; the server warns loudly. The file wins if both are set.
- **No key is a legitimate configuration.** Every secret call answers `FailedPrecondition` and a
  task naming a secret is failed at admission with a message saying which name. Podium is still
  a task runner without secrets.
- `key_id` is `hex(sha256(key)[:8])` and is stored on every row, so a half-finished rotation is
  visible in `podium secret ls`.
- **Loss is final.** There is no escrow, no second key and no recovery path. `gen-master-key
  --out` uses `O_EXCL` so it can never silently overwrite a live key. **Back the file up
  somewhere that is not the control plane.**
- Rotation is offline and atomic: `podium-server rotate-master-key --old FILE --new FILE` takes
  `select ... for update` over the whole table in one transaction, so it is never half under one
  key. Afterwards the old key decrypts nothing.

**There is no read endpoint and there must never be one.** `SecretService` is
`SetSecret`/`ListSecrets`/`DeleteSecret`. `ListSecrets` returns names, versions and key ids —
no value, no ciphertext, in the message at all. A "reveal" button is not a feature that could be
added later without changing the threat model.

### In flight

```
podium secret set NAME  ──►  server: AES-256-GCM under the master key  ──►  Postgres
                                                │
task is dispatched to a node                    │  resolved once per dispatch,
                                                ▼  values in server memory for one call
                                    Assign{resolved_secrets: [{name, target, key, value}]}
                                                │
                                                ▼  ** plaintext on the wire **
                                         podium-node
                                                │
                        ┌───────────────────────┴──────────────────────┐
                        ▼                                              ▼
        target: env  →  container environment              target: file  →  0400 file on a
        (after the spec's own env:, so a                    tmpfs at /podium/secrets,
        secret wins over a plaintext entry                  bind-mounted read-only at the
        of the same name)                                   path the ref names
```

- **Under the `dev` transport that `Assign` crosses an unencrypted loopback socket.** Under
  `tailnet` it is inside WireGuard.
- A value is in server memory for the length of one dispatch and is zeroed afterwards, on both
  the resolver's slice and the node's. Go may have copied it during the proto marshal; nothing
  short of a locked-memory allocator fixes that.
- An `env` value has to become a Go `string` to reach the Docker API, which takes `[]string`.
  Every other path keeps it as `[]byte` and zeroes it.
- File secrets are shredded at teardown: chmod writable, overwritten with zeroes, `fsync`ed,
  unlinked, directory removed.
- `Assign` is re-resolved on every dispatch, so a node that reconnects and is re-assigned gets a
  fresh resolve — and a `secret.resolve` audit row exists per dispatch, not per task.
- **A sidecar cannot reference a secret.** A sidecar that needs a credential takes it from a
  plaintext `env:` entry. Known gap.

### Redaction — what it does and does not guarantee

Every log chunk leaving a node is passed through a redactor built from the values of that task's
own resolved secrets. A match is replaced with `[redacted:NAME]`. Because it happens on the
node, **the value never crosses the wire**, so `podium logs`, the web UI and the archived log
all show the same redacted text. There is one log path.

It matches:

- any secret value of **8 bytes or more** (shorter values are too collision-prone to be worth
  the false positives),
- and that value re-encoded as standard base64, raw standard base64, URL base64, raw URL
  base64, `url.QueryEscape` and `url.PathEscape`,
- longest pattern first, across a chunk boundary (a partial trailing match is held back rather
  than emitted).

**It is string matching, and string matching is not a guarantee.** It does not catch:

- a value the task **transforms** — hex, gzip, a hash, a fragment, a different encoding;
- a value **split across two writes** by something other than the coalescer, or interleaved
  with other output within one write;
- a value **shorter than 8 bytes**;
- anything the task sends somewhere that is not its stdout or stderr — a network call, an
  artifact, a file it writes;
- anything at all after a **node restart**. An adopted task has no redactor: the values were in
  the previous incarnation's memory. The container keeps running; only the filter is gone. The
  adopted run announces itself with a `step{name: "node/reattached"}` event, which is the seam.

Treat redaction as defence in depth against an accidental `echo $PASSWORD`, never as the
control that keeps a secret out of a log. **The control is not printing it.**

---

## Artifacts and the object store

- **Nodes never talk to the object store.** An artifact travels node → server → S3 over
  `NodeService.UploadArtifact`. A presigned PUT from the node was deliberately not taken: it
  would need every worker to hold a route and a credential to the store, which is the invariant
  the networking design exists to avoid.
- `UploadArtifact` is its own HTTP request, not part of the node stream, so it re-presents
  `node_id` + `node_key` exactly as `Hello` does. The server also checks the task is on that
  node.
- **512 MB per artifact**, enforced twice: the node refuses an oversized one from the tar header
  before it crosses the wire, and the server enforces the real size on the way past and removes
  the object if a producer lied.
- Object keys are sanitised to `[A-Za-z0-9._-]`, at most 128 characters, leading dot trimmed —
  `../../etc/passwd` becomes `etc_passwd`. The original name is kept in the database and is what
  the UI shows.
- `GetArtifactURL` mints a **presigned GET good for 15 minutes**. Anyone holding that URL can
  read the object without any Podium credential, which is the point and also the risk: do not
  render one into a page that outlives it, and do not paste one anywhere durable. The download
  button in the UI and `podium artifact get` both proxy through the server instead.
- **Nothing ever deletes an artifact.** There is no retention policy and no lifecycle rule, so a
  bucket grows without bound and a task's output outlives the task indefinitely.

---

## Reporting a vulnerability

See [`SECURITY.md`](../SECURITY.md) at the repository root. Do not open a public issue.

---

## Known gaps, in one list

Everything below is a real hole, not a hypothetical:

- **No RBAC.** Anyone who can reach the API can do everything, including running code as root on
  every worker.
- **No egress policy for tasks.** Whether a task can reach the host's other networks is up to
  the host, untested, and probably yes.
- **The dev transport is plaintext**, secret values included.
- **The tailnet transport has never been run against a real tailnet.**
- **Redaction does not survive a node restart** and is best-effort at the best of times.
- **Every process in the task container can reach the runner event socket** — mode 0666 on the
  host, plus gid 0 as a supplementary group on the container so a non-root image can open it at
  all — so a task can forge `step`, `artifact` and `message` events.
- **A sidecar cannot use a secret**, so credentials for one end up in plaintext `env:`.
- **`podium node rm` does not revoke anything** — it forgets a node whose daemon keeps dialling.
- **`/metrics` and `/healthz` are unauthenticated** on all three daemons.
- **Anyone who can tag the bot, or assign it a Linear ticket, can run code on a worker.** The
  conductor has no allowlist and no roles. See *5. The conductor and the bot*.
- **Anyone who can reach the control plane can run code with any registered secret**, through a
  task spec or through a skill: `CreateTask` checks that a named secret exists and never that
  the caller may have it. A skill's `secrets:` list scopes what one bot hands one turn; it is
  not a boundary around the secret store.
- **The `coder` skill can push branches and open pull requests, and a prompt injection in a
  ticket, a comment or a cloned repository's own files can steer it.** Draft PRs, branch
  protection and a fine-grained token reduce this; nothing here removes it.
- **A task's `message` events are never redacted**, so an agent's answer can carry a secret into
  a Slack thread, a web-chat transcript and the shared memory.
- **The `analyst` skill reads everything its warehouse credential can read**, and row-level data
  in an answer lands in a chat transcript, in Hindsight memory and (over Slack) in a channel. A
  read-only role with a statement timeout is the only real control; the prompt's rules are a
  courtesy. See *The analyst and your warehouse*.
- **A web chat is partitioned by login, not protected by it.** Another login's chat answers
  `not_found`, and anybody who can reach the API can read the same rows out of `podium_agent`.
- **The Anthropic key can be replaced or removed by anyone who can reach the web UI**, and the
  only record of who did it is `set_by` on the current key.
- **`X-Podium-Login` is a plain header.** The conductor trusts it because `PODIUM_AGENT_TOKEN`
  proves the request came through `podium-server`. That holds only while the conductor's
  listener is loopback or a network only the server can reach.
- **Shared memory is a prompt-injection amplifier**, one API key guards all of it, and every
  task container holds that key. A false fact planted by one turn is read by every later turn;
  the only defence is a human reading the Memory tab.
- **The host is resolvable by name (`host.docker.internal`) from every task container**, to let
  a turn reach the shared memory. It was reachable by IP before.
- **`podium_memory` has no retention and no pruning.** Facts accumulate for the life of the
  install, and forgetting one archives it rather than deleting it.
- **No audit for reads.** `audit_log` records secret set/delete/resolve/rotate. It does not
  record who listed nodes, read a task's log, or downloaded an artifact.
- **Single server process.** Sessions are in memory; a second replica would see every node as
  sessionless and start expiring leases. This is an availability problem, not a confidentiality
  one, but it is the reason there is no HA story.
- **No security review, no fuzzing, no dependency scanning beyond the SBOM the release
  produces.**
