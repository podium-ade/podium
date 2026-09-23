import { describe, expect, it } from "vitest";

import { MaxFieldChars, activityOf } from "./activity.js";

describe("activityOf", () => {
  it("reads a finished tool call", () => {
    const a = activityOf({
      type: "tool_use",
      part: {
        type: "tool",
        callID: "call_1",
        tool: "bash",
        state: { status: "completed", title: "go test ./...", input: { command: "go test ./..." }, output: "ok" },
      },
    });
    expect(a).toEqual({
      kind: "tool",
      id: "call_1",
      tool: "bash",
      status: "completed",
      title: "go test ./...",
      input: '{"command":"go test ./..."}',
      output: "ok",
    });
  });

  it("carries the error of a failed call and not its output", () => {
    const a = activityOf({
      type: "tool_use",
      part: { tool: "read", id: "prt_1", state: { status: "error", error: "no such file", output: "x" } },
    });
    expect(a).toEqual({ kind: "tool", id: "prt_1", tool: "read", status: "error", error: "no such file" });
  });

  it("reads a reasoning block and skips an empty one", () => {
    expect(activityOf({ type: "reasoning", part: { text: " weighing it \n" } })).toEqual({
      kind: "reasoning",
      text: "weighing it",
    });
    expect(activityOf({ type: "reasoning", part: { text: "  " } })).toBeUndefined();
  });

  it("ignores everything else", () => {
    expect(activityOf({ type: "text", part: { text: "hi" } })).toBeUndefined();
    expect(activityOf({ type: "tool_use", part: {} })).toBeUndefined();
  });

  it("caps a large output so the message stays under the runner's limit", () => {
    const a = activityOf({
      type: "tool_use",
      part: { tool: "bash", state: { status: "completed", output: "x".repeat(100_000) } },
    });
    expect(a?.kind === "tool" && a.output?.length).toBe(MaxFieldChars + 1);
    expect(JSON.stringify(a).length).toBeLessThan(32 * 1024);
  });

  it("cleans every string that leaves", () => {
    const a = activityOf(
      { type: "tool_use", part: { tool: "bash", state: { status: "completed", input: "echo s3cr3t", output: "s3cr3t" } } },
      (s) => s.replaceAll("s3cr3t", "[redacted]"),
    );
    expect(JSON.stringify(a)).not.toContain("s3cr3t");
  });
});
