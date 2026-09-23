// The system prompt is assembled here and nowhere else, from the brief alone: the same
// brief must always produce the same prompt, which is what makes the snapshot test worth
// having.

import { ArtifactsDir, ChatTitleName } from "./artifacts.js";
import { onHost, type SourceKind, type TurnBrief } from "./brief.js";
import { WorkspaceDir } from "./repos.js";

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
  ];
  if (brief.delegation) {
    sections.push(delegationBlock(brief));
  }
  if (onHost(brief) && brief.source.kind === "slack" && brief.delegation) {
    sections.push(slackPlaybookBlock(brief));
  }
  if (titledConversation(brief) && firstChatTurn(brief)) {
    sections.push(chatTitleBlock());
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
  return `# This turn

You are ${brief.profile.display_name}, answering one message of ${where}. You are running in
the conductor's own process — NOT in a container — which is why this turn is fast and why it
can do so little by itself.

What you have here: this conversation, your memory if one is configured, the ability to fetch
a URL, and the delegation tools below. What you do NOT have: a shell, a filesystem you should
touch, a repository, a browser. There is no workspace, and a file you write goes nowhere.

So there are exactly two things to do with a message, and you decide which in one step:

1. **Answer it**, when the answer is already in this conversation, in your memory, or on a
   page you can fetch.
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
  return `# Delegating work

Work that needs a machine goes to a Podium task, through \`podium_delegate\`. The playbooks
you may ask for, and nothing else:

${menu.join("\n")}
${inFlight}
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
  still holds a machine and still costs money.`;
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

function reposBlock(brief: TurnBrief): string {
  const lines = (brief.repos ?? []).map(
    (r) => `- ${r.name} is checked out at ${WorkspaceDir}/${r.name} on branch ${r.default_branch} (${r.url})`,
  );
  return `# Repositories

${lines.join("\n")}`;
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

function chatTitleBlock(): string {
  return `# This chat

This is the first message of this conversation. Write a 3–6 word title for it to ${ArtifactsDir}/${ChatTitleName} — one line, no quotes, no trailing punctuation. Do not mention the title or that file in your answer.`;
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
