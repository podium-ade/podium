---
name: update-live-stack
description: Get a stack that is already running onto the latest code — rebuild only what changed, push runtime images the workers can actually pull, and restart. Use when asked to ship main to the live stack, restart with the latest changes, roll a fix out to a running stack, or rebuild the agent runtime images. For standing a stack up from nothing, see run-dev-stack.
---

# Updating a running stack

A Podium stack is three host binaries (`podium-server`, `podium-agent`, `podium-node`), zero or
more remote workers, and — if it runs an agent — the runtime images turns execute in. They are
four separate artifacts on four separate clocks. Updating the stack means working out which of
them your change is actually in, and leaving the rest alone.

**Start here.** Nothing below is worth doing for a change it does not apply to:

| What you changed | Rebuild | Then |
|---|---|---|
| Go under `cmd/` or `internal/` | `make build` | restart the affected services |
| `web/` | `make build` — the UI is embedded in the server binary | restart `podium-server` |
| `agent/runtime/src/` or either runtime Dockerfile | the images (§3) | retag the profile (§4), restart `podium-agent` |
| a playbook or profile file in the profile directory | nothing | restart `podium-agent` (§4 says why) |
| `deploy/run-host.sh`, `deploy/.env` | nothing | it is read on the next `stack-up` |

A remote worker only needs touching when Go changed — see §6.

---

## 1. Take the code, and name the build

```sh
git checkout main && git pull --ff-only
V=$(git rev-parse --short HEAD)          # every artifact below is stamped with this
```

Working from a branch is fine and normal — a fix can be proven on the live stack before it
merges. `V` is then that branch's commit, and the images carry a tag nothing else will reuse.

## 2. The binaries

```sh
make build
./bin/podium-server --version            # must print $V
```

`make build` runs the web build and embeds it, so a UI change needs nothing else. The launcher
starts whatever `PODIUM_BIN_DIR` in `deploy/.env` points at — check that it is this checkout
before wondering why a rebuild changed nothing.

## 3. The runtime images

Only when `agent/runtime/` changed. Two facts decide how to build them:

- **The runtime's `dist/` lives in the base image, and `-dev` is `FROM` it.** A change to
  `src/` means rebuilding both, base first.
- **`make agent-runtime` builds for this machine only, into this machine's image store.** That
  is enough when every worker is this host. It is useless the moment one is not: another
  machine cannot see a local tag, and a linux/amd64 worker cannot run a linux/arm64 image.

For any stack with a worker elsewhere, build multi-arch and push to the registry the profile
already names:

```sh
. deploy/.env
PROFILE=${PODIUM_AGENT_PROFILE_DIR:?set it in deploy/.env}
REG=$(sed -n 's|^image: \([^/]*\)/.*|\1|p' "$PROFILE"/playbooks/*.yaml | sort -u)   # one line, or fix the profile

docker buildx build --builder podiumx --platform linux/amd64,linux/arm64 \
  --build-arg VERSION=$V --build-arg REVISION=$V \
  -t $REG/podium-agent-runtime:$V --push \
  -f agent/runtime/Dockerfile agent/runtime

docker buildx build --builder podiumx --platform linux/amd64,linux/arm64 \
  --build-arg VERSION=$V --build-arg REVISION=$V \
  --build-arg RUNTIME_IMAGE=$REG/podium-agent-runtime:$V \
  -t $REG/podium-agent-runtime-dev:$V --push \
  -f agent/runtime/Dockerfile.dev agent/runtime
```

`--builder podiumx` is any builder on the `docker-container` driver; the default `docker` driver
cannot do more than one platform. Make one once with
`docker buildx create --name podiumx --driver docker-container`.

Check what landed before moving on — a push that half-failed looks like a stale image later:

```sh
curl -s http://$REG/v2/podium-agent-runtime/tags/list
```

## 4. Point the profile at the new tag

```sh
sed -i '' "s|^image: .*/podium-agent-runtime:.*|image: $REG/podium-agent-runtime:$V|" \
  "$PROFILE"/playbooks/*.yaml          # GNU sed: -i without the ''
```

Then repeat for `podium-agent-runtime-dev` if any playbook uses it. Playbooks that name an image of
their own, built `FROM podium-agent-runtime`, are yours to rebuild on the same tag.

**A running conductor will not see this.** `profiles.Load` reads the profile directory once, at
boot; the periodic reload rebuilds the profile from that snapshot plus the playbooks the database
holds, so a file edit reaches nothing until the process restarts. Playbooks created in the web UI
are the exception — those live in the database and do reach the next turn.

## 5. Restart

```sh
make stack-down && make stack-up
```

`down` waits for each process to actually exit before returning, so the two compose safely.
`up` waits for each service to answer its health check.

A `curl: (28)` line during startup is the first health probe timing out while the server's tsnet
device warms up. The retry that follows is the one that counts; the line to believe is
`server ready`.

To restart one service, name it: `make stack-up S=agent`. That is the whole of shipping a
profile change or a conductor fix.

## 6. Remote workers

Only when Go changed. Cross-compile, keep the binary it replaces, and restart it the way that
machine starts it:

```sh
make dist-node GOOS=linux GOARCH=amd64          # uname -m on the worker: x86_64 -> amd64
shasum -a 256 bin/podium-node-linux-amd64
scp bin/podium-node-linux-amd64 worker:podium/podium-node.new
```

On the worker: check the checksum matches, stop the old process, `mv` the current binary aside
as `podium-node.<old version>.bak`, move `.new` into place, and start it again. Its labels
survive — they are sent at enrollment and stored server-side, so a binary swap cannot lose them.

Match on a pattern that cannot match your own command when checking whether it stopped:
`pgrep -x podium-node`, not `pgrep -f podium/podium-node`, which matches the ssh command line
you are running it from and reports the process as still alive forever.

## 7. Prove it

In order of how much they catch:

```sh
deploy/run-host.sh status                        # three services, and what they answer
./bin/podium version                             # client and control plane, both $V
./bin/podium nodes                               # every worker online, and its version
./bin/podium run --image $REG/podium-agent-runtime:$V --label linux -- uname -m
./bin/podium run --image $REG/podium-agent-runtime:$V --label mac   -- uname -m
```

One task per architecture is the check that would have caught every image mistake worth
catching: it proves the worker can pull the tag, that the tag has that platform, and that the
image runs as the user it declares.

If the runtime or the profile changed, the only real proof is a turn. Send one through the chat
and read its output — a turn reports success on an exit code, so a harness that quietly fell
back to a different agent still looks green from here.

## Things that look broken and are not

- **A node `unreachable` for about 75 seconds after a server restart.** Its first dial goes to
  a stale path and sits there until it times out; the retry connects in under a second. Read the
  node's log for `control plane stream open` rather than restarting it — restarting only starts
  the same clock again.
- **`ps aux | grep podium` matching nothing** while the stack is plainly up. Use `pgrep -fl`.
- **`podium logs <task>` looking empty** for a failed turn. The interesting artifact is
  `transcript.jsonl` — `podium artifacts <task>`, then `podium artifact get <id>`. An API error
  from the model provider is in there and nowhere else.
