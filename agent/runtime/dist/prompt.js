// The system prompt is assembled here and nowhere else, from the brief alone: the same
// brief must always produce the same prompt, which is what makes the snapshot test worth
// having.
import { ArtifactsDir, ChatTitleName } from "./artifacts.js";
import { onHost } from "./brief.js";
import { WorkspaceDir } from "./repos.js";
/** MemoryTools are the Hindsight MCP tools the memory block talks about. */
export const MemoryTools = ["mcp__memory__recall", "mcp__memory__retain", "mcp__memory__list_tags"];
const sourceLabels = {
    slack: "the Slack thread this came from",
    linear: "the Linear issue this came from",
    chat: "the web chat this came from",
};
/** buildSystemPrompt renders the whole operating contract for one turn. */
export function buildSystemPrompt(brief) {
    const sections = [
        brief.profile.system_prompt,
        brief.playbook.system_prompt,
        onHost(brief) ? hostBlock(brief) : runtimeBlock(brief),
    ];
    if (brief.delegation) {
        sections.push(delegationBlock(brief));
    }
    if (brief.source.kind === "chat" && firstChatTurn(brief)) {
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
function runtimeBlock(brief) {
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

You cannot ask a question and wait for the answer: there is no interactive channel and
nobody is watching this container. If you need something from a human, end the turn with the
question as your last message.

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
function hostBlock(brief) {
    const where = sourceLabels[brief.source.kind];
    return `# This turn

You are ${brief.profile.display_name}, answering one message of ${where}. You are running in
the conductor's own process — NOT in a container — which is why this turn is fast and why it
can do so little by itself.

What you have here: this conversation, your memory if one is configured, the ability to fetch
a URL, and the delegation tools below. What you do NOT have: a shell, a filesystem you should
touch, a repository, a browser. There is no workspace, and a file you write goes nowhere.

So: answer directly when the answer is in this conversation, in your memory, or on a page you
can fetch. Delegate the moment the work needs a machine — reading or changing code, running
anything, looking at a page in a real browser.

The conversation so far is at the end of this prompt, and the message that triggered this
turn is your user message. When you stop, your last message is posted back to ${where},
verbatim: a direct answer, no preamble, no commentary about being an agent.

You cannot ask a question and wait for the answer. If you need something from a human, end
the turn with the question as your last message.

Never print a credential, a token, or the contents of an environment variable that holds
one.`;
}
/**
 * delegationBlock is the menu and the etiquette. The menu is the brief's, so a model cannot
 * be told about a playbook the conductor would then refuse.
 */
function delegationBlock(brief) {
    const menu = (brief.delegation?.playbooks ?? []).map((p) => {
        const has = [];
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
    return `# Delegating work

Work that needs a machine goes to a Podium task, through \`podium_delegate\`. The playbooks
you may ask for, and nothing else:

${menu.join("\n")}

How to do it well:

- The instruction you pass is the ONLY thing the task is told beyond this conversation. Write
  it as a complete brief — what to do, and how it will know it worked — not a subject line.
- \`podium_delegate\` returns at once with a delegation id. The task then runs for minutes or
  hours, and **its progress and its answer appear in this conversation on their own**, so do
  not repeat them and do not paraphrase them as if they were yours.
- Poll \`podium_check_delegation\` for the outcome. Between polls, say what it is doing rather
  than going silent.
- One task per piece of work. If you need two things done, delegate twice; do not fold two
  unrelated jobs into one instruction.
- \`podium_cancel_delegation\` when the work is no longer wanted. A task nobody is waiting for
  still holds a machine and still costs money.`;
}
function memoryBlock() {
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
function reposBlock(brief) {
    const lines = (brief.repos ?? []).map((r) => `- ${r.name} is checked out at ${WorkspaceDir}/${r.name} on branch ${r.default_branch} (${r.url})`);
    return `# Repositories

${lines.join("\n")}`;
}
function firstChatTurn(brief) {
    return !brief.transcript.some((e) => e.role === "assistant");
}
function chatTitleBlock() {
    return `# This chat

This is the first message of a web chat. Write a 3–6 word title for it to ${ArtifactsDir}/${ChatTitleName} — one line, no quotes, no trailing punctuation. Do not mention the title or that file in your answer.`;
}
function transcriptBlock(brief) {
    const lines = [];
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
