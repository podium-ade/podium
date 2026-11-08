// The system prompt is assembled here and nowhere else, from the brief alone: the same
// brief must always produce the same prompt, which is what makes the snapshot test worth
// having.

import { ArtifactsDir, ChatTitleName } from "./artifacts.js";
import { onHost, type SourceKind, type TurnBrief } from "./brief.js";
import { WorkspaceDir, artifactsUnder, workspaceOf } from "./repos.js";
import { TitleServer, TitleTool, titleByTool } from "./title.js";

/** PullRequestServer is the conductor's read-only view of the pull request a host turn is on. */
export const PullRequestServer = "pull-request";

/** MemoryTools are the Hindsight MCP tools the memory block talks about. */
export const MemoryTools = ["mcp__memory__recall", "mcp__memory__retain", "mcp__memory__list_tags"];

const sourceLabels: Record<SourceKind, string> = {
  slack: "the Slack thread this came from",
  linear: "the Linear issue this came from",
  chat: "the web chat this came from",
  github: "the GitHub pull request this came from",
};

/** buildSystemPrompt renders the whole operating contract for one turn. */
export function buildSystemPrompt(brief: TurnBrief): string {
  const sections = [
    brief.profile.system_prompt,
    brief.playbook.system_prompt,
    onHost(brief) ? hostBlock(brief) : runtimeBlock(brief),
    channelBlock(brief),
    unavailableServersBlock(brief),
  ];
  if (brief.delegation && brief.delegation.playbooks.length > 0) {
    sections.push(delegationBlock(brief));
  }
  if (brief.routine) {
    sections.push(routineBlock(brief));
  }
  if (brief.delegation?.routines) {
    sections.push(createRoutineBlock());
  }
  if (onHost(brief) && brief.source.kind === "slack" && brief.delegation) {
    sections.push(slackPlaybookBlock(brief));
  }
  if (onHost(brief) && brief.source.kind === "github") {
    sections.push(pullRequestBlock(brief));
  }
  if (!onHost(brief) && brief.source.kind === "github" && !brief.playbook.github) {
    sections.push(pullRequestTaskBlock());
  }
  if (titledConversation(brief) && firstChatTurn(brief)) {
    sections.push(chatTitleBlock(brief));
  }
  if (brief.desktop && !onHost(brief)) {
    sections.push(desktopBlock());
  }
  if (brief.memory) {
    sections.push(memoryBlock());
  }
  if (brief.repos && brief.repos.length > 0) {
    sections.push(reposBlock(brief));
  }
  sections.push(transcriptBlock(brief));
  return sections.map((s) => s.trim()).filter((s) => s !== "").join("\n\n");
}

/**
 * unavailableServersBlock names the MCP servers this turn was meant to have and does not.
 *
 * Without it, a message that needs one of them reads to the model like any other: it goes
 * looking for the tools, delegates the work to a task that lacks them too, or invents an
 * answer. With it, the model says plainly that it has no access, and why, which is what the
 * person can act on.
 */
function unavailableServersBlock(brief: TurnBrief): string {
  const missing = brief.playbook.unavailable_mcp_servers ?? [];
  if (missing.length === 0) {
    return "";
  }
  const lines = missing.map((s) => `- \`${s.name}\`: ${s.reason}.`).join("\n");
  return `# MCP servers you do not have

You are configured to use these MCP servers, but none of their tools are available in this
turn:

${lines}

If the message needs one of them, do not try to work around it and do not delegate it — a
task started for it would not have it either. Tell the person, in a sentence, that you do not
have access to it and why, and what they can do about it. Then answer whatever else the
message asks. Do not mention these servers when the message does not need them.`;
}

function interactiveRule(brief: TurnBrief): string {
  if (brief.playbook.interactive) {
    return `You can ask a question and wait for the answer: call the \`human_ask\` tool with
the question. The person in ${sourceLabels[brief.source.kind]} will see it, and their
reply comes back as the tool result. Do not end the turn with a question — ask, then
continue. Waiting still counts against this playbook's timeout.`;
  }
  return `You cannot ask a question and wait for the answer: there is no interactive channel and
nobody is watching this container. If you need something from a human, end the turn with the
question as your last message.`;
}

function runtimeBlock(brief: TurnBrief): string {
  const where = sourceLabels[brief.source.kind];
  return `# This turn

You are ${brief.profile.display_name}, running one turn inside a disposable Linux container
that is destroyed when the turn ends. Nothing you leave on disk outlives it and no follow-up
runs in this process.

The conversation so far is at the end of this prompt, and the message that triggered this
turn is your user message.

When you stop, your last message is posted back to ${where}, verbatim. Write it for that
audience: a direct answer, no preamble about what you are about to do and no commentary
about being an agent.

If you produce files for the reader — a screenshot, a report, a patch — save them under
${ArtifactsDir}/ and name each one you want attached, by its exact file name, in your last
message. Only files you name are attached.

${interactiveRule(brief)}

Never print a credential, a token, or the contents of an environment variable that holds
one. They were injected into this container for your tools to use and they must not leave
it.`;
}

/**
 * hostBlock is runtimeBlock's counterpart for a turn running as a child of the conductor.
 *
 * Everything runtimeBlock promises is false here and the differences matter to a model: there
 * is no container, no repository, no shell, and nothing collects a file it writes. Telling it
 * otherwise produces a turn that spends its tools trying to read a workspace that does not
 * exist and names attachments nobody will ever see.
 */
function hostBlock(brief: TurnBrief): string {
  const where = sourceLabels[brief.source.kind];
  const servers = (brief.playbook.mcp_servers ?? []).map((s) => `\`${s.name}\``);
  const reach = servers.length > 0 ? `, the tools of the MCP servers ${servers.join(", ")}` : "";
  const direct =
    servers.length > 0
      ? `
   The same goes for a small action one of your MCP servers does directly — editing a
   ticket, reading an issue, posting a comment. Do it yourself; a task for one tool call is
   a container started for nothing.`
      : "";
  return `# This turn

You are ${brief.profile.display_name}, answering one message of ${where}. You are running in
the conductor's own process — NOT in a container — which is why this turn is fast and why it
can do so little by itself.

What you have here: this conversation, your memory if one is configured, the ability to fetch
a URL${reach}, and the delegation tools below. What you do NOT have: a shell, a filesystem you should
touch, a repository, a browser. There is no workspace, and a file you write goes nowhere.

So there are exactly two things to do with a message, and you decide which in one step:

1. **Answer it**, when the answer is already in this conversation, in your memory, or on a
   page you can fetch.${direct}
2. **Delegate it** otherwise — immediately, on your first tool call if you can.

**Do not investigate first.** You have no repository and no shell, so anything you work out
here about code, a file, a command or a running system is a guess: it costs a turn, and the
task checks it from scratch anyway. Naming a file, quoting a line or reasoning about how
something is implemented is the task's job. Hand it the question, not your answer to it.

The conversation so far is at the end of this prompt, and the message that triggered this
turn is your user message. When you stop, your last message is posted back to ${where},
verbatim: a direct answer, no preamble, no commentary about being an agent.

You cannot ask a question and wait for the answer. If you need something from a human, end
the turn with the question as your last message. Delegation can, when the playbook is
interactive — that is the container's job, not yours.

Never print a credential, a token, or the contents of an environment variable that holds
one.`;
}

/**
 * desktopBlock is a desktop turn's: what the task's prompt says about nobody watching is not
 * true of it, and a model told nothing about the desktop spent its first steps looking for a
 * display server to start and a window tool the image does not carry.
 */
function desktopBlock(): string {
  return `# The desktop

This turn runs on a Linux desktop (XFCE, display :1) that a person watches live beside the
conversation and can take over with their own mouse and keyboard. It is already running when
the turn starts: do not start, check or restart it.

- Your browser tools drive the Chromium on that desktop. Whatever you open there, the person
  sees. Prefer them for anything on the web.
- For the rest of the screen, run commands with \`DISPLAY=:1\` and use \`xdotool\` to find
  windows, click and type.
- The person has a terminal of their own in the workspace: the tmux session \`podium\`.
  \`tmux capture-pane -p -t podium\` shows what is in it.
- The desktop stays up after your answer. Leave on screen what the person should see, and
  say in your answer what you did there.`;
}

/**
 * delegationBlock is the menu and the etiquette. The menu is the brief's, so a model cannot
 * be told about a playbook the conductor would then refuse.
 */
function delegationBlock(brief: TurnBrief): string {
  const menu = (brief.delegation?.playbooks ?? []).map((p) => {
    const has: string[] = [];
    if (p.repos && p.repos.length > 0) {
      has.push(`repositories: ${p.repos.join(", ")}`);
    }
    if (p.docker) {
      has.push("a Docker daemon");
    }
    if (p.browser) {
      has.push("a browser");
    }
    if (p.desktop) {
      has.push("a desktop the person watches live in the chat, with a browser and a terminal");
    }
    if (p.preview) {
      has.push("a preview that stays up after it finishes");
    }
    if (p.github) {
      has.push("the pull-request review");
    }
    const carries = has.length > 0 ? ` — has ${has.join(", ")}` : "";
    const summary = p.summary && p.summary !== "" ? `: ${p.summary}` : "";
    return `- \`${p.name}\`${carries}${summary}`;
  });
  const running = brief.delegation?.running ?? [];
  const inFlight =
    running.length === 0
      ? ""
      : `

Already running in this conversation — started here and NOT finished:

${running
  .map(
    (d) =>
      `- \`${d.id}\` (\`${d.playbook}\`${d.started_at ? `, started ${d.started_at}` : ""})${
        d.instruction ? `: ${d.instruction}` : ""
      }`,
  )
  .join("\n")}

If this message adds to or corrects one of those, \`podium_inject_delegation\` into it rather
than delegating again. Delegate only for a genuinely new piece of work.
`;
  const previews = brief.delegation?.previews ?? [];
  const live =
    previews.length === 0
      ? ""
      : `
Finished in this conversation and still up as a preview:

${previews
  .map((p) => {
    const urls = Object.entries(p.urls ?? {})
      .map(([name, url]) => `${name} ${url}`)
      .join(", ");
    return `- \`${p.delegation_id}\` (\`${p.playbook}\`, task \`${p.task_id}\`${
      p.expires_at ? `, up until ${p.expires_at}` : ""
    })${urls ? `: ${urls}` : ""}`;
  })
  .join("\n")}

When the person says they are done with one — "I'm done", "you can take it down" — call
\`podium_release_preview\` with its id and say it is down. If it is unclear which one they
mean, ask.
`;
  return `# Delegating work

Work that needs a machine goes to a Podium task, through \`podium_delegate\`. The playbooks
you may ask for, and nothing else:

${menu.join("\n")}
${inFlight}${live}
How to do it well:

- The instruction you pass is the ONLY thing the task is told beyond this conversation. Write
  it as a complete brief — what to do, and how it will know it worked — not a subject line.
  A brief written from the person's own words is enough; you do not have to find the code
  first, and you cannot.
- **Delegate, then stop.** \`podium_delegate\` returns at once with a delegation id, and the
  task's progress and its answer then appear in this conversation **on their own**. So say in
  one sentence what you started, and end the turn. Do not narrate, do not summarise what the
  task is about to do, and do not repeat what it says.
- Because the answer arrives on its own, **never end a turn by apologising for not having
  it**. "A task ran but I did not receive the result" is always wrong: the reader has the
  result, directly above your message, and a denial under it reads as a failure when nothing
  failed. If a task you delegated has not finished when you stop, say that it is running and
  that its answer will follow — or say nothing more at all.
- **Do not poll.** \`podium_check_delegation\` exists for the one case where you cannot
  continue without knowing the outcome — where what to delegate next depends on what this one
  found. It is not a way to wait: a task runs for minutes or hours, every check costs you a
  turn, and a turn spent watching is a turn you no longer have. Ending the turn loses nothing,
  because the answer is delivered without you.
- One task per piece of work. If you need two things done, delegate twice; do not fold two
  unrelated jobs into one instruction.
- A task this conversation already started may still be running after you stop. If the
  person is correcting or adding to that work — "actually change xyz", "use main" —
  \`podium_list_delegations\` then \`podium_inject_delegation\` with their words. Do not
  start a second task for the same job. Inject is a new user message inside the running
  container; the workspace is kept. Delegate only for a new piece of work.
- \`podium_cancel_delegation\` when the work is no longer wanted. A task nobody is waiting for
  still holds a machine and still costs money.
- \`podium_start_chat\` opens a separate Podium chat — a triage, an incident, a side
  discussion — when the person asks for one or wants other people brought in. Write its
  opening as the brief that conversation starts from, invite people by email, and put the
  link it returns in your answer: "Starting a triage — [click here to join](…)". It is not a
  way to delegate work: that is still \`podium_delegate\`.`;
}

/**
 * createRoutineBlock is how a person's web chat sets up a routine: by asking. There is no
 * button in the chat for it; the Routines page is the other way in.
 */
function createRoutineBlock(): string {
  return `# Routines

When the person asks for something to happen on a schedule ("every morning send me…", "check
each hour whether…"), you can set it up with \`podium_create_routine\`. Agree what it does and
when before you call it, and ask their timezone when the time of day matters. Write its
instruction as the whole brief for one run: the run starts fresh, without this conversation.
Tell them when it first runs and link its chat from the tool's answer. Do not offer a routine
for something they asked once.`;
}

/** GitHubReadServer is the conductor's read-only GitHub reader a routine's turn gets. */
export const GitHubReadServer = "github-read";

/**
 * routineBlock is how a turn in a routine's chat behaves. A scheduled run has nobody watching:
 * it decides, acts, and says whether anything happened. The owner's own message there is a
 * conversation about the routine.
 */
function routineBlock(brief: TurnBrief): string {
  const r = brief.routine;
  if (!r) {
    return "";
  }
  const notes =
    r.notes && r.notes.trim() !== ""
      ? `\n## Standing notes from the owner\n\nThese hold for every run, as much as the instruction does:\n\n${r.notes.trim()}\n`
      : "";
  const state = JSON.stringify(r.state ?? {}, null, 1);
  const idle =
    r.idle_runs && r.idle_runs > 0
      ? `\nThe last ${r.idle_runs} run${r.idle_runs === 1 ? "" : "s"} found nothing to do.`
      : "";
  const changed = r.instruction_changed_at
    ? `\nThe instruction was changed at ${r.instruction_changed_at}. Earlier runs in this chat followed an older one; do not copy what they did.`
    : "";
  const hasReader = (brief.playbook.mcp_servers ?? []).some((s) => s.name === GitHubReadServer);
  const find = ` The instruction and the notes name repositories in words; \`list_repositories\` turns them into owner/name. Use the repositories it names, or that clearly fit, and nothing else.`;
  const reads = !hasReader
    ? ""
    : r.system
      ? `\n- Read GitHub with the \`${GitHubReadServer}\` tools — pull requests, their checks, a failed job's log, recent commits — as the Podium App, in any repository it is installed on.${find} To tell the people on a pull request what you found or propose, use \`comment_on_pull_request\`. Reach for a task only when the work needs a machine.`
      : `\n- Read GitHub with the \`${GitHubReadServer}\` tools — pull requests, their checks, a failed job's log, recent commits — as the routine's owner.${find} Reach for a task only when the work needs a machine.`;
  const who = r.system
    ? `\n\nThis is a **system routine**: admins set it up, it belongs to nobody, and it runs with the company's credentials. Nobody can write in this chat; admins steer it by editing the instruction and the notes. "The owner" below means the admins who read this chat.`
    : "";
  if (!r.scheduled) {
    return `# This is a routine's chat

This chat belongs to the routine **${r.name}**, which runs ${r.schedule}. The owner is writing to you
between runs: answer them as you would in any chat. Its runs' answers are above.
${notes}
## The routine's state

\`\`\`json
${state}
\`\`\`

When the owner gives a standing instruction ("skip #42 from now on"), record it: call
\`podium_routine_finish\` with status "done", the state updated to carry it, and the instruction as
\`propose_note\` so they can keep it on the routine.`;
  }
  return `# This is a scheduled run of a routine

You are running the routine **${r.name}** (${r.schedule}). Nobody asked you this just now and
nobody is watching: the instruction below is the routine's, and this run is one of many. Earlier
runs that did something are in this chat above.${idle}${changed}${who}
${notes}
## The routine's state

What earlier runs kept, so you know exactly what has already been handled:

\`\`\`json
${state}
\`\`\`

## How to run

- Decide first whether there is anything to do. Most runs of most routines find nothing new.${reads}
- Do not repeat work the state or the delegations in flight say is already done or under way.
- End EVERY run by calling \`podium_routine_finish\` exactly once:
  - status \`idle\` when there is nothing worth telling the owner. The run stays in the chat quietly,
    is not shown to later runs and they are not notified, so do not pad a run into "done" to say that
    nothing happened.
  - status \`done\` when you did or found something. Then your answer is what the owner reads:
    lead with what changed or what needs them, briefly.
  - pass \`state\` whenever what has been handled changed: the whole object, small, exact facts
    keyed by id (a pull request and its head commit, a ticket number), not a log.
- Do not ask questions: nobody will answer until the next run. Act on the instruction and the notes,
  and say in your answer what you could not decide.`;
}

/**
 * slackPlaybookBlock is how a Slack mention chooses a playbook. There is no default: the
 * message says, or the channel note above says, or the turn asks.
 */
function slackPlaybookBlock(brief: TurnBrief): string {
  const described = Boolean(brief.source.channel?.description);
  const preference = described
    ? `The channel note above is a preference its owner wrote. When the message does not name a playbook, follow that note: a description that names a playbook, or that says what this channel is for, is the choice.`
    : `This channel has no description, so nothing in it prefers a playbook.`;
  return `# Choosing a playbook

There is no default playbook. The instruction above to delegate on your first tool call waits until you know which one. When this message needs a machine, choose from the menu above.

- A playbook the message names — \`/name\`, or the name written out — is that playbook.
- Otherwise match the work to a playbook's summary and to what it can reach. A single playbook on the menu is that playbook.
- ${preference}
- When the message and the channel description, taken together, do not make one playbook clearly right, ask. Name each playbook and the one line you were given about it, then stop. Do not call \`podium_delegate\` until they answer, and do not pick the first name on the menu in order to keep moving.
- A question you can answer from this conversation, from memory, or from a page you can fetch still needs no playbook. Ask only when the work needs a machine and the playbook is the part you cannot tell.`;
}

/**
 * pullRequestBlock is how a host turn behaves on a pull request. The review is a playbook's
 * job and only its answer becomes the review; the assistant answers questions and routes work.
 */
function pullRequestBlock(brief: TurnBrief): string {
  const review = (brief.delegation?.playbooks ?? []).find((p) => p.github)?.name;
  const reads = (brief.playbook.mcp_servers ?? []).some((s) => s.name === PullRequestServer);
  const readLine = reads
    ? `
  Read the pull request itself with the \`${PullRequestServer}\` tools — the changed files, the
  diff, and any file as it is at the head commit — before you reach for a task.`
    : "";
  const reviewLine = review
    ? `- Asked to review, or to review again — new commits, "take another look": delegate to \`${review}\`. Pass on the commits the message names. That playbook reads what it reviewed last and looks only at what changed.`
    : `- No playbook reviews pull requests here. If you are asked for a review, say so.`;
  return `# This pull request

The message is a comment on a GitHub pull request. The review, its inline notes and the
replies so far are in the conversation below. Your answer is posted as a comment — in the
same review thread when the message was a reply in one.

${reviewLine}
- A question about the code, the review or one of its findings: answer it from the
  conversation — the review, the inline notes and the diff they quote — in a few sentences.${readLine}
  When the answer needs more than reading — running the code, the tests, the whole
  repository — delegate it with \`read_only\` set: the task reads the workspace the review left
  and changes nothing, so it never waits for other work.
- One task at a time changes this pull request. If \`podium_delegate\` says another task is
  still working in it, inject into that task when the message adds to its work; otherwise say
  what is running and that you will pick this up when it answers.
- Never write a verdict yourself. "Approve", "Request changes" and inline notes are the
  review playbook's; yours is a reply.
- Asked to change the code: delegate to a playbook on the menu that can push to this pull
  request. If none can, say that you cannot change code here and stop.
- The menu is what this commenter may ask for. It depends on their access to the repository,
  so a playbook missing from it is not one to describe, offer or work around.`;
}

/**
 * pullRequestTaskBlock is a task on a pull request that is not its review: fixing what the
 * review found, usually. Its answer is a reply, and it answers each finding in its own thread.
 */
function pullRequestTaskBlock(): string {
  return `# This pull request

You are working on a GitHub pull request that has a review. The review and its findings are in
the conversation below, and \`.podium/review.json\` in the workspace has the commit it covered.

- Change the pull request's own head branch: commit there and push it. Never open another pull
  request, and never comment on GitHub yourself — your last message is posted for you.
- A push of yours is reviewed again by itself. Do not ask for a review.
- Answer each finding you dealt with under that finding, with a fenced block naming the path
  and line the finding named. \`resolved\` replies and resolves the thread; \`reply\` only
  replies, for a finding you left as it is and why:

\`\`\`resolved
internal/api/handler.go:42
Fixed in abc1234: the error is returned instead of dropped.
\`\`\`

  Everything outside those blocks is posted on the pull request as one comment: say what you
  pushed, in a sentence or two.`;
}

function memoryBlock(): string {
  return `# Shared memory

You have a memory that outlives this container, through the tools ${MemoryTools.join(", ")}.

- Before you start on anything that could have history — a repository, a person, a recurring
  question, a decision already taken — recall it.
- When you learn something durable and organisation-wide, retain it: a decision and the
  reason for it, how a system is put together, who owns what, a convention the team follows.
- Never retain a secret, a credential, or personal data about an individual.
- This memory is shared by every agent in the organisation. Anything you retain, another
  agent will read.`;
}

// reposBlock says where each repository is, and where a relative path starts. The harness's
// project is the workspace and not a repository — on purpose: a repository's own AGENTS.md or
// CLAUDE.md is content a pull request can write, and the project is where the harness reads
// those as instructions. So a tool path like `internal/server` resolves under the workspace,
// finds nothing, and grep and glob fail with "ripgrep execution failed". Saying it here is
// what keeps a turn from spending its first few tool calls finding that out.
function reposBlock(brief: TurnBrief): string {
  const repos = brief.repos ?? [];
  const lines = repos.map(
    (r) => `- ${r.name} is checked out at ${WorkspaceDir}/${r.name} on branch ${r.default_branch} (${r.url})`,
  );
  const example = repos[0] ? `${WorkspaceDir}/${repos[0].name}/src` : `${WorkspaceDir}/<repo>/src`;
  return `# Repositories

${lines.join("\n")}

Your working directory is ${WorkspaceDir}, not a repository. A path that is not absolute is resolved from ${WorkspaceDir}, so give grep, glob, list, read and edit absolute paths (\`${example}\`, not \`src\`), and run bash with its workdir set to the repository.`;
}

function channelBlock(brief: TurnBrief): string {
  const ch = brief.source.channel;
  if (!ch) {
    return "";
  }
  const name = ch.name ? `#${ch.name}` : ch.id;
  const lines = [`This message is in Slack channel ${name}.`];
  if (ch.description) {
    lines.push(`This channel is for: ${ch.description}`);
  }
  return `# This channel

${lines.join("\n")}`;
}

function firstChatTurn(brief: TurnBrief): boolean {
  return !brief.transcript.some((e) => e.role === "assistant");
}

// titledConversation is a source whose threads are listed as chats and can be renamed. A
// Slack thread is one: it is mirrored into the same list as a web chat, so without this it
// kept the fallback name taken verbatim from the first message.
function titledConversation(brief: TurnBrief): boolean {
  const kind = brief.source.kind;
  return kind === "chat" || kind === "slack" || kind === "github";
}

// chatTitleBlock names the chat. A turn that has `write` is told the file: ArtifactsDir is a
// container path, and a host turn has no /workspace — the conductor reads the title out of
// the turn's own jail. A turn without `write` (every host turn) is told to call title_set.
// Asking it to write the file makes it answer that it has no file tool and then skip the name.
function chatTitleBlock(brief: TurnBrief): string {
  if (titleByTool(brief)) {
    return `# This chat

This is the first message of this conversation. Call \`${TitleServer}_${TitleTool}\` once with a 3–6 word title for it — one line, no quotes, no trailing punctuation. You have no filesystem tool, so that call is how this chat gets its name. Do not mention the title or that tool in your answer.`;
  }
  return `# This chat

This is the first message of this conversation. Write a 3–6 word title for it to ${artifactsUnder(workspaceOf(brief))}/${ChatTitleName} — one line, no quotes, no trailing punctuation. Do not mention the title or that file in your answer.`;
}

function transcriptBlock(brief: TurnBrief): string {
  const lines: string[] = [];
  if (brief.transcript_truncated) {
    lines.push("(earlier messages omitted)");
  }
  for (const entry of brief.transcript) {
    lines.push(`[${entry.ts}] ${entry.author} (${entry.role}): ${entry.text}`);
  }
  if (lines.length === 0) {
    lines.push("(no earlier messages; this is the first turn)");
  }
  return `# The conversation so far

${lines.join("\n")}`;
}
