import { readFileSync } from "node:fs";

import { describe, expect, it } from "vitest";

import { BriefEnv } from "./brief.js";
import { codeMode, codeModeBlock, codeModePath, render } from "./render.js";

function goldenEnv(patch: Record<string, unknown> = {}): NodeJS.ProcessEnv {
  const brief = { ...JSON.parse(readFileSync(new URL("../testdata/brief.example.json", import.meta.url), "utf8")), ...patch };
  return { [BriefEnv]: Buffer.from(JSON.stringify(brief), "utf8").toString("base64") };
}

describe("codeMode", () => {
  it("rewrites the chat host's MCP tool names to their code-mode paths", () => {
    const out = codeMode("Call `podium_delegate`, then `podium_check_delegation`; name it with `title_set`.");
    expect(out).toBe(
      "Call `tools.podium.delegate`, then `tools.podium.check_delegation`; name it with `tools.title.set`.",
    );
  });

  it("rewrites the memory tools and leaves other names alone", () => {
    expect(codeMode("use mcp__memory__recall, not my_tool or podiumx_y")).toBe(
      "use tools.memory.recall, not my_tool or podiumx_y",
    );
  });

  it("brackets a server whose name is not an identifier", () => {
    expect(codeModePath("pull-request", "diff")).toBe('tools["pull-request"].diff');
    expect(codeModePath("podium", "delegate")).toBe("tools.podium.delegate");
  });
});

describe("render", () => {
  it("is the runtime's own prompt in code-mode form, with the block that says how", () => {
    const delegation = { url: "http://127.0.0.1:8090", token_env: "T", playbooks: [{ name: "coder" }] };
    const prompt = render(goldenEnv({ runs_on: "host", delegation }));
    expect(prompt).toContain("tools.podium.delegate");
    expect(prompt).not.toMatch(/\bpodium_delegate\b/);
    expect(prompt.endsWith(codeModeBlock())).toBe(true);
  });

  it("refuses an unusable brief", () => {
    expect(() => render({})).toThrow(/PODIUM_AGENT_TURN is not set/);
  });
});
