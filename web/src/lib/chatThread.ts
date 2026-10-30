import {
  fromThreadMessageLike,
  type ThreadMessage,
  type ThreadMessageLike,
  type ToolCallMessagePart,
} from "@assistant-ui/react";
import type { ChatAttachment, ChatMessage } from "../gen/podium/agent/v1/agent_pb";
import { parseActivity } from "./activity";
import { toDate } from "./format";

export { parseActivity, type Activity } from "./activity";

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
 * Until it answers they stay in the block it started in, however many questions later: it
 * is one piece of work. Its answer is read where the conversation is, and only a task that
 * speaks again after answering — a follow-up — opens a new block where that happens.
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

/** TaskBlock is one block of a task: the index of its part in its message, and its trail. */
interface TaskBlock {
  index: number;
  parts: Part[];
  answered: boolean;
  /** continued is set when the task opened a later block after this one answered. */
  continued: boolean;
}

interface Assistant {
  id: string;
  createdAt?: Date;
  parts: Part[];
  /** tasks maps a task id to its block in this message. */
  tasks: Map<string, TaskBlock>;
  attachments: ChatAttachment[];
}

export function toThread({ messages, busy, taskRunning, pendingUser }: ThreadInput): ThreadMessageLike[] {
  // Messages are finished at the end, not as they close: a task's later rows can still
  // land in a block an earlier message holds.
  const out: Array<ThreadMessageLike | Assistant> = [];
  // open is each task's block that has not answered yet, wherever it is.
  const open = new Map<string, TaskBlock>();
  // last is each task's latest block, so a follow-up after an answer can mark it continued.
  const last = new Map<string, TaskBlock>();

  let current: Assistant | undefined;
  const close = () => {
    if (!current) return;
    out.push(current);
    current = undefined;
  };

  for (const m of messages) {
    if (m.role === "user") {
      close();
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

    let task = open.get(m.taskId) ?? current.tasks.get(m.taskId);
    if (!task) {
      const before = last.get(m.taskId);
      if (before) before.continued = true;
      task = { index: current.parts.length, parts: [], answered: false, continued: false };
      current.tasks.set(m.taskId, task);
      current.parts.push({ type: "tool-call", toolName: TASK_TOOL, toolCallId: `task:${m.taskId}:${m.seq}` });
      last.set(m.taskId, task);
    }
    if (m.role === "assistant") {
      // The task's answer is read where the assistant's would be: in the turn it lands in,
      // after its block when the block is there too.
      task.answered = true;
      open.delete(m.taskId);
      current.parts.push({ type: "text", text: m.text });
      continue;
    }
    open.set(m.taskId, task);
    const part = rowPart(m);
    if (part) task.parts.push(part);
  }
  close();

  const live = busy || taskRunning;
  const thread = out.map((m) => ("role" in m ? m : finish(m, live)));

  if (pendingUser !== undefined) {
    thread.push({ id: "pending-user", role: "user", content: pendingUser });
  }
  const tail = thread[thread.length - 1];
  if (busy && tail?.role !== "assistant") {
    // The turn has started and said nothing yet: an empty running message is where the
    // answer will land, and what the view draws its waiting indicator in.
    thread.push({ id: "pending-assistant", role: "assistant", content: [], status: { type: "running" } });
  } else if (busy && tail?.role === "assistant" && tail.status?.type !== "running") {
    thread[thread.length - 1] = { ...tail, status: { type: "running" } };
  }
  return thread;
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

function finish(a: Assistant, live: boolean): ThreadMessageLike {
  let running = false;
  const parts = a.parts.slice();
  for (const [taskId, t] of a.tasks) {
    const done = t.answered || t.continued;
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
