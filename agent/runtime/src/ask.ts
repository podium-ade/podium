// The MCP server an interactive turn asks a human through.
//
// One tool, `ask`: post the question into the conversation and wait on the inbox socket
// for the reply. The conductor is what delivers the reply — Inject on the node stream —
// so this process only reads. Off unless the brief said interactive: a playbook that did
// not opt in must not grow a wait.

import { createConnection, type Socket } from "node:net";
import { fileURLToPath } from "node:url";

import { emitMessage, runnerInvoke, type RunnerInvoke } from "./emit.js";
import { HumanServer } from "./opencode.js";
import { RPCError, serve, type Handler } from "./mcp.js";

/** InboxSockEnv is where the node bind-mounts the inbox, and DefaultInboxSock is that
 * path inside the container. */
export const InboxSockEnv = "PODIUM_INBOX_SOCK";
export const DefaultInboxSock = "/podium/inbox.sock";

/** AwaitTimeoutMs is how long one ask waits. Waiting still counts against the playbook
 * timeout; this is the shorter cap so a forgotten question does not pin a node for the
 * whole 30m. */
export const AwaitTimeoutMs = 10 * 60 * 1000;

const InvalidParams = -32602;
const MethodNotFound = -32601;

const tools = [
  {
    name: "ask",
    description:
      "Ask the human a question and wait for their reply in this same turn. Use it when " +
      "you need a decision, a credential you do not have, or a clarification that would " +
      "change what you do next. The question is posted into the conversation; the reply " +
      "comes back as this tool's result. Do not end the turn with a question — call this " +
      "instead. Waiting still counts against this playbook's timeout.",
    inputSchema: {
      type: "object",
      properties: {
        question: {
          type: "string",
          description: "The question, written for the person in the conversation.",
        },
      },
      required: ["question"],
      additionalProperties: false,
    },
  },
];

export function askEntrypoint(here = import.meta.url): string {
  return fileURLToPath(new URL("./ask.js", here));
}

export function inboxSock(env: NodeJS.ProcessEnv = process.env): string {
  const override = env[InboxSockEnv];
  return override === undefined || override === "" ? DefaultInboxSock : override;
}

/** WaitForReply reads one inbox line. Exported so a test can drive it without a socket. */
export type WaitForReply = (timeoutMs: number, signal?: AbortSignal) => Promise<string>;

export function newHandler(
  emit: RunnerInvoke,
  wait: WaitForReply,
  timeoutMs = AwaitTimeoutMs,
): Handler {
  return async (method, params) => {
    switch (method) {
      case "initialize":
        return {
          protocolVersion: "2025-06-18",
          capabilities: { tools: {} },
          serverInfo: { name: HumanServer, version: "1" },
        };
      case "ping":
        return {};
      case "tools/list":
        return { tools };
      case "tools/call":
        return callTool(emit, wait, timeoutMs, params);
      default:
        throw new RPCError(MethodNotFound, `unknown method ${method}`);
    }
  };
}

async function callTool(
  emit: RunnerInvoke,
  wait: WaitForReply,
  timeoutMs: number,
  params: Record<string, unknown>,
): Promise<unknown> {
  const name = typeof params.name === "string" ? params.name : "";
  const args = (params.arguments ?? {}) as Record<string, unknown>;
  if (name !== "ask") {
    throw new RPCError(InvalidParams, `unknown tool ${name}`);
  }
  const question = typeof args.question === "string" ? args.question.trim() : "";
  if (question === "") {
    throw new RPCError(InvalidParams, "question is required");
  }
  try {
    await emitMessage("question", question, [], emit);
    const reply = await wait(timeoutMs);
    return { content: [{ type: "text", text: reply }] };
  } catch (err) {
    const message = err instanceof Error ? err.message : String(err);
    return { content: [{ type: "text", text: message }], isError: true };
  }
}

/** waitOnSocket dials the inbox and reads one JSON line `{v, text}`. */
export function waitOnSocket(path: string): WaitForReply {
  return (timeoutMs, signal) =>
    new Promise<string>((resolve, reject) => {
      const sock: Socket = createConnection(path);
      let buf = "";
      const timer = setTimeout(() => {
        sock.destroy();
        reject(new Error(`nobody answered in ${Math.round(timeoutMs / 1000)}s; continue without it or ask again`));
      }, timeoutMs);
      const onAbort = () => {
        clearTimeout(timer);
        sock.destroy();
        reject(new Error("the turn was cancelled while waiting for a reply"));
      };
      signal?.addEventListener("abort", onAbort, { once: true });
      sock.setEncoding("utf8");
      sock.on("data", (chunk: string) => {
        buf += chunk;
        const nl = buf.indexOf("\n");
        if (nl < 0) {
          return;
        }
        clearTimeout(timer);
        signal?.removeEventListener("abort", onAbort);
        sock.end();
        const line = buf.slice(0, nl).trim();
        try {
          const parsed = JSON.parse(line) as { text?: unknown };
          if (typeof parsed.text !== "string" || parsed.text.trim() === "") {
            reject(new Error("the inbox delivered an empty reply"));
            return;
          }
          resolve(parsed.text);
        } catch (err) {
          reject(err instanceof Error ? err : new Error(String(err)));
        }
      });
      sock.on("error", (err) => {
        clearTimeout(timer);
        signal?.removeEventListener("abort", onAbort);
        reject(err);
      });
      sock.on("end", () => {
        if (buf.trim() === "") {
          clearTimeout(timer);
          signal?.removeEventListener("abort", onAbort);
          reject(new Error("the inbox closed before a reply arrived"));
        }
      });
    });
}

async function main(): Promise<number> {
  await serve(
    process.stdin,
    (line) => process.stdout.write(line + "\n"),
    newHandler(runnerInvoke(), waitOnSocket(inboxSock())),
    (message) => process.stderr.write(`podium-ask: ${message}\n`),
  );
  return 0;
}

if (import.meta.url === `file://${process.argv[1]}`) {
  process.exit(await main());
}
