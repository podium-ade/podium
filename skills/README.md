# The bot's own Agent Skills

One directory per skill, each holding a `SKILL.md` with YAML frontmatter. This is what
`PODIUM_AGENT_SKILLS_DIR` points at on the deployment that runs
[`../playbooks`](../playbooks); the rules a bundle has to pass are in
[`../docs/agent.md`](../docs/agent.md#skills--third-party-agent-skills).

An Agent Skill is a **procedure** — the method a turn follows once it has decided to. A
[playbook](../playbooks) is the **environment** it follows it in: the image, the tool
allow-list, the resources, the secrets, the sidecars. Neither belongs in the other, and a
playbook has to name a skill by name before any turn of it can load one.

| | |
|---|---|
| [`validate-pr`](validate-pr/SKILL.md) | attack a change you have just made, in a browser, before reporting the work done |

`validate-pr` is also **shipped as a developer tool**, and not only as this bot's. It is the
procedure half of [`../examples/agent-dev`](../examples/agent-dev/README.md) — the dev kit a
reader copies to get a playbook that writes code — whose setup step is `cp -R skills/validate-pr
"$PODIUM_AGENT_SKILLS_DIR"/`. It is copied rather than duplicated under `examples/`: a skill is
executable content, and two copies of executable content drift until one of them is wrong. This
directory is the one that gets reviewed, so this directory is the one to copy.

A skill in this directory is **instructions and shell commands that run in a turn's
container, beside that turn's GitHub token and model credential**. That is the same privilege
as a change to `playbooks/`, which is why these are files in the repository and arrive by
pull request rather than through the Skills screen in the web UI. Read
[`../docs/security.md`](../docs/security.md#a-playbook-that-carries-agent-skills) before
adding one.

`PODIUM_AGENT_SKILLS_DIR` is also the half of the library that **outranks the browser**: a
name this directory holds cannot be replaced, disabled or deleted through the API, and it wins
a clash with an uploaded skill of the same name.
