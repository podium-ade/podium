import { mkdtempSync, readFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

import { describe, expect, it, vi } from "vitest";

import { TurnName } from "./artifacts.js";
import type { RunnerInvoke } from "./emit.js";
import { addTokens, noTokens, reportTurn, storeArtifacts, type Summary } from "./report.js";

function recorder(): { calls: { argv: string[]; text: string }[]; invoke: RunnerInvoke } {
  const calls: { argv: string[]; text: string }[] = [];
  return {
    calls,
    invoke: vi.fn(async (argv: string[], text: string) => {
      calls.push({ argv, text });
    }),
  };
}

function typeOf(argv: string[]): string {
  return argv[argv.indexOf("--type") + 1] ?? "";
}

const summary: Summary = {
  sessionID: "sess_1",
  turnID: "turn_1",
  sdkSessionID: "sdk_1",
  turns: 4,
  cost: 0.0123,
  tokens: { input: 1200, output: 340, reasoning: 50, cacheRead: 9000, cacheWrite: 800 },
  code: 0,
  startedAt: "2026-09-05T10:00:00.000Z",
};

describe("addTokens", () => {
  it("sums each step, counting a field the step left out as nothing", () => {
    const t = noTokens();
    addTokens(t, { input: 10, output: 2, reasoning: 1, cache: { read: 100, write: 5 } });
    addTokens(t, { input: 3, output: 4 });
    addTokens(t, undefined);
    expect(t).toEqual({ input: 13, output: 6, reasoning: 1, cacheRead: 100, cacheWrite: 5 });
  });
});

describe("reportTurn", () => {
  it("emits the accounting after the answer, so a resumed turn is still sent it", async () => {
    const r = recorder();
    await reportTurn(r.invoke, summary, "pong", [], mkdtempSync(join(tmpdir(), "podium-report-")));

    expect(r.calls.map((c) => typeOf(c.argv))).toEqual(["final", "accounting"]);
    expect(r.calls[0]?.text).toBe("pong");
  });

  it("puts num_turns and total_cost_usd in the accounting message", async () => {
    const r = recorder();
    await reportTurn(r.invoke, summary, "pong", [], mkdtempSync(join(tmpdir(), "podium-report-")));

    const acct = JSON.parse(r.calls[1]?.text ?? "");
    expect(acct.num_turns).toBe(4);
    expect(acct.total_cost_usd).toBe(0.0123);
    expect(acct.session_id).toBe("sess_1");
    expect(acct.turn_id).toBe("turn_1");
  });

  it("puts the turn's tokens in the accounting message", async () => {
    const r = recorder();
    await reportTurn(r.invoke, summary, "pong", [], mkdtempSync(join(tmpdir(), "podium-report-")));

    const acct = JSON.parse(r.calls[1]?.text ?? "");
    expect(acct).toMatchObject({
      input_tokens: 1200,
      output_tokens: 340,
      reasoning_tokens: 50,
      cache_read_tokens: 9000,
      cache_write_tokens: 800,
    });
  });

  it("says the same thing in the message as in turn.json", async () => {
    const dir = mkdtempSync(join(tmpdir(), "podium-report-"));
    const r = recorder();
    await reportTurn(r.invoke, summary, "pong", [], dir);

    // The runner trims trailing whitespace, so the message is turn.json without its newline.
    expect(r.calls[1]?.text).toBe(readFileSync(join(dir, TurnName), "utf8").trimEnd());
  });

  it("still emits the accounting when the artifacts directory is unusable", async () => {
    const r = recorder();
    await reportTurn(r.invoke, summary, "pong", [], "/nonexistent/podium/artifacts");

    expect(r.calls.map((c) => typeOf(c.argv))).toEqual(["final", "accounting"]);
  });

  it("attaches only to the answer, never to the accounting", async () => {
    const r = recorder();
    await reportTurn(r.invoke, summary, "see out.png", ["out.png"], mkdtempSync(join(tmpdir(), "podium-report-")));

    expect(r.calls[0]?.argv).toContain("out.png");
    expect(r.calls[1]?.argv).not.toContain("--attach");
  });

  it("reports the accounting even when the answer could not be delivered", async () => {
    const calls: string[] = [];
    const invoke = vi.fn<RunnerInvoke>(async (argv: string[]) => {
      calls.push(typeOf(argv));
      if (typeOf(argv) === "final") {
        throw new Error("no node is listening");
      }
    });
    await reportTurn(invoke, summary, "pong", [], mkdtempSync(join(tmpdir(), "podium-report-")));

    expect(calls).toEqual(["final", "accounting"]);
  });
});

describe("storeArtifacts", () => {
  it("hands each named file to the node under its own name", async () => {
    const r = recorder();
    await storeArtifacts(r.invoke, ["a.png", "b.png"], "/workspace/.podium/artifacts");

    expect(r.calls.map((c) => c.argv)).toEqual([
      ["artifact", "add", "/workspace/.podium/artifacts/a.png", "--name", "a.png"],
      ["artifact", "add", "/workspace/.podium/artifacts/b.png", "--name", "b.png"],
    ]);
  });

  it("carries on past a file the runner refused, so the answer is still reported", async () => {
    const calls: string[] = [];
    const invoke: RunnerInvoke = async (argv) => {
      calls.push(argv[2] ?? "");
      if (calls.length === 1) {
        throw new Error("no node is listening");
      }
    };
    vi.spyOn(process.stderr, "write").mockImplementation(() => true);

    await expect(storeArtifacts(invoke, ["a.png", "b.png"], "/d")).resolves.toBeUndefined();
    expect(calls).toEqual(["/d/a.png", "/d/b.png"]);
  });
});
