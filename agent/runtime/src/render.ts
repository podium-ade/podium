// The system prompt of a turn the runtime does not run.
//
// A conductor with a chat host answers conversations on one long-lived opencode 2 service
// rather than starting this runtime per turn. The prompt is still this runtime's: the brief
// is the same document and buildSystemPrompt is the one place it becomes words, so a chat
// host turn and a host turn are told the same thing. What differs is how the model calls a
// tool. opencode 2 offers MCP tools only inside its `execute` tool, as JavaScript —
// `tools.podium.delegate({...})` — so the names the prompt gives in MCP form
// (`podium_delegate`) are rewritten to that form, and a short block says how to call them.
//
// Run as `node dist/render.js` with the brief in PODIUM_AGENT_TURN; it prints
// {"system": "..."} and exits 0, or exits 2 when the brief is unusable.

import { BriefError, ExitBriefInvalid, decodeBrief } from "./brief.js";
import { MemoryTools, PullRequestServer, buildSystemPrompt } from "./prompt.js";
import { DelegateServer, TitleServer } from "./opencode.js";

/** CodeModeServers are the servers a chat host's location registers, by the name the prompt uses. */
export const CodeModeServers = [DelegateServer, TitleServer];

/** codeModePath is how a tool is reached inside `execute`. A name that is not an identifier
 * takes bracket notation, which is how opencode 2 writes it in its own catalog. */
export function codeModePath(server: string, tool: string): string {
  const ns = /^[A-Za-z_$][\w$]*$/.test(server) ? `.${server}` : `["${server}"]`;
  return `tools${ns}.${tool}`;
}

/**
 * codeMode rewrites the MCP-form tool names in a prompt to their code-mode paths: `podium_delegate`
 * becomes `tools.podium.delegate`, and the memory server's `mcp__memory__recall` becomes
 * `tools.memory.recall`. Only the servers a chat host registers are rewritten; anything else
 * the prompt quotes is left as written.
 */
export function codeMode(prompt: string): string {
  let out = prompt;
  for (const server of CodeModeServers) {
    out = out.replace(new RegExp(`\\b${server}_([a-z][a-z_]*)\\b`, "g"), (_, tool: string) => codeModePath(server, tool));
  }
  for (const name of MemoryTools) {
    const tool = name.split("__").pop() ?? name;
    out = out.split(name).join(codeModePath("memory", tool));
  }
  return out;
}

/** codeModeBlock tells the model how its tools are called on a chat host. */
export function codeModeBlock(): string {
  return `# How you call your tools

Your tools are JavaScript functions, and you call them inside the \`execute\` tool:
\`return await ${codeModePath(DelegateServer, "delegate")}({ playbook: "...", instruction: "..." })\`.
Await every call and return what you need. A server whose name is not a plain identifier is
reached with brackets, as in \`${codeModePath(PullRequestServer, "<tool>")}\`. Inside \`execute\`,
\`search({ query })\` lists a tool's exact signature. There is no filesystem, no shell and no
network access from there: only these tools.`;
}

/** render is the chat host's system prompt for one brief. */
export function render(env: NodeJS.ProcessEnv): string {
  const brief = decodeBrief(env);
  return `${codeMode(buildSystemPrompt(brief))}\n\n${codeModeBlock()}`;
}

function main(): number {
  try {
    process.stdout.write(JSON.stringify({ system: render(process.env) }));
    return 0;
  } catch (err) {
    if (err instanceof BriefError) {
      process.stderr.write(`podium-agent render: ${err.message}\n`);
      return ExitBriefInvalid;
    }
    throw err;
  }
}

if (import.meta.url === `file://${process.argv[1]}`) {
  process.exitCode = main();
}
