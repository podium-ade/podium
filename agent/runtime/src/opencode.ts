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
import { lookup as dnsLookupCb } from "node:dns";
import { mkdirSync, writeFileSync } from "node:fs";
import { isIP } from "node:net";
import { join } from "node:path";
import { createInterface } from "node:readline";
import type { Readable } from "node:stream";
import { promisify } from "node:util";

import type { McpServerRef } from "./brief.js";

/** dnsLookup is the callback API as a promise; node:dns/promises resolves differently. */
const dnsLookup = promisify(dnsLookupCb) as (host: string) => Promise<{ address: string }>;

/** Binary is the harness. It is on PATH in the runtime image. */
export const Binary = "opencode";

/** AgentName is the agent the config defines and the run selects. */
export const AgentName = "podium";

/**
 * MemoryServer is the MCP server memory arrives on. The name is what prefixes its tools, so
 * a playbook's allow-list and the harness agree on what they are called.
 */
export const MemoryServer = "memory";

/**
 * BrowserServer is the MCP server that drives the sidecar browser, and the prefix its tools
 * carry. BrowserBinary is installed in the -dev runtime image; it is not fetched at run
 * time, so a turn does not depend on a registry being reachable to be able to look at a
 * page.
 */
export const BrowserServer = "browser";
export const BrowserBinary = "chrome-devtools-mcp";

/**
 * DelegateServer is the MCP server a host turn delegates through, and the prefix its tools
 * carry. It matches ServerName in mcp.ts: the harness names a tool `<server>_<tool>`, so
 * this string is what the conductor's fence and the model's tool list agree to call it.
 */
export const DelegateServer = "podium";

/** DelegateURLFlag matches UrlFlag in mcp.ts. */
export const DelegateURLFlag = "--url";

/**
 * resolveBrowserURL turns the sidecar's name into its address, because Chrome will not
 * answer to a name.
 *
 * The DevTools HTTP endpoint validates the Host header and serves only an IP or localhost:
 * anything else comes back as "Host header is specified and is not an IP address or
 * localhost" and no client gets as far as a WebSocket. The conductor can only name the
 * sidecar — the address is assigned when the node creates the container — so the name is
 * resolved here, in the container that can see it.
 *
 * A lookup that fails is not fatal. The URL goes through unchanged and the browser tools
 * fail with the harness's own error, which is a better turn than one that refuses to start
 * over a browser the agent may never use.
 */
export async function resolveBrowserURL(
  cdpURL: string,
  lookup: (host: string) => Promise<string> = defaultLookup,
): Promise<string> {
  let url: URL;
  try {
    url = new URL(cdpURL);
  } catch {
    return cdpURL;
  }
  // Already an address, or the one name Chrome accepts.
  if (isAddress(url.hostname) || url.hostname === "localhost") {
    return cdpURL;
  }
  try {
    url.hostname = await lookup(url.hostname);
  } catch {
    return cdpURL;
  }
  return url.toString().replace(/\/$/, "");
}

/** isAddress is true for a literal IPv4 or IPv6 host, which needs no resolving. */
function isAddress(host: string): boolean {
  return isIP(host.replace(/^\[|\]$/g, "")) !== 0;
}

async function defaultLookup(host: string): Promise<string> {
  const { address } = await dnsLookup(host);
  return address;
}

/**
 * ReservedServers are the MCP server names this runtime wires up itself. A playbook's own
 * `mcp_servers` may not take one: the harness config is a single map keyed by name, so a
 * registration reusing one would silently replace something the conductor decided the turn
 * gets — the shared memory, the sidecar browser, or the delegation a host turn works
 * through. internal/agent/mcp holds the same list, and refuses a registration much earlier.
 */
export const ReservedServers = new Set([MemoryServer, BrowserServer, DelegateServer]);

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
  /** tools is the playbook's allow-list, as harness tool names. */
  tools: string[];
  /** providerID and baseURL point the harness at one model API. */
  providerID: string;
  baseURL?: string;
  /** memory is the MCP server, when this host has one. */
  memory?: { url: string; apiKeyEnv: string };
  /** browser is the CDP endpoint of the sidecar browser, when the playbook asked for one. */
  browser?: { cdpURL: string };
  /** skills is the Agent Skills this turn may use, by name. Everything else is denied. */
  skills?: string[];
  /**
   * delegation is how a HOST turn reaches a container, when it has one. It becomes a local
   * MCP server beside the turn (mcp.ts) and the `podium*` tools that go with it — the same
   * shape as the browser, and for the same reason: what speaks MCP runs here, and what it
   * drives is somewhere else.
   */
  delegation?: { url: string; entry: string };
  /** mcpServers is the playbook's own MCP servers, already resolved by the conductor. */
  mcpServers?: McpServerRef[];
}

/**
 * skillPermission is the harness's `permission.skill` map: a wildcard deny, then one allow
 * per skill the playbook named.
 *
 * The order is load-bearing. The harness evaluates the LAST matching rule, so the broad
 * rule has to come first — `{"pr-review": "allow", "*": "deny"}` denies pr-review. It is
 * written even when the playbook named nothing, and that is the point: with everything
 * denied the harness drops the `skill` tool from the agent altogether, so a turn with no
 * skills cannot load one by any route, including the skills the harness ships with itself.
 *
 * `--auto` does not undo it: it auto-approves what is not *explicitly* denied, and `*`
 * denies explicitly.
 */
export function skillPermission(names: string[]): Record<string, "allow" | "deny"> {
  const out: Record<string, "allow" | "deny"> = { "*": "deny" };
  for (const name of names) {
    out[name] = "allow";
  }
  return out;
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
 * which is the opposite of what a playbook's `allowed_tools` means.
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
  // whether a turn gets memory at all, and no playbook may opt out of it.
  if (cfg.memory) {
    tools[`${MemoryServer}*`] = true;
  }
  // Same rule for the browser: the conductor decided this turn has one, so the tools that
  // drive it are on. A playbook that did not ask for a browser has neither the sidecar nor
  // these tools, so there is nothing to opt out of. This is a `browser*` tool entry and not
  // a `permission.skill` line: the two grant different things — the browser is a sidecar
  // the playbook asked for, a skill is content it is allowed to load — and neither map may
  // be written over the other.
  if (cfg.browser) {
    tools[`${BrowserServer}*`] = true;
  }
  // And the same rule for delegation: the conductor decided this turn may delegate, and a
  // host turn's short tool list is only defensible because these are on. A playbook cannot
  // opt out of them any more than it can opt out of memory.
  if (cfg.delegation) {
    tools[`${DelegateServer}*`] = true;
  }
  // And the playbook's own servers. Each one's tools are enabled wholesale, because naming
  // the server in `mcp_servers` is what granting it means — the conductor already decided
  // which turns get which, and a playbook cannot describe individual tools of a server
  // whose tool list only exists once the server has been connected to.
  for (const server of cfg.mcpServers ?? []) {
    tools[`${server.name}*`] = true;
  }

  const doc: Record<string, unknown> = {
    $schema: "https://opencode.ai/config.json",
    permission: { skill: skillPermission(cfg.skills ?? []) },
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
  const mcp: Record<string, unknown> = {};
  if (cfg.memory) {
    mcp[MemoryServer] = {
      type: "remote",
      url: cfg.memory.url,
      enabled: true,
      // {env:...} is resolved by the harness, so the token is never written to disk and
      // never appears in this config file.
      headers: { Authorization: `Bearer {env:${cfg.memory.apiKeyEnv}}` },
    };
  }
  if (cfg.browser) {
    // A local server, unlike memory: the thing speaking MCP runs in this container and the
    // thing it drives is the sidecar. `--browserUrl` is what makes that split work — the
    // server attaches to a browser it did not launch, so no Chrome is installed here and
    // the one being driven is isolated in its own container with its own profile.
    mcp[BrowserServer] = {
      type: "local",
      command: [BrowserBinary, "--browserUrl", cfg.browser.cdpURL],
      enabled: true,
    };
  }
  if (cfg.delegation) {
    // A local server, like the browser's: this runtime's own second entrypoint, run by the
    // same node that is running the turn. The ADDRESS is an argument and the TOKEN is not —
    // it is read from the environment by the child, because an argument is visible in a
    // process list.
    mcp[DelegateServer] = {
      type: "local",
      command: [process.execPath, cfg.delegation.entry, DelegateURLFlag, cfg.delegation.url],
      enabled: true,
    };
  }
  for (const server of cfg.mcpServers ?? []) {
    if (ReservedServers.has(server.name)) {
      // Unreachable while the conductor refuses the reserved names, and refused again here
      // because the cost of it getting through is silent: this map is keyed by name, so a
      // second `memory` would replace the shared memory a turn cannot opt out of, and a
      // second `podium` would replace the delegation a host turn works through.
      throw new Error(`mcp server ${server.name} may not use a reserved name`);
    }
    mcp[server.name] = {
      type: "remote",
      url: server.url,
      enabled: true,
      // No token means no header, which is what an unauthenticated server wants. Where
      // there is one, {env:...} is resolved by the harness, so the token is never written
      // to disk and never appears in this config file — exactly as memory's is.
      ...(server.token_env
        ? { headers: { Authorization: `Bearer {env:${server.token_env}}` } }
        : {}),
    };
  }
  if (Object.keys(mcp).length > 0) {
    doc.mcp = mcp;
  }
  writeFileSync(join(cfg.dir, ConfigName), JSON.stringify(doc, null, 2), "utf8");
  return cfg.dir;
}

/**
 * KnownTools is the harness's own tool vocabulary, and the set a playbook's `allowed_tools` is
 * held to.
 *
 * It is spelled out rather than discovered because it is also the validator: a playbook naming
 * a tool that is not here is a playbook that would silently run without it, and finding that
 * out from a turn's behaviour is worse than finding it out when the playbook is saved.
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
 * agent: no system prompt of ours, and no tool allow-list — the playbook's `allowed_tools`
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
