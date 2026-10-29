import { describe, expect, it } from "vitest";
import { pretty, toolSummary } from "./activity";

describe("toolSummary", () => {
  it("names what each tool was pointed at", () => {
    expect(toolSummary({ kind: "tool", tool: "bash", input: '{"command":"ls -la"}', title: "List" })).toBe("ls -la");
    expect(toolSummary({ kind: "tool", tool: "read", input: '{"filePath":"/w/a.go"}' })).toBe("/w/a.go");
    expect(toolSummary({ kind: "tool", tool: "grep", input: '{"pattern":"TODO","path":"."}' })).toBe("TODO");
  });

  it("puts a multi-line command on one line", () => {
    expect(toolSummary({ kind: "tool", tool: "bash", input: JSON.stringify({ command: "a\n  b" }) })).toBe("a ⏎ b");
  });

  it("falls back to the title, then the raw input", () => {
    expect(toolSummary({ kind: "tool", tool: "todowrite", title: "3 todos", input: "{}" })).toBe("3 todos");
    expect(toolSummary({ kind: "tool", tool: "mystery", input: "raw" })).toBe("raw");
    expect(toolSummary({ kind: "tool", tool: "bash", input: "not json" })).toBe("not json");
  });
});

describe("pretty", () => {
  it("indents JSON and leaves anything else alone", () => {
    expect(pretty('{"a":1}')).toBe('{\n  "a": 1\n}');
    expect(pretty("plain")).toBe("plain");
    expect(pretty("")).toBe("");
  });
});
