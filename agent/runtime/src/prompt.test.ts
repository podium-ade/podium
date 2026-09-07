import { readFileSync } from "node:fs";

import { describe, expect, it } from "vitest";

import { BriefEnv, decodeBrief, type TurnBrief } from "./brief.js";
import { buildSystemPrompt } from "./prompt.js";

function golden(): TurnBrief {
  const raw = readFileSync(new URL("../testdata/brief.example.json", import.meta.url), "utf8");
  return decodeBrief({ [BriefEnv]: Buffer.from(raw, "utf8").toString("base64") });
}

describe("buildSystemPrompt", () => {
  it("renders the whole prompt for a brief with memory and repos", () => {
    expect(buildSystemPrompt(golden())).toMatchSnapshot();
  });

  it("is deterministic", () => {
    const brief = golden();
    expect(buildSystemPrompt(brief)).toBe(buildSystemPrompt(brief));
  });

  it("leaves the memory block out when the brief has no memory", () => {
    const brief = golden();
    expect(buildSystemPrompt(brief)).toContain("# Shared memory");
    const { memory: _memory, ...rest } = brief;
    const prompt = buildSystemPrompt(rest as TurnBrief);
    expect(prompt).not.toContain("# Shared memory");
    expect(prompt).not.toContain("mcp__memory__recall");
  });

  it("leaves the repositories block out when the brief has no repos", () => {
    const brief = { ...golden(), repos: [] };
    expect(buildSystemPrompt(brief)).not.toContain("# Repositories");
  });

  it("marks a truncated transcript and not a whole one", () => {
    const brief = golden();
    expect(buildSystemPrompt(brief)).toContain("(earlier messages omitted)");
    expect(buildSystemPrompt({ ...brief, transcript_truncated: false })).not.toContain(
      "(earlier messages omitted)",
    );
  });

  it("says so when there is no transcript at all", () => {
    const brief = { ...golden(), transcript: [], transcript_truncated: false };
    expect(buildSystemPrompt(brief)).toContain("(no earlier messages; this is the first turn)");
  });

  it("names the source the answer is posted back to", () => {
    const brief = golden();
    expect(buildSystemPrompt(brief)).toContain("the Slack thread this came from");
    expect(buildSystemPrompt({ ...brief, source: { kind: "chat", ref: "chat_1" } })).toContain(
      "the web chat this came from",
    );
    expect(buildSystemPrompt({ ...brief, source: { kind: "linear", ref: "ENG-1" } })).toContain(
      "the Linear issue this came from",
    );
  });

  it("puts the profile before the playbook before the runtime block", () => {
    const prompt = buildSystemPrompt(golden());
    const profile = prompt.indexOf("You are Podium, the engineering team's agent.");
    const playbook = prompt.indexOf("You write code, verify it");
    const runtime = prompt.indexOf("# This turn");
    const transcript = prompt.indexOf("# The conversation so far");
    expect(profile).toBe(0);
    expect(playbook).toBeGreaterThan(profile);
    expect(runtime).toBeGreaterThan(playbook);
    expect(transcript).toBeGreaterThan(runtime);
  });
});
