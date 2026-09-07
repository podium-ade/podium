// The system prompt is assembled here and nowhere else, from the brief alone: the same
// brief must always produce the same prompt, which is what makes the snapshot test worth
// having.

import { ArtifactsDir } from "./artifacts.js";
import type { SourceKind, TurnBrief } from "./brief.js";
import { WorkspaceDir } from "./repos.js";

/** MemoryTools are the Hindsight MCP tools the memory block talks about. */
export const MemoryTools = ["mcp__memory__recall", "mcp__memory__retain", "mcp__memory__list_tags"];

const sourceLabels: Record<SourceKind, string> = {
  slack: "the Slack thread this came from",
  linear: "the Linear issue this came from",
  chat: "the web chat this came from",
};

/** buildSystemPrompt renders the whole operating contract for one turn. */
export function buildSystemPrompt(brief: TurnBrief): string {
  const sections = [
    brief.profile.system_prompt,
    brief.playbook.system_prompt,
    runtimeBlock(brief),
  ];
  if (brief.memory) {
    sections.push(memoryBlock());
  }
  if (brief.repos && brief.repos.length > 0) {
    sections.push(reposBlock(brief));
  }
  sections.push(transcriptBlock(brief));
  return sections.map((s) => s.trim()).filter((s) => s !== "").join("\n\n");
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

You cannot ask a question and wait for the answer: there is no interactive channel and
nobody is watching this container. If you need something from a human, end the turn with the
question as your last message.

Never print a credential, a token, or the contents of an environment variable that holds
one. They were injected into this container for your tools to use and they must not leave
it.`;
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
