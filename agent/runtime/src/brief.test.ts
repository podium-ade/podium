import { readFileSync } from "node:fs";

import { describe, expect, it } from "vitest";

import { BriefEnv, BriefError, MaxBriefBytes, decodeBrief } from "./brief.js";

/** encode builds the env the conductor sets. */
function encode(brief: unknown): NodeJS.ProcessEnv {
  return { [BriefEnv]: Buffer.from(JSON.stringify(brief), "utf8").toString("base64") };
}

const minimal = {
  version: 1,
  session_id: "sess_1",
  turn_id: "turn_1",
  source: { kind: "chat", ref: "chat_1" },
  profile: { name: "podium", display_name: "Podium", system_prompt: "be direct", model: "claude-opus-5" },
  skill: { name: "general", system_prompt: "answer questions", allowed_tools: ["Read"], max_turns: 20 },
  transcript: [],
  transcript_truncated: false,
  instruction: "hello",
};

describe("decodeBrief", () => {
  it("decodes a valid brief", () => {
    const brief = decodeBrief(encode(minimal));
    expect(brief.session_id).toBe("sess_1");
    expect(brief.source.kind).toBe("chat");
    expect(brief.skill.allowed_tools).toEqual(["Read"]);
    expect(brief.repos).toBeUndefined();
    expect(brief.memory).toBeUndefined();
  });

  it("decodes the golden fixture step 17 mirrors in Go", () => {
    const raw = readFileSync(new URL("../testdata/brief.example.json", import.meta.url), "utf8");
    const brief = decodeBrief({ [BriefEnv]: Buffer.from(raw, "utf8").toString("base64") });
    expect(brief.profile.name).toBe("podium");
    expect(brief.skill.name).toBe("coder");
    expect(brief.transcript).toHaveLength(2);
    expect(brief.transcript_truncated).toBe(true);
    expect(brief.repos?.[0]?.name).toBe("podium");
    expect(brief.memory?.api_key_env).toBe("PODIUM_MEMORY_API_KEY");
  });

  it("round-trips transcript_truncated", () => {
    expect(decodeBrief(encode({ ...minimal, transcript_truncated: true })).transcript_truncated).toBe(true);
    expect(decodeBrief(encode({ ...minimal, transcript_truncated: false })).transcript_truncated).toBe(false);
  });

  it("refuses a missing env", () => {
    expect(() => decodeBrief({})).toThrow(`${BriefEnv} is not set`);
    expect(() => decodeBrief({ [BriefEnv]: "" })).toThrow(`${BriefEnv} is not set`);
  });

  it("refuses a version it does not speak", () => {
    expect(() => decodeBrief(encode({ ...minimal, version: 2 }))).toThrow(
      "turn brief version is 2; this runtime speaks version 1",
    );
  });

  it("refuses an unknown top-level key", () => {
    expect(() => decodeBrief(encode({ ...minimal, sesion_id: "typo" }))).toThrow(BriefError);
    expect(() => decodeBrief(encode({ ...minimal, sesion_id: "typo" }))).toThrow(/sesion_id/);
  });

  it("refuses a brief over the encoded cap, naming the real size", () => {
    const padded = { ...minimal, instruction: "x".repeat(MaxBriefBytes) };
    const env = encode(padded);
    const size = Buffer.byteLength(env[BriefEnv] ?? "", "utf8");
    expect(size).toBeGreaterThan(MaxBriefBytes);
    expect(() => decodeBrief(env)).toThrow(`turn brief is ${size} bytes; the limit is 262144`);
  });

  it("accepts a brief exactly at the cap", () => {
    // base64 is 4 bytes per 3, so the largest JSON that still encodes to exactly the cap is
    // 3 * (cap / 4) - 2 bytes long. "x" needs no JSON escaping, so the filler is exact.
    const jsonBytes = 3 * (MaxBriefBytes / 4) - 2;
    const empty = JSON.stringify({ ...minimal, instruction: "" }).length;
    const env = encode({ ...minimal, instruction: "x".repeat(jsonBytes - empty) });
    expect(Buffer.byteLength(env[BriefEnv] ?? "", "utf8")).toBe(MaxBriefBytes);
    expect(decodeBrief(env).instruction).toHaveLength(jsonBytes - empty);
  });

  it("refuses a repo name that is not a safe directory", () => {
    for (const name of ["../etc", ".podium", "Podium", "", "a".repeat(65), "has space"]) {
      expect(() => decodeBrief(encode({ ...minimal, repos: [{ name, url: "https://x/y", default_branch: "main" }] })))
        .toThrow(BriefError);
    }
    expect(
      decodeBrief(encode({ ...minimal, repos: [{ name: "podium.v2-x", url: "https://x/y", default_branch: "main" }] }))
        .repos?.[0]?.name,
    ).toBe("podium.v2-x");
  });

  it("refuses a source kind it does not know", () => {
    expect(() => decodeBrief(encode({ ...minimal, source: { kind: "email", ref: "x" } }))).toThrow(BriefError);
  });

  it("refuses a non-positive max_turns", () => {
    expect(() => decodeBrief(encode({ ...minimal, skill: { ...minimal.skill, max_turns: 0 } }))).toThrow(BriefError);
  });

  it("refuses something that is not base64 JSON", () => {
    expect(() => decodeBrief({ [BriefEnv]: "bm90IGpzb24=" })).toThrow(/is not base64-encoded JSON/);
  });

  it("carries exit code 2 on every failure", () => {
    try {
      decodeBrief({});
      expect.unreachable("decodeBrief must throw");
    } catch (err) {
      expect(err).toBeInstanceOf(BriefError);
      expect((err as BriefError).exitCode).toBe(2);
    }
  });
});
