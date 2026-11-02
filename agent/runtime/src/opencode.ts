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

import { spawn, spawnSync, type ChildProcessByStdio } from "node:child_process";
import { randomBytes } from "node:crypto";
import { lookup as dnsLookupCb } from "node:dns";
import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { createServer, isIP } from "node:net";
import { join } from "node:path";
import { createInterface } from "node:readline";
import type { Readable } from "node:stream";
import { setTimeout as sleep } from "node:timers/promises";
import { promisify } from "node:util";

import type { McpServerRef } from "./brief.js";
import { TitleServer } from "./title.js";

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

/**
 * HumanServer is the MCP server an interactive turn asks a person through. The name is
 * the prefix its tools carry (`human_ask`).
 */
export const HumanServer = "human";

export { TitleServer };

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
 * gets — the shared memory, the sidecar browser, the delegation a host turn works
 * through, the ask tool an interactive turn waits on, or the title tool a first turn
 * names the chat with. internal/agent/mcp holds the same list, and refuses a registration
 * much earlier.
 */
export const ReservedServers = new Set([
  MemoryServer,
  BrowserServer,
  DelegateServer,
  HumanServer,
  TitleServer,
]);

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
  /**
   * error is set on an error event: the harness's own reason for failing. opencode 2 says
   * `{type, message}`; opencode 1 said `{name, data: {message}}`.
   */
  error?: { name?: string; type?: string; message?: string; data?: { message?: string } };
  part?: {
    id?: string;
    type?: string;
    /** text carries an assistant message, or the thought on a reasoning event. */
    text?: string;
    /** tool is the tool's name on a tool_use event. */
    tool?: string;
    callID?: string;
    /** reason is why a step ended: "tool-calls" when it will continue, "stop" when done. */
    reason?: string;
    tokens?: { total?: number; input?: number; output?: number; reasoning?: number };
    /** cost is what the step cost, in USD. It is what turn.json reports. */
    cost?: number;
    state?: { status?: string; title?: string; input?: unknown; output?: unknown; error?: unknown };
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
  /** apiKeyEnv names the variable holding the credential, so a ChatGPT token can add the account header. */
  apiKeyEnv?: string;
  /** memory is the MCP server, when this host has one. */
  memory?: { url: string; apiKeyEnv: string };
  /** browser is the CDP endpoint of the sidecar browser, when the playbook asked for one. */
  browser?: { cdpURL: string };
  /** skills is the Agent Skills this turn may use, by name. Everything else is denied. */
  skills?: string[];
  /** skillsDir is the directory holding those skills, one directory each. */
  skillsDir?: string;
  /**
   * delegation is how a HOST turn reaches a container, when it has one. It becomes a local
   * MCP server beside the turn (mcp.ts) and the `podium*` tools that go with it — the same
   * shape as the browser, and for the same reason: what speaks MCP runs here, and what it
   * drives is somewhere else.
   */
  delegation?: { url: string; entry: string };
  /** mcpServers is the playbook's own MCP servers, already resolved by the conductor. */
  mcpServers?: McpServerRef[];
  /**
   * interactive is how a playbook turn asks a human a question and waits. It becomes a
   * local MCP server beside the turn (ask.ts) and the `human*` tools that go with it.
   */
  interactive?: { entry: string };
  /**
   * title is how a first turn that cannot write a file names the conversation. It becomes
   * a local MCP server (title.ts). The file path is an environment variable, not an
   * argument: the model must not be able to choose where the title is written.
   */
  title?: { entry: string };
}

/**
 * skillRules is the agent's skill permissions: a wildcard deny, then one allow per skill the
 * playbook named.
 *
 * The order is load-bearing. The harness evaluates the LAST matching rule, so the broad
 * rule has to come first — an allow followed by `*` deny denies it. It is written even when
 * the playbook named nothing, and that is the point: with every skill denied, a turn with no
 * skills cannot load one by any route, including the skills the harness ships with itself.
 * (opencode 2 still lists the `skill` tool; every load through it is refused.)
 *
 * `--auto` does not undo it: it auto-approves what is not *explicitly* denied, and `*`
 * denies explicitly.
 */
export function skillRules(names: string[]): Rule[] {
  return [
    { action: "skill", resource: "*", effect: "deny" },
    ...names.map((name): Rule => ({ action: "skill", resource: name, effect: "allow" })),
  ];
}

/** Rule is one of the harness's permission rules. */
export interface Rule {
  action: string;
  resource: string;
  effect: "allow" | "deny" | "ask";
}

/**
 * chatgptAccountId is the ChatGPT account a Codex access token names. The token is a JWT;
 * the claim is only used as a request header, never to authorise anything here.
 */
export function chatgptAccountId(token: string): string | undefined {
  const parts = token.split(".");
  const payloadB64 = parts[1];
  if (parts.length !== 3 || payloadB64 === undefined) {
    return undefined;
  }
  try {
    const payload = JSON.parse(Buffer.from(payloadB64, "base64url").toString("utf8")) as {
      "https://api.openai.com/auth"?: { chatgpt_account_id?: unknown };
    };
    const id = payload["https://api.openai.com/auth"]?.chatgpt_account_id;
    return typeof id === "string" && id !== "" ? id : undefined;
  } catch {
    return undefined;
  }
}

function chatgptHeaders(cfg: Config): Record<string, string> | undefined {
  if (cfg.providerID !== "openai" || !cfg.apiKeyEnv) {
    return undefined;
  }
  const account = chatgptAccountId(process.env[cfg.apiKeyEnv] ?? "");
  if (!account) {
    return undefined;
  }
  return { "ChatGPT-Account-Id": account, originator: "podium" };
}

/**
 * mcpServer is one MCP server as opencode 2 reads it.
 *
 * `codemode: false` on every one. opencode 2 otherwise reaches an MCP server's tools only
 * through Code Mode — JavaScript inside its `execute` tool — and the prompts, the playbooks'
 * allow-lists and the conductor's fence all name them as plain tools, `<server>_<tool>`.
 * With Code Mode off they are exactly that.
 */
function mcpServer(entry: Record<string, unknown>): Record<string, unknown> {
  return { ...entry, codemode: false };
}

/**
 * playbookMcpExtras is what a playbook's own server config may add to the entry this runtime
 * writes, translated to opencode 2. A v1 `timeout` was one number of milliseconds; v2 has one
 * per phase. Every other key opencode 1 took is dropped: v2 rejects a config it cannot read.
 */
function playbookMcpExtras(config: Record<string, unknown>): Record<string, unknown> {
  const out: Record<string, unknown> = {};
  if (typeof config.timeout === "number" && config.timeout > 0) {
    const ms = Math.round(config.timeout);
    out.timeout = { startup: ms, catalog: ms, execution: ms };
  } else if (config.timeout && typeof config.timeout === "object") {
    out.timeout = config.timeout;
  }
  if (config.oauth !== undefined) {
    out.oauth = config.oauth;
  }
  if (typeof config.protocol === "string") {
    out.protocol = config.protocol;
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
 * The tool allow-list is expressed as an explicit deny for every built-in tool not named. An
 * allow-list that only says what is permitted would leave the harness's defaults in place for
 * the rest, which is the opposite of what a playbook's `allowed_tools` means. A denied tool is
 * not offered to the model at all.
 */
export function writeConfig(cfg: Config): string {
  mkdirSync(cfg.dir, { recursive: true });
  writeFileSync(join(cfg.dir, PromptName), cfg.systemPrompt, "utf8");

  const allowed = new Set(cfg.tools.flatMap((t) => harnessTools(t)));
  const permissions: Rule[] = [];
  for (const t of DeniableTools) {
    if (!allowed.has(t)) {
      permissions.push({ action: t, resource: "*", effect: "deny" });
    }
  }
  // opencode's own tools: moving the session to another directory, making worktrees, listing
  // models and MCP resources. With Code Mode denied they are offered as plain tools, and none
  // of them is a playbook's to grant.
  permissions.push({ action: `${HarnessToolPrefix}*`, resource: "*", effect: "deny" });
  // MCP tools are not in that list, and need no allow: `--auto` approves anything not
  // explicitly denied. The conductor decides which servers a turn gets — memory, the
  // browser, delegation, the ask and title tools, the playbook's own — and naming a server
  // is what granting its tools means, since its tool list only exists once it is connected.
  permissions.push(...skillRules(cfg.skills ?? []));

  const doc: Record<string, unknown> = {
    $schema: "https://opencode.ai/config.json",
    share: "disabled",
    // Read where the node mounted them, read-only, rather than copied anywhere first.
    ...(cfg.skillsDir ? { skills: [cfg.skillsDir] } : {}),
    // The todo tool is experimental in opencode 2, and off unless asked for.
    ...(allowed.has("todowrite") ? { experimental: { todo: true } } : {}),
    agents: {
      // opencode names a session with a model call of its own. Nobody reads that name: the
      // conductor names the chat, through title_set or its own call.
      title: { disabled: true },
      [AgentName]: {
        description: "One Podium turn.",
        mode: "primary",
        system: `{file:./${PromptName}}`,
        permissions,
      },
    },
  };
  const settings: Record<string, unknown> = {};
  if (cfg.baseURL) {
    settings.baseURL = cfg.baseURL;
  }
  if (cfg.apiKeyEnv) {
    // {env:...} is resolved by the harness, so the key is never written to this file. It is
    // named for every provider rather than left to the harness to find: a provider that
    // lists several variables would otherwise pick one this turn did not set.
    settings.apiKey = `{env:${cfg.apiKeyEnv}}`;
  }
  const provider: Record<string, unknown> = {};
  if (Object.keys(settings).length > 0) {
    provider.settings = settings;
  }
  const headers = chatgptHeaders(cfg);
  if (headers) {
    provider.headers = headers;
  }
  if (Object.keys(provider).length > 0) {
    doc.providers = { [cfg.providerID]: provider };
  }
  const mcp: Record<string, unknown> = {};
  if (cfg.memory) {
    mcp[MemoryServer] = mcpServer({
      type: "remote",
      url: cfg.memory.url,
      // {env:...} is resolved by the harness, so the token is never written to disk and
      // never appears in this config file.
      headers: { Authorization: `Bearer {env:${cfg.memory.apiKeyEnv}}` },
    });
  }
  if (cfg.browser) {
    // A local server, unlike memory: the thing speaking MCP runs in this container and the
    // thing it drives is the sidecar. `--browserUrl` is what makes that split work — the
    // server attaches to a browser it did not launch, so no Chrome is installed here and
    // the one being driven is isolated in its own container with its own profile.
    mcp[BrowserServer] = mcpServer({
      type: "local",
      command: [BrowserBinary, "--browserUrl", cfg.browser.cdpURL],
    });
  }
  if (cfg.delegation) {
    // A local server, like the browser's: this runtime's own second entrypoint, run by the
    // same node that is running the turn. The ADDRESS is an argument and the TOKEN is not —
    // it is read from the environment by the child, because an argument is visible in a
    // process list.
    mcp[DelegateServer] = mcpServer({
      type: "local",
      command: [process.execPath, cfg.delegation.entry, DelegateURLFlag, cfg.delegation.url],
    });
  }
  if (cfg.interactive) {
    mcp[HumanServer] = mcpServer({ type: "local", command: [process.execPath, cfg.interactive.entry] });
  }
  if (cfg.title) {
    mcp[TitleServer] = mcpServer({ type: "local", command: [process.execPath, cfg.title.entry] });
  }
  for (const server of cfg.mcpServers ?? []) {
    if (ReservedServers.has(server.name)) {
      // Unreachable while the conductor refuses the reserved names, and refused again here
      // because the cost of it getting through is silent: this map is keyed by name, so a
      // second `memory` would replace the shared memory a turn cannot opt out of, and a
      // second `podium` would replace the delegation a host turn works through.
      throw new Error(`mcp server ${server.name} may not use a reserved name`);
    }
    const config = server.config ?? {};
    // The conductor refuses these at save time. Refused again because the harness expands
    // {env:} and {file:} anywhere in its config, and one here would send the container's own
    // credentials to the server.
    if (/\{(env|file):/.test(JSON.stringify(config))) {
      throw new Error(`mcp server ${server.name} config may not reference {env:} or {file:}`);
    }
    const headers: Record<string, string> = {};
    for (const [key, value] of Object.entries((config.headers ?? {}) as Record<string, string>)) {
      if (key.toLowerCase() === "authorization") {
        throw new Error(`mcp server ${server.name} config may not set ${key}`);
      }
      headers[key] = value;
    }
    // No token means no Authorization header, which is what an unauthenticated server wants.
    // Where there is one, {env:...} is resolved by the harness, so the token is never written
    // to disk and never appears in this config file — exactly as memory's is.
    if (server.token_env) {
      headers.Authorization = `Bearer {env:${server.token_env}}`;
    }
    mcp[server.name] = mcpServer({
      ...playbookMcpExtras(config),
      type: "remote",
      url: server.url,
      ...(Object.keys(headers).length > 0 ? { headers } : {}),
    });
  }
  if (Object.keys(mcp).length > 0) {
    doc.mcp = { servers: mcp };
  }
  writeFileSync(join(cfg.dir, ConfigName), JSON.stringify(doc, null, 2), "utf8");
  return cfg.dir;
}

/** mcpCount is how many MCP servers the config in dir names: what serve waits to connect. */
export function mcpCount(dir: string): number {
  try {
    const doc = JSON.parse(readFileSync(join(dir, ConfigName), "utf8")) as {
      mcp?: { servers?: Record<string, unknown> };
    };
    return Object.keys(doc.mcp?.servers ?? {}).length;
  } catch {
    return 0;
  }
}

/**
 * KnownTools is the harness's own tool vocabulary, and the set a playbook's `allowed_tools` is
 * held to. It mirrors Tools in internal/agent/profiles/agents.go.
 *
 * It is spelled out rather than discovered because it is also the validator: a playbook naming
 * a tool that is not here is a playbook that would silently run without it, and finding that
 * out from a turn's behaviour is worse than finding it out when the playbook is saved.
 */
export const KnownTools = [
  "shell",
  "edit",
  "write",
  "read",
  "glob",
  "grep",
  "webfetch",
  "websearch",
  "subagent",
  "todowrite",
] as const;

/**
 * LegacyTools are opencode 1's names, which saved playbooks still use, and the opencode 2
 * tools each one means. The map is explicit so that no name is guessed at: `list` was a
 * directory listing, which `glob` does; `patch` applied a diff, which `edit` does; and the
 * todo list is one tool now.
 */
export const LegacyTools: Readonly<Record<string, readonly string[]>> = {
  bash: ["shell"],
  task: ["subagent"],
  list: ["glob"],
  patch: ["edit"],
  todoread: ["todowrite"],
};

/** harnessTools is what a name in a playbook's allowed_tools turns on. */
export function harnessTools(name: string): readonly string[] {
  return LegacyTools[name] ?? [name];
}

/**
 * DeniableTools are the built-in tools a turn is denied unless its playbook names them. Two
 * are denied always: `question`, because nobody is at a headless turn to answer it (an
 * interactive playbook asks through human_ask), and `execute`, Code Mode, which no server
 * here uses and which is the only way to the harness's own session and worktree tools.
 */
const DeniableTools = [...KnownTools, "question", "execute"];

/** HarnessToolPrefix names opencode's own tools, `opencode_session_move` and the rest. */
const HarnessToolPrefix = "opencode_";

/**
 * Server is the harness's API server for one turn: started once, before the first run, and
 * stopped when the turn ends.
 *
 * Why not `opencode run --standalone`, which starts one of its own: opencode 2 sends a
 * session's first request before its MCP servers have connected, and that request offers the
 * model no MCP tools at all. A turn whose first move is to recall from memory, or to look at
 * the browser, would not find the tool. So the server is started first, the runtime waits
 * until every server it was given has connected, and each run attaches to it with `--server`.
 * The same server carries the resumes and the follow-ups of a warm turn.
 */
export interface Server {
  url: string;
  password: string;
  child: ChildProcessByStdio<null, Readable, Readable>;
  stop: () => void;
}

/** ServeTimeoutMs bounds the server's start, and MCPTimeoutMs the wait for its MCP servers. */
export const ServeTimeoutMs = 60_000;
export const MCPTimeoutMs = 30_000;

/**
 * MCPSettleMs is how long after its servers connect a session may start. opencode publishes a
 * location's tools a little after it reports the servers connected; measured against 2.2.5 by
 * the chat host (internal/agent/chathost, DefaultSettle).
 */
export const MCPSettleMs = 1_500;

/**
 * serve starts the server for a turn whose config is in configDir and whose workspace is
 * workdir, and resolves once it answers and its MCP servers are connected.
 *
 * A server that fails to connect does not fail the turn: the turn runs without it, as it did
 * on opencode 1, and the reason is in the task log. The password is the server's own, made
 * here; it binds to loopback only.
 */
export async function serve(opts: {
  configDir: string;
  workdir: string;
  env: NodeJS.ProcessEnv;
  warn: (line: string) => void;
}): Promise<Server> {
  const port = await freePort();
  const password = randomBytes(24).toString("base64url");
  const child = spawn(Binary, ["serve", "--hostname", "127.0.0.1", "--port", String(port)], {
    cwd: opts.workdir,
    env: serverEnv(opts.env, opts.configDir, password),
    stdio: ["ignore", "pipe", "pipe"],
  });
  child.on("error", (err) => opts.warn(spawnError(err)));
  child.stdout.on("data", () => {});
  child.stderr.on("data", (chunk: Buffer) => {
    const line = chunk.toString().trimEnd();
    if (line !== "") opts.warn(line);
  });
  const url = `http://127.0.0.1:${port}`;
  const server: Server = { url, password, child, stop: () => child.kill("SIGTERM") };
  const exited = new Promise<never>((_, reject) => {
    child.once("exit", (code) => reject(new Error(`${Binary} serve exited ${code} before it was ready`)));
    child.once("error", (err) => reject(err));
  });
  exited.catch(() => {});
  try {
    await Promise.race([awaitReady(server, opts.workdir, mcpCount(opts.configDir), opts.warn), exited]);
  } catch (err) {
    server.stop();
    throw err;
  }
  return server;
}

/** serverEnv is the environment the server runs with: the turn's, plus where its config is. */
export function serverEnv(env: NodeJS.ProcessEnv, configDir: string, password: string): NodeJS.ProcessEnv {
  return {
    ...env,
    OPENCODE_CONFIG: join(configDir, ConfigName),
    OPENCODE_SERVER_PASSWORD: password,
    OPENCODE_DISABLE_AUTOUPDATE: "1",
  };
}

async function awaitReady(server: Server, workdir: string, servers: number, warn: (l: string) => void) {
  const auth = `Basic ${Buffer.from(`opencode:${server.password}`).toString("base64")}`;
  const get = async (path: string): Promise<Response | undefined> => {
    try {
      return await fetch(server.url + path, { headers: { Authorization: auth }, signal: AbortSignal.timeout(5_000) });
    } catch {
      return undefined;
    }
  };
  const until = Date.now() + ServeTimeoutMs;
  for (;;) {
    const res = await get("/global/health");
    if (res?.ok) break;
    if (Date.now() > until) throw new Error(`${Binary} serve did not answer within ${ServeTimeoutMs / 1000}s`);
    await sleep(200);
  }
  if (servers === 0) return;
  const mcpUntil = Date.now() + MCPTimeoutMs;
  const query = `/api/mcp?location%5Bdirectory%5D=${encodeURIComponent(workdir)}`;
  for (;;) {
    const res = await get(query);
    const body = (res?.ok ? await res.json().catch(() => undefined) : undefined) as
      | { data?: { name: string; status?: { status?: string; error?: string } }[] }
      | undefined;
    const list = body?.data ?? [];
    const settled = list.filter((s) => s.status?.status !== "pending");
    if (list.length >= servers && settled.length === list.length) {
      for (const s of list) {
        if (s.status?.status !== "connected") {
          warn(`mcp server ${s.name} is ${s.status?.status ?? "unknown"}: ${s.status?.error ?? ""}`.trimEnd());
        }
      }
      break;
    }
    if (Date.now() > mcpUntil) {
      const waiting = list.filter((s) => s.status?.status === "pending").map((s) => s.name);
      warn(`mcp servers still connecting after ${MCPTimeoutMs / 1000}s; starting without waiting: ${waiting.join(", ")}`);
      break;
    }
    await sleep(100);
  }
  await sleep(MCPSettleMs);
}

function freePort(): Promise<number> {
  return new Promise((resolve, reject) => {
    const s = createServer();
    s.once("error", reject);
    s.listen(0, "127.0.0.1", () => {
      const addr = s.address();
      s.close(() => (addr && typeof addr === "object" ? resolve(addr.port) : reject(new Error("no port"))));
    });
  });
}

function spawnError(err: Error): string {
  return (err as NodeJS.ErrnoException).code === "ENOENT"
    ? `${Binary} is not on PATH. Host turns need the same harness the runtime image installs (@opencode/cli).`
    : err.message;
}

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
 * start spawns one run against the turn's server. It does not wait: the caller reads events
 * off `child.stdout` and decides when the turn is over.
 *
 * `--auto` is the same decision the previous harness's `bypassPermissions` was, for the same
 * reason: the run is headless, nobody is there to answer a prompt, and the sandbox is the
 * task container — every capability dropped, no-new-privileges, a private network, a fresh
 * workspace. The policy knob is the agent's permissions in the config, not a prompt.
 */
export function start(opts: {
  server: Server;
  workdir: string;
  providerID: string;
  model: string;
  effort?: string;
  instruction: string;
  sessionID?: string;
  env: NodeJS.ProcessEnv;
}): Run {
  const { argv, cwd, env } = invocation({ ...opts, thinking: takesThinking(opts.env) });
  const child = spawn(Binary, argv, { cwd, env, stdio: ["ignore", "pipe", "pipe"] });
  // ENOENT is an 'error' event, not a non-zero exit. Without a listener Node treats it as
  // unhandled and kills THIS process with status 1 — before main can emit a final — which
  // is how a host turn ends as "said nothing at all".
  child.on("error", (err) => {
    process.stderr.write(`podium-agent: ${spawnError(err)}\n`);
  });
  return { child, argv };
}

let thinks: boolean | undefined;

/**
 * takesThinking reports whether the harness on PATH accepts `--thinking`, asking it once.
 *
 * The image pins a harness that does. A HOST turn runs whatever npm installed on that host,
 * and the harness's argument parser is strict: one given a flag it does not know prints its
 * usage and exits 1 before the turn has begun, with nothing on stdout to say why. Without the
 * flag a turn still runs; the chat only misses the reasoning.
 */
export function takesThinking(
  env: NodeJS.ProcessEnv,
  help = () => {
    const r = spawnSync(Binary, ["run", "--help"], { env, encoding: "utf8", timeout: 15_000 });
    return `${r.stdout ?? ""}${r.stderr ?? ""}`;
  },
): boolean {
  thinks ??= help().includes("--thinking");
  return thinks;
}

/** resetTakesThinking forgets the answer, for tests. */
export function resetTakesThinking(): void {
  thinks = undefined;
}

/**
 * invocation is how a run is called: the arguments, the directory it runs in, and the
 * environment it runs with. Separated from the spawn so it can be asserted without one.
 *
 * The directory is the workspace: opencode 2 has no `--dir`, and the run's working directory
 * is the location — the project — its session works in. The server already holds the config;
 * the run needs only the server's address and password.
 */
export function invocation(opts: {
  server: Pick<Server, "url" | "password">;
  workdir: string;
  providerID: string;
  model: string;
  effort?: string;
  instruction: string;
  sessionID?: string;
  env: NodeJS.ProcessEnv;
  /** thinking asks for reasoning blocks, which the chat draws. See takesThinking. */
  thinking?: boolean;
}): { argv: string[]; cwd: string; env: NodeJS.ProcessEnv } {
  // A reasoning effort is the model's variant, named after a `#`.
  const model = `${opts.providerID}/${opts.model}${opts.effort ? `#${opts.effort}` : ""}`;
  const argv = ["run", "--server", opts.server.url, "--format", "json", "--agent", AgentName, "--auto", "--model", model];
  if (opts.thinking) {
    // Reasoning blocks are off by default outside interactive mode. The chat shows them.
    argv.push("--thinking");
  }
  if (opts.sessionID) {
    argv.push("--session", opts.sessionID);
  }
  argv.push(opts.instruction);

  return {
    argv,
    cwd: opts.workdir,
    env: { ...opts.env, OPENCODE_SERVER_PASSWORD: opts.server.password },
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

/**
 * ModelTurnResume is the user turn that resumes a session Gemini refused. Gemini rejects a
 * request whose last turn is the model's ("Requests ending with a model turn are not
 * supported"), and opencode 1.x sends one after a step that ended with only reasoning, or
 * when it retries an empty step (anomalyco/opencode#47034, fixed in opencode 2, kept here in
 * case it comes back). The session is intact, so resuming it with a turn of the user's own
 * is enough.
 */
export const ModelTurnResume = "Continue.";

/** MaxModelTurnResumes bounds how often one turn resumes a session Gemini refused. */
export const MaxModelTurnResumes = 2;

/** endedOnModelTurn reports whether a harness error is Gemini refusing a model-turn tail. */
export function endedOnModelTurn(reason: string | undefined): boolean {
  return /ending with a model turn/i.test(reason ?? "");
}

/**
 * NetworkResume is the user turn that resumes a session whose request to the model was lost
 * to the network. The harness does not retry a connection that failed before the provider
 * answered (it reports it as an UnknownError, not a retryable APIError), so one dropped TLS
 * handshake would otherwise throw away every step the turn had already taken.
 */
export const NetworkResume =
  "Your last request to the model was lost to a network error. Continue where you left off.";

/** MaxNetworkResumes bounds how often one turn goes on after a network error. */
export const MaxNetworkResumes = 2;

/** NetworkResumeDelayMs is how long to wait before each attempt, by attempt. */
export const NetworkResumeDelayMs = [2_000, 10_000];

// The codes Node and Bun give a connection that failed or dropped, and the phrases they use
// when no code comes with it. An error from the provider itself (a 4xx, a refusal) is not one.
const networkError =
  /\b(EPROTO|ECONNRESET|ECONNREFUSED|ECONNABORTED|ETIMEDOUT|ENETUNREACH|EHOSTUNREACH|EAI_AGAIN|EPIPE|UND_ERR_SOCKET|UND_ERR_CONNECT_TIMEOUT)\b|TLS handshake|socket hang up|socket connection was closed unexpectedly|other side closed|fetch failed|Unable to connect/i;

/** lostToNetwork reports whether a harness error is a connection lost before the model answered. */
export function lostToNetwork(reason: string | undefined): boolean {
  return networkError.test(reason ?? "");
}

/** errorOf is the reason an error event gives, or undefined for any other event. */
export function errorOf(event: Event): string | undefined {
  if (event.type !== "error") {
    return undefined;
  }
  const e = event.error;
  const why = (e?.message ?? e?.data?.message ?? e?.name ?? e?.type ?? "").trim();
  return why === "" ? "an unnamed error" : why;
}
