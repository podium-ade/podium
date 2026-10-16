// The MCP server a first chat turn names the conversation through.
//
// A host turn has no `write` tool. Giving it one would be a window onto the conductor's
// machine: there is no container. The prompt used to ask that turn to write chat-title.txt
// anyway, and the model answered that it had no file tool and then never named the chat.
//
// This server writes that one file and nothing else. The path comes from the runtime, not
// from the model. The conductor already reads the file when the turn finishes
// (chat-title.txt, host jail or task artifacts) and applies it only while the chat is still
// auto-titled.

import { writeFileSync } from "node:fs";
import { fileURLToPath } from "node:url";

import { ChatTitleName } from "./artifacts.js";
import type { TurnBrief } from "./brief.js";
import { RPCError, serve, type Handler } from "./mcp.js";

/** TitleServer prefixes the tool the harness shows the model (`title_set`). */
export const TitleServer = "title";

/** TitleTool is the tool name on this server, before the harness adds the prefix. */
export const TitleTool = "set";

/** TitleFileEnv is the path the runtime wants the title written to. The model never
 * chooses it. Unset means the server refuses: a tool that guessed a path would be worse. */
export const TitleFileEnv = "PODIUM_CHAT_TITLE_FILE";

/** MaxTitleRunes matches chat.MaxTitleRunes. The conductor clamps again; this stops a
 * runaway tool argument becoming the file. */
export const MaxTitleRunes = 48;

const InvalidParams = -32602;
const MethodNotFound = -32601;

const tools = [
  {
    name: TitleTool,
    description:
      "Name this conversation. Call it once, on this first turn, with a 3–6 word title: " +
      "one line, no quotes, no trailing punctuation. Do not mention the title or this tool " +
      "in your answer to the person.",
    inputSchema: {
      type: "object",
      properties: {
        title: {
          type: "string",
          description: "The conversation's name. 3–6 words, one line, no quotes.",
        },
      },
      required: ["title"],
      additionalProperties: false,
    },
  },
];

export function titleEntrypoint(here = import.meta.url): string {
  return fileURLToPath(new URL("./title.js", here));
}

/**
 * titleByTool is a first turn of a listed conversation whose tool list cannot write a file.
 * A task that has `write` still writes chat-title.txt itself. A host turn never has `write`.
 */
export function titleByTool(brief: TurnBrief): boolean {
  const kind = brief.source.kind;
  if (kind !== "chat" && kind !== "slack" && kind !== "github") {
    return false;
  }
  if (brief.transcript.some((entry) => entry.role === "assistant")) {
    return false;
  }
  return !brief.playbook.allowed_tools.includes("write");
}

/** sanitizeTitle is the line that is safe to store. Empty means the model sent nothing
 * usable. It follows chat.SanitizeTitle closely enough that the file and the row agree. */
export function sanitizeTitle(raw: string): string {
  let s = raw.trim();
  const nl = s.indexOf("\n");
  if (nl >= 0) {
    s = s.slice(0, nl).trim();
  }
  s = Array.from(s)
    .filter((ch) => {
      const c = ch.codePointAt(0) ?? 0;
      return c > 0x1f && c !== 0x7f;
    })
    .join("");
  s = s.trim().replace(/^["'“”‘’]+|["'“”‘’]+$/g, "");
  s = s.trim().replace(/[.,;:!?]+$/g, "");
  s = s.split(/\s+/).filter((part) => part !== "").join(" ");
  if (s === "" || s.toLowerCase() === "new chat") {
    return "";
  }
  const runes = Array.from(s);
  if (runes.length <= MaxTitleRunes) {
    return s;
  }
  let cut = runes.slice(0, MaxTitleRunes);
  const text = cut.join("");
  const space = text.lastIndexOf(" ");
  if (space >= MaxTitleRunes / 2) {
    cut = Array.from(text.slice(0, space));
  }
  return cut.join("").trim();
}

export function newHandler(file = process.env[TitleFileEnv] ?? ""): Handler {
  return async (method, params) => {
    switch (method) {
      case "initialize":
        return {
          protocolVersion: "2025-06-18",
          capabilities: { tools: {} },
          serverInfo: { name: TitleServer, version: "1" },
        };
      case "ping":
        return {};
      case "tools/list":
        return { tools };
      case "tools/call":
        return callTool(file, params);
      default:
        throw new RPCError(MethodNotFound, `unknown method ${method}`);
    }
  };
}

function callTool(file: string, params: Record<string, unknown>): unknown {
  const name = typeof params.name === "string" ? params.name : "";
  if (name !== TitleTool) {
    throw new RPCError(InvalidParams, `unknown tool ${name}`);
  }
  const args = (params.arguments ?? {}) as Record<string, unknown>;
  const raw = typeof args.title === "string" ? args.title : "";
  const title = sanitizeTitle(raw);
  if (title === "") {
    return {
      content: [{ type: "text", text: "title is empty; send a short phrase" }],
      isError: true,
    };
  }
  if (file === "") {
    return {
      content: [{ type: "text", text: "this turn has nowhere to record a title" }],
      isError: true,
    };
  }
  try {
    // The runtime chose the path. The model chose only the words.
    writeFileSync(file, `${title}\n`, { encoding: "utf8", mode: 0o644 });
  } catch (err) {
    const message = err instanceof Error ? err.message : String(err);
    return { content: [{ type: "text", text: message }], isError: true };
  }
  return {
    content: [
      {
        type: "text",
        text: `Named this conversation "${title}". Do not mention the title in your answer.`,
      },
    ],
  };
}

/** chatTitlePath is the file the conductor reads, under the turn's artifacts directory. */
export function chatTitlePath(artifactsDir: string): string {
  return `${artifactsDir}/${ChatTitleName}`;
}

async function main(): Promise<number> {
  await serve(
    process.stdin,
    (line) => process.stdout.write(line + "\n"),
    newHandler(),
    (message) => process.stderr.write(`podium-title: ${message}\n`),
  );
  return 0;
}

if (import.meta.url === `file://${process.argv[1]}`) {
  process.exit(await main());
}
