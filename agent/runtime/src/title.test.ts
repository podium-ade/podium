import { mkdtempSync, readFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

import { describe, expect, it } from "vitest";

import type { TurnBrief } from "./brief.js";
import { newHandler, sanitizeTitle, titleByTool } from "./title.js";

function brief(over: Partial<TurnBrief> = {}): TurnBrief {
  return {
    session_id: "s",
    turn_id: "t",
    instruction: "hi",
    profile: {
      display_name: "Podium",
      model: "grok",
      system_prompt: "",
    },
    playbook: { name: "assistant", system_prompt: "", allowed_tools: ["webfetch", "todoread", "todowrite"] },
    provider: { id: "xai", model: "grok", api_key_env: "K" },
    source: { kind: "chat", ref: "chat_1" },
    transcript: [{ role: "user", author: "ada", ts: "2026-10-14T00:00:00Z", text: "hi" }],
    ...over,
  } as TurnBrief;
}

describe("sanitizeTitle", () => {
  it("keeps a short phrase and drops the wrapping the model adds", () => {
    expect(sanitizeTitle('  "August account totals." \n ignore the rest')).toBe(
      "August account totals",
    );
  });

  it("refuses an empty title and the placeholder", () => {
    expect(sanitizeTitle("   \n")).toBe("");
    expect(sanitizeTitle("New chat")).toBe("");
  });

  it("cuts a long title on a word", () => {
    const title = sanitizeTitle("one two three four five six seven eight nine ten eleven twelve thirteen");
    expect(Array.from(title).length).toBeLessThanOrEqual(48);
    expect(title.endsWith(" ")).toBe(false);
    expect(title.split(" ").length).toBeGreaterThan(2);
  });
});

describe("titleByTool", () => {
  it("is a first host-style turn, and not a later one or a turn that can write", () => {
    expect(titleByTool(brief())).toBe(true);
    expect(
      titleByTool(
        brief({
          transcript: [
            { role: "user", author: "ada", ts: "t", text: "hi" },
            { role: "assistant", author: "Podium", ts: "t", text: "hello" },
          ],
        }),
      ),
    ).toBe(false);
    expect(titleByTool(brief({ source: { kind: "linear", ref: "ENG-1" } }))).toBe(false);
    expect(
      titleByTool(
        brief({
          playbook: {
            name: "task",
            system_prompt: "",
            allowed_tools: ["write", "bash"],
          },
        }),
      ),
    ).toBe(false);
  });
});

describe("title tool", () => {
  it("writes the one file the conductor reads, and nothing the model named as a path", async () => {
    const dir = mkdtempSync(join(tmpdir(), "podium-title-"));
    const file = join(dir, "chat-title.txt");
    const handler = newHandler(file);
    const listed = (await handler("tools/list", {})) as { tools: { name: string }[] };
    expect(listed.tools.map((tool) => tool.name)).toEqual(["set"]);

    const result = (await handler("tools/call", {
      name: "set",
      arguments: { title: '"Hello there."' },
    })) as { isError?: boolean; content: { text: string }[] };
    expect(result.isError).toBeUndefined();
    expect(readFileSync(file, "utf8")).toBe("Hello there\n");
  });

  it("refuses an empty title and a turn with no file", async () => {
    const handler = newHandler(join(tmpdir(), "does-not-matter"));
    const empty = (await handler("tools/call", { name: "set", arguments: { title: "  " } })) as {
      isError?: boolean;
    };
    expect(empty.isError).toBe(true);

    const nowhere = newHandler("");
    const missing = (await nowhere("tools/call", {
      name: "set",
      arguments: { title: "A real title" },
    })) as { isError?: boolean };
    expect(missing.isError).toBe(true);
  });
});
