// The MCP server a host turn delegates through.
//
// It is a second entrypoint of this same runtime (dist/mcp.js), spawned by the harness as a
// local MCP server — the same shape as the browser's chrome-devtools-mcp, and for the same
// reason: the thing speaking MCP runs beside the turn, and what it drives is somewhere else.
// Here "somewhere else" is the conductor, over loopback (delegate.ts).
//
// The protocol is implemented here rather than taken from a package. MCP's stdio transport
// is newline-delimited JSON-RPC 2.0 and the surface a tool server needs is four methods —
// initialize, tools/list, tools/call and ping — so a dependency would have cost this image
// ninety packages to save a hundred lines, in a runtime that deliberately carries one.
//
// Nothing here interprets a tool result. The conductor decides what a turn may delegate to
// and refuses anything else; this hands the model's words over and hands the answer back.

import { createInterface } from "node:readline";

import { Client, DelegateError, type Delegation } from "./delegate.js";

/** ProtocolVersion is what this server speaks when a client asks for nothing else. MCP
 * negotiates by date; a client that names a version it can speak gets its own echoed back,
 * which is what the specification asks a server to do for a version it supports. */
export const ProtocolVersion = "2025-06-18";

/** SupportedProtocols are the versions this server will echo. Anything else gets
 * ProtocolVersion, and the client decides whether it can live with that — which is what
 * opencode 1.18.29 does: it asks for 2025-11-25, is answered with an older one, and carries
 * on to tools/list. The list is what has been seen rather than what is imaginable. */
export const SupportedProtocols = new Set(["2024-11-05", "2025-03-26", "2025-06-18", "2025-11-25"]);

/** ServerName prefixes every tool the harness sees, so it is also what a playbook's tool
 * list and the conductor's fence agree to call these. */
export const ServerName = "podium";

/** UrlFlag is how the conductor's address arrives. The TOKEN does not: it comes from the
 * environment, because an argument is visible in a process list. */
export const UrlFlag = "--url";

/** TokenEnv holds the turn's token. It matches conductor.TurnTokenEnv. */
export const TokenEnv = "PODIUM_TURN_TOKEN";

/** JSON-RPC error codes this server uses. -32602 and -32601 are the specification's; a tool
 * that fails answers with an ordinary result carrying isError, which is what MCP asks for so
 * a model can read the failure rather than the transport swallowing it. */
const InvalidParams = -32602;
const MethodNotFound = -32601;

/** ToolDefinition is one tool as tools/list reports it. */
interface ToolDefinition {
  name: string;
  description: string;
  inputSchema: Record<string, unknown>;
}

/** tools is the whole surface. The descriptions are written for the model that reads them:
 * each one says when to reach for the tool and what it costs, because a model choosing
 * between answering directly and starting a container needs to know the difference. */
export const tools: ToolDefinition[] = [
  {
    name: "delegate",
    description:
      "Run work in a Podium task, on a machine with a repository checked out and, depending " +
      "on the playbook, a Docker daemon and a browser. Use it for anything you cannot do " +
      "from this conversation: reading or changing a repository, running a build or a test " +
      "suite, driving a browser, or any command at all. Returns immediately with a " +
      "delegation id — the task then runs for minutes or hours, and its progress and its " +
      "answer appear in this conversation as they happen, so do not repeat them. Poll " +
      "check_delegation for the outcome. The instruction is the ONLY thing the task is told " +
      "beyond this conversation's transcript, so write it as a complete brief.",
    inputSchema: {
      type: "object",
      properties: {
        playbook: {
          type: "string",
          description: "One of the playbooks your brief listed under delegation.playbooks.",
        },
        instruction: {
          type: "string",
          description: "The complete brief for the task: what to do, and how you will know it worked.",
        },
      },
      required: ["playbook", "instruction"],
      additionalProperties: false,
    },
  },
  {
    name: "check_delegation",
    description:
      "Where a delegated task has got to, and its answer once it has one. status is " +
      "running, succeeded, failed, lost, cancelled or timeout. While it is running you also " +
      "get the last thing the task said, which is how you tell work from a hang. A task can " +
      "take a long time: poll rather than waiting, and tell the person what it is doing.",
    inputSchema: {
      type: "object",
      properties: { delegation_id: { type: "string" } },
      required: ["delegation_id"],
      additionalProperties: false,
    },
  },
  {
    name: "list_delegations",
    description: "Every task you have delegated in this turn, oldest first, with their status.",
    inputSchema: { type: "object", properties: {}, additionalProperties: false },
  },
  {
    name: "cancel_delegation",
    description:
      "Stop a delegated task. Use it when the work is no longer wanted or is plainly stuck — " +
      "a task nobody is waiting for still holds a machine and still costs money.",
    inputSchema: {
      type: "object",
      properties: {
        delegation_id: { type: "string" },
        reason: { type: "string", description: "Why, for the operator's log. Optional." },
      },
      required: ["delegation_id"],
      additionalProperties: false,
    },
  },
];

/** Handler answers one request. It is separated from the transport so the tools can be
 * tested without a pipe, and the transport without a conductor. */
export type Handler = (method: string, params: Record<string, unknown>) => Promise<unknown>;

/** rpcError is a JSON-RPC failure with a code, as opposed to a tool that ran and failed. */
export class RPCError extends Error {
  readonly code: number;

  constructor(code: number, message: string) {
    super(message);
    this.name = "RPCError";
    this.code = code;
  }
}

/** newHandler is the server's logic: the four methods, over one conductor client. */
export function newHandler(client: Client): Handler {
  return async (method, params) => {
    switch (method) {
      case "initialize": {
        const asked = typeof params.protocolVersion === "string" ? params.protocolVersion : "";
        return {
          protocolVersion: SupportedProtocols.has(asked) ? asked : ProtocolVersion,
          capabilities: { tools: {} },
          serverInfo: { name: ServerName, version: "1" },
        };
      }
      case "ping":
        return {};
      case "tools/list":
        return { tools };
      case "tools/call":
        return callTool(client, params);
      default:
        throw new RPCError(MethodNotFound, `unknown method ${method}`);
    }
  };
}

/** callTool runs one tool and shapes the answer MCP's way: text content, and isError for a
 * tool that failed rather than a transport error, so the model reads the reason and can
 * correct itself. */
async function callTool(client: Client, params: Record<string, unknown>): Promise<unknown> {
  const name = typeof params.name === "string" ? params.name : "";
  const args = (params.arguments ?? {}) as Record<string, unknown>;
  try {
    switch (name) {
      case "delegate": {
        const playbook = requireString(args, "playbook");
        const instruction = requireString(args, "instruction");
        const { delegation } = await client.delegate(playbook, instruction);
        return ok(
          `Delegated to \`${delegation.playbook}\`.\n` +
            `delegation_id: ${delegation.id}\ntask: ${delegation.task_id ?? "(starting)"}\n\n` +
            "Its progress and its answer go into this conversation on their own. Poll " +
            "check_delegation for the outcome, and tell the person what it is doing rather " +
            "than waiting in silence.",
        );
      }
      case "check_delegation": {
        const id = requireString(args, "delegation_id");
        const state = await client.get(id);
        return ok(describe(state.delegation, state.progress));
      }
      case "list_delegations": {
        const { delegations } = await client.list();
        if (!delegations || delegations.length === 0) {
          return ok("You have delegated nothing in this turn.");
        }
        return ok(delegations.map((d) => describe(d)).join("\n\n"));
      }
      case "cancel_delegation": {
        const id = requireString(args, "delegation_id");
        const reason = typeof args.reason === "string" ? args.reason : "";
        const { delegation } = await client.cancel(id, reason);
        return ok(`Asked the node to stop ${delegation.id} (task ${delegation.task_id ?? "?"}).`);
      }
      default:
        throw new RPCError(InvalidParams, `unknown tool ${name}`);
    }
  } catch (err) {
    if (err instanceof RPCError) {
      throw err;
    }
    if (err instanceof DelegateError) {
      // The conductor's own sentence, handed to the model as a failed tool result: a
      // playbook that was not offered is a mistake the model can fix by picking another.
      return failed(`${err.message} (${err.code})`);
    }
    return failed(err instanceof Error ? err.message : String(err));
  }
}

/** describe is one delegation in the words a model should relay. */
function describe(d: Delegation, progress?: string): string {
  const lines = [`${d.id} — \`${d.playbook}\` — ${d.status}`];
  if (d.task_id) {
    lines.push(`task: ${d.task_id}`);
  }
  if (d.status === "running" && progress && progress.trim() !== "") {
    lines.push(`last said: ${progress.trim()}`);
  }
  if (d.final_text && d.final_text.trim() !== "") {
    lines.push("", d.final_text.trim());
  }
  return lines.join("\n");
}

function ok(text: string): unknown {
  return { content: [{ type: "text", text }] };
}

function failed(text: string): unknown {
  return { content: [{ type: "text", text }], isError: true };
}

function requireString(args: Record<string, unknown>, key: string): string {
  const value = args[key];
  if (typeof value !== "string" || value.trim() === "") {
    throw new RPCError(InvalidParams, `${key} is required`);
  }
  return value;
}

/** serve reads newline-delimited JSON-RPC from `input` and writes answers to `write`.
 *
 * Two rules from the specification carry the weight here. A NOTIFICATION — a message with no
 * id, which is how a client says "initialized" — is answered with nothing at all, and a
 * server that replies to one breaks the client. And a line that is not JSON is not a reason
 * to exit: the transport is a pipe shared with whatever else the harness writes, so an
 * unreadable line is dropped and the next one is read. */
export async function serve(
  input: NodeJS.ReadableStream,
  write: (line: string) => void,
  handler: Handler,
  onWarn: (message: string) => void = () => {},
): Promise<void> {
  const rl = createInterface({ input, crlfDelay: Infinity });
  for await (const line of rl) {
    if (line.trim() === "") {
      continue;
    }
    let msg: { id?: unknown; method?: unknown; params?: unknown };
    try {
      msg = JSON.parse(line) as typeof msg;
    } catch (err) {
      onWarn(`undecodable message: ${err instanceof Error ? err.message : String(err)}`);
      continue;
    }
    const id = msg.id;
    const method = typeof msg.method === "string" ? msg.method : "";
    const params = (msg.params ?? {}) as Record<string, unknown>;
    if (id === undefined || id === null) {
      // A notification. Answering it is a protocol error, so this is deliberate silence.
      continue;
    }
    try {
      const result = await handler(method, params);
      write(JSON.stringify({ jsonrpc: "2.0", id, result }));
    } catch (err) {
      const code = err instanceof RPCError ? err.code : InvalidParams;
      const message = err instanceof Error ? err.message : String(err);
      write(JSON.stringify({ jsonrpc: "2.0", id, error: { code, message } }));
    }
  }
}

/** urlFrom reads --url out of an argument list. */
export function urlFrom(argv: string[]): string {
  const i = argv.indexOf(UrlFlag);
  if (i >= 0 && i + 1 < argv.length) {
    return argv[i + 1] ?? "";
  }
  return "";
}

/** main is the entrypoint. It refuses to start without an address and a token: a tool server
 * that answers every call with "not configured" is worse than a harness that reports a
 * server it could not start. */
async function main(): Promise<number> {
  const url = urlFrom(process.argv.slice(2));
  const token = process.env[TokenEnv] ?? "";
  if (url === "" || token === "") {
    process.stderr.write(
      `podium-mcp: needs ${UrlFlag} and ${TokenEnv}; this server is started by the agent ` +
        "runtime and is not meant to be run by hand\n",
    );
    return 2;
  }
  await serve(
    process.stdin,
    (line) => process.stdout.write(line + "\n"),
    newHandler(new Client(url, token)),
    (message) => process.stderr.write(`podium-mcp: ${message}\n`),
  );
  return 0;
}

// Only when run as a program. A test importing this module is not argv[1], so it gets the
// exports and no process of its own.
if (import.meta.url === `file://${process.argv[1]}`) {
  process.exit(await main());
}
