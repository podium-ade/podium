# Podium's own bot

The profile the real deployment runs: `PODIUM_AGENT_PROFILE_DIR` points at this directory,
and `PODIUM_AGENT_SKILLS_DIR` at [`../skills`](../skills) beside it.

```
profile.yaml            the bot's identity, model and default playbook
playbooks/general.yaml  answer the question in the thread
playbooks/podium.yaml   the dogfood: develop Podium itself
prompts/                one prompt per file above, resolved by `file:`
```

The nested `playbooks/playbooks/` is `profiles.Load`'s layout and not a choice — a profile
directory is a `profile.yaml` and a `playbooks/` beside it. [`docs/agent.md`](../docs/agent.md#the-profile-directory)
is the reference for every field.

**This is not the worked example.** [`examples/agent`](../examples/agent) is that, and the two
are separate directories on purpose: this profile clones this repository, holds a GitHub
token, asks for a privileged node and a browser, and none of that belongs in the first thing
a reader copies. `deploy/run-host.sh` therefore still defaults to `examples/agent`, so a first
`make stack-up` gets a bot that loads and runs anywhere; the operator of the real bot sets

```sh
PODIUM_AGENT_PROFILE_DIR=/path/to/podium/playbooks
PODIUM_AGENT_SKILLS_DIR=/path/to/podium/skills
```

in `deploy/.env`, and both are commented in [`../deploy/.env.example`](../deploy/.env.example).

Changing a file here needs a conductor restart — there is no SIGHUP reload. A skill in
`../skills` does not: the library is resolved at the start of every turn.
