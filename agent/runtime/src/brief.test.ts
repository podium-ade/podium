import { readFileSync } from "node:fs";

import { describe, expect, it } from "vitest";

import { BriefEnv, BriefError, MaxArgStrlen, MaxBriefBytes, decodeBrief } from "./brief.js";

/** encode builds the env the conductor sets. */
function encode(brief: unknown): NodeJS.ProcessEnv {
  return { [BriefEnv]: Buffer.from(JSON.stringify(brief), "utf8").toString("base64") };
}

const minimal = {
  version: 1,
  session_id: "sess_1",
  turn_id: "turn_1",
  source: { kind: "chat", ref: "chat_1" },
  profile: {
    name: "podium",
    display_name: "Podium",
    system_prompt: "be direct",
    model: "claude-opus-5",
  },
  provider: { id: "anthropic", api_key_env: "ANTHROPIC_API_KEY" },
  playbook: { name: "general", system_prompt: "answer questions", allowed_tools: ["read"], max_turns: 20 },
  transcript: [],
  transcript_truncated: false,
  instruction: "hello",
};

describe("decodeBrief", () => {
  it("decodes a valid brief", () => {
    const brief = decodeBrief(encode(minimal));
    expect(brief.session_id).toBe("sess_1");
    expect(brief.source.kind).toBe("chat");
    expect(brief.playbook.allowed_tools).toEqual(["read"]);
    expect(brief.repos).toBeUndefined();
    expect(brief.memory).toBeUndefined();
    expect(brief.provider.base_url).toBeUndefined();
    expect(brief.profile.effort).toBeUndefined();
  });

  it("requires a provider: a turn with nowhere to send its requests cannot run", () => {
    const { provider: _dropped, ...noProvider } = minimal;
    expect(() => decodeBrief(encode(noProvider))).toThrow(BriefError);
    expect(() => decodeBrief(encode({ ...minimal, provider: { id: "", api_key_env: "X" } })))
      .toThrow(BriefError);
    expect(() => decodeBrief(encode({ ...minimal, provider: { id: "xai", api_key_env: "" } })))
      .toThrow(BriefError);
  });

  it("refuses an effort level the SDK has no name for", () => {
    expect(() => decodeBrief(encode({ ...minimal, profile: { ...minimal.profile, effort: "ludicrous" } })))
      .toThrow(BriefError);
    expect(decodeBrief(encode({ ...minimal, profile: { ...minimal.profile, effort: "xhigh" } })).profile.effort)
      .toBe("xhigh");
  });

  it("decodes the golden fixture step 17 mirrors in Go", () => {
    const raw = readFileSync(new URL("../testdata/brief.example.json", import.meta.url), "utf8");
    const brief = decodeBrief({ [BriefEnv]: Buffer.from(raw, "utf8").toString("base64") });
    expect(brief.profile.name).toBe("podium");
    expect(brief.playbook.name).toBe("coder");
    expect(brief.transcript).toHaveLength(2);
    expect(brief.transcript_truncated).toBe(true);
    expect(brief.repos?.[0]?.name).toBe("podium");
    expect(brief.memory?.api_key_env).toBe("PODIUM_MEMORY_API_KEY");
    // The fixture is a Grok turn, so it is also the one place every new field has a value.
    expect(brief.profile.effort).toBe("xhigh");
    expect(brief.provider.id).toBe("xai");
    expect(brief.provider.api_key_env).toBe("XAI_API_KEY");
    expect(brief.provider.base_url).toBe("https://api.x.ai");
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
    expect(() => decodeBrief(env)).toThrow(`turn brief is ${size} bytes; the limit is 98304`);
  });

  it("keeps the cap under what a container can exec with", () => {
    // The cap this side refuses at is the cap the conductor emits at, and both exist to keep
    // PODIUM_AGENT_TURN inside MAX_ARG_STRLEN. Bisected against a real container — Docker
    // 29.4.3, Linux 6.12.76 aarch64, `getconf PAGESIZE` 4096:
    //
    //   PODIUM_AGENT_TURN of 131053 bytes  ->  ok
    //   PODIUM_AGENT_TURN of 131054 bytes  ->  exec /bin/sh: argument list too long
    //
    // Past that the container never starts, so this file never runs and nothing reports it.
    // The cap was 256 KiB, twice the ceiling; raising it back must fail here.
    const measuredLargestValue = 131053;
    expect(measuredLargestValue + `${BriefEnv}=`.length + 1).toBe(MaxArgStrlen);

    const envString = MaxBriefBytes + `${BriefEnv}=`.length + 1;
    expect(envString).toBeLessThan(MaxArgStrlen);
    // And not merely under it: a quarter of the ceiling stays unused, which is what the
    // name, the `=` and the NUL are paid out of.
    expect(MaxBriefBytes).toBeLessThanOrEqual(MaxArgStrlen - MaxArgStrlen / 4);
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

  it("takes a skill as a name, a digest and the variable its bundle travels in", () => {
    const skills = [
      {
        name: "pr-review",
        sha256: "a".repeat(64),
        bundle_env: "PODIUM_AGENT_SKILL_PR_REVIEW",
      },
    ];
    const brief = decodeBrief(encode({ ...minimal, playbook: { ...minimal.playbook, skills } }));
    expect(brief.playbook.skills).toEqual(skills);
    // Absent, not empty: a playbook that names no skills leaves the key out.
    expect(decodeBrief(encode(minimal)).playbook.skills).toBeUndefined();
  });

  it("refuses a skill ref that is not one", () => {
    const bad = [
      { name: "../etc", sha256: "a".repeat(64), bundle_env: "X" },
      { name: "Upper", sha256: "a".repeat(64), bundle_env: "X" },
      { name: "pr-review", sha256: "nothex", bundle_env: "X" },
      { name: "pr-review", sha256: "A".repeat(64), bundle_env: "X" },
      { name: "pr-review", sha256: "a".repeat(64), bundle_env: "" },
      { name: "pr-review", sha256: "a".repeat(64), bundle_env: "X", extra: 1 },
    ];
    for (const skill of bad) {
      expect(() =>
        decodeBrief(encode({ ...minimal, playbook: { ...minimal.playbook, skills: [skill] } })),
      ).toThrow(BriefError);
    }
  });

  it("takes an MCP server as an address and, at most, the name of a variable", () => {
    const mcp_servers = [
      { name: "linear", url: "https://mcp.linear.app/mcp", token_env: "PODIUM_MCP_LINEAR_TOKEN" },
      { name: "wiki", url: "http://wiki:9000/mcp" },
    ];
    const brief = decodeBrief(
      encode({ ...minimal, playbook: { ...minimal.playbook, mcp_servers } }),
    );
    expect(brief.playbook.mcp_servers).toEqual(mcp_servers);
    // Absent, not empty: a playbook that names no servers leaves the key out.
    expect(decodeBrief(encode(minimal)).playbook.mcp_servers).toBeUndefined();
  });

  it("refuses an MCP server ref that is not one", () => {
    const bad = [
      { name: "Linear", url: "https://x/mcp" },
      { name: "linear_wiki", url: "https://x/mcp" },
      { name: "../etc", url: "https://x/mcp" },
      { name: "linear", url: "" },
      { name: "linear", url: "https://x/mcp", token_env: "" },
      // The strict schema is what makes a misspelt key a failed turn rather than a field
      // that silently does nothing — a `token` here would be a token in the brief.
      { name: "linear", url: "https://x/mcp", token: "lin_api_secret" },
    ];
    for (const server of bad) {
      expect(() =>
        decodeBrief(encode({ ...minimal, playbook: { ...minimal.playbook, mcp_servers: [server] } })),
      ).toThrow(BriefError);
    }
  });

  it("refuses a source kind it does not know", () => {
    expect(() => decodeBrief(encode({ ...minimal, source: { kind: "email", ref: "x" } }))).toThrow(BriefError);
  });

  it("refuses a non-positive max_turns", () => {
    expect(() => decodeBrief(encode({ ...minimal, playbook: { ...minimal.playbook, max_turns: 0 } }))).toThrow(BriefError);
  });

  // The assistant runs with none: it answers a conversation and delegates, so what is worth
  // bounding is the container it starts. A task always carries one.
  it("accepts a brief with no max_turns at all", () => {
    const { max_turns: _dropped, ...playbook } = minimal.playbook;
    const brief = decodeBrief(encode({ ...minimal, playbook }));
    expect(brief.playbook.max_turns).toBeUndefined();
  });

  it("refuses something that is not base64 JSON", () => {
    expect(() => decodeBrief({ [BriefEnv]: "bm90IGpzb24=" })).toThrow(/is not base64-encoded JSON/);
  });

  it("accepts a host turn's delegation block", () => {
    const brief = decodeBrief(
      encode({
        ...minimal,
        runs_on: "host",
        delegation: {
          url: "http://127.0.0.1:8090",
          token_env: "PODIUM_TURN_TOKEN",
          playbooks: [
            { name: "podium", summary: "develops Podium itself", docker: true, repos: ["podium"] },
            { name: "general" },
          ],
        },
      }),
    );
    expect(brief.runs_on).toBe("host");
    expect(brief.delegation?.playbooks).toHaveLength(2);
    expect(brief.delegation?.playbooks[0]?.docker).toBe(true);
    expect(brief.delegation?.playbooks[1]?.summary).toBeUndefined();
  });

  it("has neither on a task's brief, which is what stops a delegated task delegating again", () => {
    const brief = decodeBrief(encode(minimal));
    expect(brief.delegation).toBeUndefined();
    expect(brief.runs_on).toBeUndefined();
  });

  it("refuses a delegation block with no playbooks, because an empty menu is not a menu", () => {
    expect(() =>
      decodeBrief(encode({ ...minimal, delegation: { url: "http://h", token_env: "T", playbooks: [] } })),
    ).toThrow(BriefError);
  });

  it("refuses an unknown key inside the delegation block", () => {
    // The token travels in the environment. A brief that could carry one is a brief that
    // must never be logged, and this is what keeps that true.
    expect(() =>
      decodeBrief(
        encode({
          ...minimal,
          delegation: {
            url: "http://h",
            token_env: "T",
            playbooks: [{ name: "podium" }],
            token: "leaked-by-accident",
          },
        }),
      ),
    ).toThrow(BriefError);
  });

  it("refuses a runs_on it does not know", () => {
    expect(() => decodeBrief(encode({ ...minimal, runs_on: "somewhere-else" }))).toThrow(BriefError);
    expect(decodeBrief(encode({ ...minimal, runs_on: "task" })).runs_on).toBe("task");
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
