import { describe, expect, it } from "vitest";
import { create } from "@bufbuild/protobuf";
import { ChatMessageSchema, type ChatMessage } from "../gen/podium/agent/v1/agent_pb";
import { TASK_TOOL, parseActivity, toThread } from "./chatThread";

let seq = 0n;
function row(role: string, text: string, taskId = ""): ChatMessage {
  seq += 1n;
  return create(ChatMessageSchema, { seq, role, text, taskId });
}

const tool = (name: string, extra: Record<string, string> = {}) =>
  JSON.stringify({ kind: "tool", tool: name, status: "completed", ...extra });

type Parts = Array<Record<string, unknown>>;
const partsOf = (m: { content: unknown }) => m.content as Parts;

describe("parseActivity", () => {
  it("reads a tool call and a thought, and nothing else", () => {
    expect(parseActivity(tool("bash", { title: "go test" }))).toMatchObject({ kind: "tool", tool: "bash", title: "go test" });
    expect(parseActivity('{"kind":"reasoning","text":"hm"}')).toEqual({ kind: "reasoning", text: "hm" });
    expect(parseActivity("not json")).toBeUndefined();
    expect(parseActivity('{"kind":"tool"}')).toBeUndefined();
    expect(parseActivity('{"kind":"tool","tool":{"x":1}}')).toBeUndefined();
    expect(parseActivity("null")).toBeUndefined();
  });
});

describe("toThread", () => {
  it("folds everything between two questions into one assistant message", () => {
    const thread = toThread({
      messages: [
        row("user", "why did the ETL fail?"),
        row("progress", "Looking at the last run"),
        row("activity", tool("read", { input: '{"path":"etl.log"}', output: "boom" })),
        row("assistant", "It ran out of disk."),
        row("user", "thanks"),
      ],
      busy: false,
      taskRunning: false,
    });

    expect(thread.map((m) => m.role)).toEqual(["user", "assistant", "user"]);
    const parts = partsOf(thread[1]);
    expect(parts.map((p) => p.type)).toEqual(["reasoning", "tool-call", "text"]);
    expect(parts[1]).toMatchObject({ toolName: "read", args: { path: "etl.log" }, result: "boom", isError: false });
    expect(thread[1].status).toEqual({ type: "complete", reason: "stop" });
  });

  it("draws a delegated task as a subagent holding its own trail, and its answer in the turn", () => {
    const thread = toThread({
      messages: [
        row("user", "fix the flaky test"),
        row("assistant", "I'll start a task for that."),
        row("progress", "Working on this in a `podium` task", "task_1"),
        row("activity", tool("bash", { title: "go test ./..." }), "task_1"),
        row("activity", '{"kind":"reasoning","text":"the race is in the cache"}', "task_1"),
        row("assistant", "Fixed in #12.", "task_1"),
      ],
      busy: false,
      taskRunning: false,
    });

    const parts = partsOf(thread[1]);
    expect(parts.map((p) => p.type)).toEqual(["text", "tool-call", "text"]);
    expect(parts[1]).toMatchObject({ toolName: TASK_TOOL, args: { taskId: "task_1" }, result: "complete" });
    expect(parts[2]).toMatchObject({ text: "Fixed in #12." });

    const nested = (parts[1].messages as Array<{ content: Parts; status: unknown }>)[0];
    expect(nested.content.map((p) => p.type)).toEqual(["reasoning", "tool-call", "reasoning"]);
    expect(nested.status).toEqual({ type: "complete", reason: "stop" });
  });

  it("keeps a task running, and its turn with it, until it answers", () => {
    const thread = toThread({
      messages: [row("user", "deploy"), row("activity", tool("bash"), "task_2")],
      busy: false,
      taskRunning: true,
    });
    const block = partsOf(thread[1])[0];
    expect(block.result).toBeUndefined();
    expect(thread[1].status).toEqual({ type: "running" });
  });

  it("marks a task that stopped without answering as incomplete", () => {
    const thread = toThread({
      messages: [row("user", "deploy"), row("activity", tool("bash"), "task_3")],
      busy: false,
      taskRunning: false,
    });
    expect(partsOf(thread[1])[0]).toMatchObject({ result: "incomplete", isError: true });
  });

  it("opens a second block when a task keeps talking after the next question", () => {
    const thread = toThread({
      messages: [
        row("user", "a"),
        row("activity", tool("bash"), "task_4"),
        row("user", "b"),
        row("assistant", "done", "task_4"),
      ],
      busy: false,
      taskRunning: false,
    });
    expect(thread.map((m) => m.role)).toEqual(["user", "assistant", "user", "assistant"]);
    expect(partsOf(thread[1])[0]).toMatchObject({ result: "complete" });
    expect(partsOf(thread[3]).map((p) => p.type)).toEqual(["tool-call", "text"]);
  });

  it("adds a running placeholder while a turn has said nothing yet", () => {
    const thread = toThread({ messages: [row("user", "hi")], busy: true, taskRunning: false, pendingUser: "and?" });
    expect(thread.map((m) => m.id)).toEqual([expect.stringMatching(/^u/), "pending-user", "pending-assistant"]);
    expect(thread[2].status).toEqual({ type: "running" });
  });

  it("skips activity it cannot read rather than failing the transcript", () => {
    const thread = toThread({
      messages: [row("user", "hi"), row("activity", "{nope"), row("assistant", "hello")],
      busy: false,
      taskRunning: false,
    });
    expect(partsOf(thread[1]).map((p) => p.type)).toEqual(["text"]);
  });

  it("gathers the turn's attachments and a mirrored question's author", () => {
    const answer = row("assistant", "here");
    answer.attachments = [{ artifactId: "art_1", name: "report.csv" } as never];
    const q = row("user", "report please");
    q.author = "alice";
    const thread = toThread({ messages: [q, answer], busy: false, taskRunning: false });
    expect(thread[0].metadata?.custom).toMatchObject({ author: "alice" });
    expect(thread[1].metadata?.custom).toMatchObject({ attachments: [{ name: "report.csv" }] });
  });
});
