import { mkdirSync, mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

import { beforeEach, describe, expect, it } from "vitest";

import {
  TranscriptName,
  TurnName,
  appendTranscript,
  ensureArtifacts,
  matchAttachments,
  writeTurn,
} from "./artifacts.js";

let dir: string;

beforeEach(() => {
  dir = mkdtempSync(join(tmpdir(), "podium-artifacts-"));
});

function touch(name: string, body = "x"): void {
  writeFileSync(join(dir, name), body, "utf8");
}

describe("matchAttachments", () => {
  it("attaches only what the final text names", () => {
    touch("before.png");
    touch("after.png");
    touch("unmentioned.png");
    expect(matchAttachments("compare before.png with after.png", dir)).toEqual(["after.png", "before.png"]);
  });

  it("attaches nothing when nothing is named", () => {
    touch("report.txt");
    expect(matchAttachments("all done", dir)).toEqual([]);
  });

  it("attaches nothing for a name with no file", () => {
    expect(matchAttachments("see missing.png", dir)).toEqual([]);
  });

  it("matches the exact file name, not a suffix of a longer one", () => {
    touch("shot.png");
    expect(matchAttachments("here is screenshot.png", dir)).toEqual([]);
    expect(matchAttachments("here is shot.png", dir)).toEqual(["shot.png"]);
    expect(matchAttachments("here is shot.png.", dir)).toEqual(["shot.png"]);
    expect(matchAttachments("here is shot.pngx", dir)).toEqual([]);
    expect(matchAttachments("here is shots/shot.png", dir)).toEqual([]);
  });

  it("never attaches the runtime's own files", () => {
    ensureArtifacts(dir);
    writeTurn(
      {
        session_id: "sess_1",
        turn_id: "turn_1",
        sdk_session_id: "dry-run",
        num_turns: 0,
        total_cost_usd: 0,
        exit_code: 0,
        started_at: "2026-09-03T10:00:00.000Z",
        finished_at: "2026-09-03T10:00:01.000Z",
      },
      dir,
    );
    expect(matchAttachments(`${TranscriptName} and ${TurnName}`, dir)).toEqual([]);
  });

  it("ignores subdirectories", () => {
    mkdirSync(join(dir, "shots"));
    touch("top.png");
    expect(matchAttachments("shots and top.png", dir)).toEqual(["top.png"]);
  });

  it("survives a directory that does not exist", () => {
    expect(matchAttachments("anything", join(dir, "nope"))).toEqual([]);
  });
});

describe("ensureArtifacts", () => {
  it("leaves an empty transcript so a turn with no SDK message still has both files", () => {
    const nested = join(dir, "a", "b");
    ensureArtifacts(nested);
    expect(readFileSync(join(nested, TranscriptName), "utf8")).toBe("");
  });

  it("does not truncate an existing transcript", () => {
    ensureArtifacts(dir);
    appendTranscript({ type: "assistant" }, dir);
    ensureArtifacts(dir);
    expect(readFileSync(join(dir, TranscriptName), "utf8")).toBe('{"type":"assistant"}\n');
  });
});

describe("appendTranscript", () => {
  it("writes one JSON line per message, whatever shape it is", () => {
    ensureArtifacts(dir);
    appendTranscript({ type: "system", subtype: "init" }, dir);
    appendTranscript({ type: "some_kind_this_runtime_has_never_heard_of", nested: { a: [1, 2] } }, dir);
    const lines = readFileSync(join(dir, TranscriptName), "utf8").trimEnd().split("\n");
    expect(lines).toHaveLength(2);
    expect(JSON.parse(lines[1] ?? "")).toEqual({
      type: "some_kind_this_runtime_has_never_heard_of",
      nested: { a: [1, 2] },
    });
  });
});
