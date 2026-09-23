import {
  fromThreadMessageLike,
  type ThreadMessage,
  type ThreadMessageLike,
  type ToolCallMessagePart,
} from "@assistant-ui/react";
import type { ChatAttachment, ChatMessage } from "../gen/podium/agent/v1/agent_pb";
import { toDate } from "./format";

/**
 * How a Podium transcript becomes an assistant-ui thread.
 *
 * The transcript is flat rows in seq order: questions, the assistant's own words, and the
 * words of every task it delegated, each row tagged with the task it came from. The thread
 * is nested: everything said between two questions is ONE assistant message, and inside it
 *
 * - the assistant's own thoughts, progress lines and tool calls are reasoning and tool-call
 *   parts, which the view folds into a collapsible trail;
 * - each delegated task is one TASK_TOOL call whose nested `messages` carry that task's own
 *   trail — a subagent, drawn inside the turn that started it;
 * - answers, the assistant's and a task's alike, are text parts, where they are read.
 *
 * A task outlives the turn that started it, so its rows can land after the next question.
 * They then open a second block for the same task in that turn, rather than reaching back
 * into an earlier message.
 */

/** TASK_TOOL is the tool name a delegated task is drawn under. It is Podium's, not the model's. */
export const TASK_TOOL = "podium_task";

export interface TaskArgs {
  taskId: string;
  [key: string]: string;
}

/** Custom metadata the view reads off a converted message. */
export interface MessageCustom {
  author?: string;
  attachments?: ChatAttachment[];
}

/** Activity is the JSON an `activity` row carries (agent/runtime/src/activity.ts). */
export type Activity =
  | {
      kind: "tool";
      id?: string;
      tool: string;
      title?: string;
      status?: string;
      input?: string;
      output?: string;
      error?: string;
    }
  | { kind: "reasoning"; text: string };

/**
 * parseActivity reads an activity row, or undefined for one this build does not understand.
 * The text is a task's and untrusted: only the fields named here are read, only as strings.
 */
export function parseActivity(text: string): Activity | undefined {
  let v: unknown;
  try {
    v = JSON.parse(text);
  } catch {
    return undefined;
  }
  if (typeof v !== "object" || v === null) return undefined;
  const o = v as Record<string, unknown>;
  const str = (k: string) => (typeof o[k] === "string" ? (o[k] as string) : undefined);
  if (o.kind === "reasoning") {
    const t = str("text");
    return t ? { kind: "reasoning", text: t } : undefined;
  }
  if (o.kind === "tool" && str("tool")) {
    return {
      kind: "tool",
      id: str("id"),
      tool: str("tool")!,
      title: str("title"),
      status: str("status"),
      input: str("input"),
      output: str("output"),
      error: str("error"),
    };
  }
  return undefined;
}

export interface ThreadInput {
  messages: readonly ChatMessage[];
  /** busy is true while a turn of this chat is in flight and not waiting on a human. */
  busy: boolean;
  /** taskRunning is true while a task this chat delegated is still running. */
  taskRunning: boolean;
  /** pendingUser is a question sent but not yet on the stream. */
  pendingUser?: string;
}

type Part = Exclude<ThreadMessageLike["content"], string>[number];

interface Assistant {
  id: string;
  createdAt?: Date;
  parts: Part[];
  /** tasks maps a task id to its block's index in parts and the nested parts it holds. */
  tasks: Map<string, { index: number; parts: Part[]; answered: boolean }>;
  attachments: ChatAttachment[];
}

export function toThread({ messages, busy, taskRunning, pendingUser }: ThreadInput): ThreadMessageLike[] {
  const out: ThreadMessageLike[] = [];
  // The last segment each task spoke in: a block with more of its task below is finished
  // as far as that block goes.
  const lastSegmentOf = new Map<string, number>();
  const answered = new Set<string>();
  let segment = 0;
  for (const m of messages) {
    if (m.role === "user") segment++;
    else if (m.taskId !== "") {
      lastSegmentOf.set(m.taskId, segment);
      if (m.role === "assistant") answered.add(m.taskId);
    }
  }

  let current: Assistant | undefined;
  segment = 0;
  const close = () => {
    if (!current) return;
    out.push(finish(current, segment, lastSegmentOf, answered, busy || taskRunning));
    current = undefined;
  };

  for (const m of messages) {
    if (m.role === "user") {
      close();
      segment++;
      out.push({
        id: `u${m.seq}`,
        role: "user",
        content: m.text,
        createdAt: toDate(m.ts),
        metadata: { custom: { author: m.author || undefined, attachments: m.attachments } },
      });
      continue;
    }
    current ??= { id: `a${m.seq}`, createdAt: toDate(m.ts), parts: [], tasks: new Map(), attachments: [] };
    current.attachments.push(...m.attachments);

    if (m.taskId === "") {
      const part = rowPart(m);
      if (part) current.parts.push(part);
      continue;
    }

    let task = current.tasks.get(m.taskId);
    if (!task) {
      task = { index: current.parts.length, parts: [], answered: false };
      current.tasks.set(m.taskId, task);
      current.parts.push({ type: "tool-call", toolName: TASK_TOOL, toolCallId: `task:${m.taskId}:${m.seq}` });
    }
    if (m.role === "assistant") {
      // The task's answer is read where the assistant's would be: after its block, in the turn.
      task.answered = true;
      current.parts.push({ type: "text", text: m.text });
      continue;
    }
    const part = rowPart(m);
    if (part) task.parts.push(part);
  }
  close();

  if (pendingUser !== undefined) {
    out.push({ id: "pending-user", role: "user", content: pendingUser });
  }
  const last = out[out.length - 1];
  if (busy && last?.role !== "assistant") {
    // The turn has started and said nothing yet: an empty running message is where the
    // answer will land, and what the view draws its waiting indicator in.
    out.push({ id: "pending-assistant", role: "assistant", content: [], status: { type: "running" } });
  } else if (busy && last?.role === "assistant" && last.status?.type !== "running") {
    out[out.length - 1] = { ...last, status: { type: "running" } };
  }
  return out;
}

/** rowPart is one non-answer row, or an answer, as a part; undefined for what draws nothing. */
function rowPart(m: ChatMessage): Part | undefined {
  if (m.role === "assistant") return { type: "text", text: m.text };
  if (m.role === "progress") return { type: "reasoning", text: m.text };
  if (m.role !== "activity") return undefined;
  const a = parseActivity(m.text);
  if (!a) return undefined;
  if (a.kind === "reasoning") return { type: "reasoning", text: a.text };
  const failed = a.status === "error";
  return {
    type: "tool-call",
    toolCallId: `t${m.seq}`,
    toolName: a.tool,
    args: argsOf(a.input),
    argsText: a.input ?? "",
    result: (failed ? a.error : a.output) ?? "",
    isError: failed,
    artifact: a.title ? { title: a.title } : undefined,
  };
}

type ReadonlyJSONObject = ToolCallMessagePart["args"];

function argsOf(input?: string): ReadonlyJSONObject {
  if (!input) return {};
  try {
    const v: unknown = JSON.parse(input);
    return typeof v === "object" && v !== null && !Array.isArray(v) ? (v as ReadonlyJSONObject) : {};
  } catch {
    return {};
  }
}

function finish(
  a: Assistant,
  segment: number,
  lastSegmentOf: Map<string, number>,
  answered: Set<string>,
  live: boolean,
): ThreadMessageLike {
  let running = false;
  const parts = a.parts.slice();
  for (const [taskId, t] of a.tasks) {
    const continues = (lastSegmentOf.get(taskId) ?? segment) > segment;
    const done = t.answered || continues || answered.has(taskId);
    const status = done
      ? ({ type: "complete", reason: "stop" } as const)
      : live
        ? ({ type: "running" } as const)
        : ({ type: "incomplete", reason: "other" } as const);
    if (status.type === "running") running = true;
    const nested: ThreadMessage = fromThreadMessageLike(
      { id: `${a.id}:${taskId}`, role: "assistant", content: t.parts, status },
      `${a.id}:${taskId}`,
      status,
    );
    const args: TaskArgs = { taskId };
    parts[t.index] = {
      type: "tool-call",
      toolName: TASK_TOOL,
      toolCallId: (parts[t.index] as { toolCallId: string }).toolCallId,
      args,
      messages: [nested],
      // A result is what says the call is over; a running task has none yet.
      result: status.type === "running" ? undefined : status.type,
      isError: status.type === "incomplete",
    };
  }
  const custom: MessageCustom = { attachments: a.attachments };
  return {
    id: a.id,
    role: "assistant",
    content: parts,
    createdAt: a.createdAt,
    status: running ? { type: "running" } : { type: "complete", reason: "stop" },
    metadata: { custom: custom as Record<string, unknown> },
  };
}
