// Driving opencode: the config one turn needs, the process, and its event stream.
//
// opencode is the harness because it is provider-agnostic and Podium is not a Claude
// product. The previous harness was the Claude Agent SDK, which speaks the Anthropic
// Messages dialect and only that: pointed at xAI's Anthropic-compatible endpoint it failed
// on the first request, because it sends a `system`-role entry inside `messages[]` that xAI
// rejects. That was not a bug to fix — it was the harness being specific to one vendor.
// Here a provider is `provider/model` on a command line.
//
// Everything about a turn that opencode needs is written into a config file first, because
// a command line cannot carry a system prompt of several kilobytes or an MCP server's
// bearer token.

import { spawn, type ChildProcessByStdio } from "node:child_process";
import { mkdirSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { createInterface } from "node:readline";
import type { Readable } from "node:stream";

/** Binary is the harness. It is on PATH in the runtime image. */
export const Binary = "opencode";

/** AgentName is the agent the config defines and the run selects. */
export const AgentName = "podium";

/**
 * MemoryServer is the MCP server memory arrives on. The name is what prefixes its tools, so
 * a skill's allow-list and the harness agree on what they are called.
 */
export const MemoryServer = "memory";

/** ConfigName and PromptName are what is written into the config directory. */
const ConfigName = "opencode.json";
const PromptName = "system.md";

/**
 * Event is one line of `--format json`. Only the members this runtime reads are named; the
 * stream carries more, and the transcript keeps every line whole regardless.
 */
export interface Event {
  type: string;
  sessionID?: string;
  part?: {
    type?: string;
    /** text carries an assistant message. */
    text?: string;
    /** tool is the tool's name on a tool_use event. */
    tool?: string;
    /** reason is why a step ended: "tool-calls" when it will continue, "stop" when done. */
    reason?: string;
    tokens?: { total?: number; input?: number; output?: number; reasoning?: number };
    /** cost is what the step cost, in USD. It is what turn.json reports. */
    cost?: number;
    state?: { status?: string; title?: string };
  };
}

/** Config is what writeConfig needs: one turn's whole shape. */
export interface Config {
  /** dir is where the config is written. */
  dir: string;
  /** systemPrompt is the operating contract, verbatim. */
  systemPrompt: string;
  /** tools is the skill's allow-list, as harness tool names. */
  tools: string[];
  /** providerID and baseURL point the harness at one model API. */
  providerID: string;
  baseURL?: string;
  /** memory is the MCP server, when this host has one. */
  memory?: { url: string; apiKeyEnv: string };
}

/**
 * writeConfig writes the opencode config for one turn and returns its directory.
 *
 * The system prompt goes in its own file and is referenced, rather than inlined into the
 * JSON: it is the whole operating contract, several kilobytes of it, and a prompt that has
 * to survive JSON escaping is a prompt somebody will eventually break.
 *
 * The tool allow-list is expressed as explicit false for everything not named. An allow-list
 * that only says what is permitted would leave the harness's defaults in place for the rest,
 * which is the opposite of what a skill's `allowed_tools` means.
 */
export function writeConfig(cfg: Config): string {
  mkdirSync(cfg.dir, { recursive: true });
  writeFileSync(join(cfg.dir, PromptName), cfg.systemPrompt, "utf8");

  const tools: Record<string, boolean> = {};
  for (const t of KnownTools) {
    tools[t] = cfg.tools.includes(t);
  }
  // A memory tool is named by its server prefix and cannot be listed in KnownTools, which is
  // a fixed set. It is enabled wholesale when this host has memory: the conductor decides
  // whether a turn gets memory at all, and no skill may opt out of it.
  if (cfg.memory) {
    tools[`${MemoryServer}*`] = true;
  }

  const doc: Record<string, unknown> = {
    $schema: "https://opencode.ai/config.json",
    agent: {
      [AgentName]: {
        description: "One Podium turn.",
        mode: "primary",
        prompt: `{file:./${PromptName}}`,
        tools,
      },
    },
  };
  if (cfg.baseURL) {
    doc.provider = { [cfg.providerID]: { options: { baseURL: cfg.baseURL } } };
  }
  if (cfg.memory) {
    doc.mcp = {
      [MemoryServer]: {
        type: "remote",
        url: cfg.memory.url,
        enabled: true,
        // {env:...} is resolved by the harness, so the token is never written to disk and
        // never appears in this config file.
        headers: { Authorization: `Bearer {env:${cfg.memory.apiKeyEnv}}` },
      },
    };
  }
  writeFileSync(join(cfg.dir, ConfigName), JSON.stringify(doc, null, 2), "utf8");
  return cfg.dir;
}

/**
 * KnownTools is the harness's own tool vocabulary, and the set a skill's `allowed_tools` is
 * held to.
 *
 * It is spelled out rather than discovered because it is also the validator: a skill naming
 * a tool that is not here is a skill that would silently run without it, and finding that
 * out from a turn's behaviour is worse than finding it out when the skill is saved.
 */
export const KnownTools = [
  "bash",
  "edit",
  "glob",
  "grep",
  "list",
  "patch",
  "read",
  "task",
  "todoread",
  "todowrite",
  "webfetch",
  "write",
] as const;

/**
 * Child is the harness process. stdin is closed — the run is headless and nothing types at
 * it — so the type says so rather than pretending there is a writable stream.
 */
export type Child = ChildProcessByStdio<null, Readable, Readable>;

/** Run is a started harness process and the arguments it was started with. */
export interface Run {
  child: Child;
  argv: string[];
}

/**
 * start spawns one turn. It does not wait: the caller reads events off `child.stdout` and
 * decides when the turn is over.
 *
 * `--auto` is the same decision the previous harness's `bypassPermissions` was, for the same
 * reason: the run is headless, nobody is there to answer a prompt, and the sandbox is the
 * task container — every capability dropped, no-new-privileges, a private network, a fresh
 * workspace. The policy knob is the tool allow-list in the config, not a prompt.
 */
export function start(opts: {
  configDir: string;
  workdir: string;
  providerID: string;
  model: string;
  effort?: string;
  instruction: string;
  env: NodeJS.ProcessEnv;
}): Run {
  const { argv, cwd, env } = invocation(opts);
  const child = spawn(Binary, argv, { cwd, env, stdio: ["ignore", "pipe", "pipe"] });
  return { child, argv };
}

/**
 * invocation is how the harness is called: the arguments, the directory it runs in, and the
 * environment it runs with. Separated from the spawn so it can be asserted without one.
 *
 * OPENCODE_CONFIG is what makes the config above reach the harness at all. `--dir` names the
 * workspace as the project, and the project — not this process's working directory — is
 * where the harness looks for an opencode.json. Without the variable it finds no config, and
 * `--agent podium` resolves to nothing:
 *
 *     ! agent "podium" not found. Falling back to default agent
 *
 * which is a warning on stderr and a turn that runs anyway, on the harness's own default
 * agent: no system prompt of ours, and no tool allow-list — the skill's `allowed_tools`
 * silently stops being a restriction.
 */
export function invocation(opts: {
  configDir: string;
  workdir: string;
  providerID: string;
  model: string;
  effort?: string;
  instruction: string;
  env: NodeJS.ProcessEnv;
}): { argv: string[]; cwd: string; env: NodeJS.ProcessEnv } {
  const argv = [
    "run",
    "--format",
    "json",
    "--agent",
    AgentName,
    "--auto",
    "--model",
    `${opts.providerID}/${opts.model}`,
    "--dir",
    opts.workdir,
  ];
  if (opts.effort) {
    argv.push("--variant", opts.effort);
  }
  argv.push(opts.instruction);

  return {
    argv,
    cwd: opts.configDir,
    env: { ...opts.env, OPENCODE_CONFIG: join(opts.configDir, ConfigName) },
  };
}

/**
 * events yields one parsed Event per line of the harness's stdout, and the raw line with it
 * so the transcript keeps exactly what was said rather than a re-serialisation of it.
 *
 * A line that is not JSON is skipped rather than fatal: the harness prints the occasional
 * notice on stdout (npm does, for one), and a turn that already produced an answer must not
 * be failed by a stray line after it.
 */
export async function* events(child: Child): AsyncGenerator<{ event: Event; raw: string }> {
  const lines = createInterface({ input: child.stdout, crlfDelay: Infinity });
  for await (const raw of lines) {
    const line = raw.trim();
    if (line === "" || !line.startsWith("{")) {
      continue;
    }
    let event: Event;
    try {
      event = JSON.parse(line) as Event;
    } catch {
      continue;
    }
    yield { event, raw: line };
  }
}
