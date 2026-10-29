import { mkdirSync, mkdtempSync, symlinkSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";

import { beforeEach, describe, expect, it } from "vitest";

import type { SkillRef } from "./brief.js";
import {
  MaxSkillBytes,
  MaxSkillFiles,
  MaxSkills,
  SkillError,
  SkillFile,
  resolveSkillsDir,
  skillDigest,
  verifySkills,
} from "./skills.js";

const skillMD = "---\nname: pr-review\ndescription: Use when reviewing a pull request.\n---\n\nRead the diff.\n";

function digestOf(files: Record<string, string>): string {
  return skillDigest(new Map(Object.entries(files).map(([k, v]) => [k, Buffer.from(v, "utf8")])));
}

/** lay writes one skill under root exactly as the node would, and returns its ref. */
function lay(root: string, name: string, files: Record<string, string>): SkillRef {
  for (const [rel, content] of Object.entries(files)) {
    const path = join(root, name, rel);
    mkdirSync(dirname(path), { recursive: true });
    writeFileSync(path, content);
  }
  return { name, sha256: digestOf(files) };
}

let root: string;
beforeEach(() => {
  root = mkdtempSync(join(tmpdir(), "podium-skills-test-"));
});

describe("skillDigest", () => {
  it("is internal/agent/skills.Digest", () => {
    // Pinned in internal/agent/skills/skills_test.go too: the two must not drift apart.
    expect(
      digestOf({
        "SKILL.md": "---\nname: pr-review\n---\n",
        "reference/checklist.md": "- read the diff ✓\n",
      }),
    ).toBe("8818b95a9167cc448360be8bc1c4bbfd600fdf99bbfb4c589c159c1885060317");
  });

  it("puts the length in the hash", () => {
    expect(digestOf({ ab: "c" })).not.toBe(digestOf({ a: "bc" }));
  });
});

describe("resolveSkillsDir", () => {
  it("takes an absolute path as it is and a relative one from HOME", () => {
    expect(resolveSkillsDir("/podium/files/skills", "/home/agent")).toBe("/podium/files/skills");
    expect(resolveSkillsDir(".podium/skills", "/tmp/turn/home")).toBe("/tmp/turn/home/.podium/skills");
  });
});

describe("verifySkills", () => {
  it("accepts a directory that is what the brief says", () => {
    const ref = lay(root, "pr-review", {
      [SkillFile]: skillMD,
      "reference/checklist.md": "- read the diff\n",
    });
    expect(verifySkills([ref], root)).toEqual(["pr-review"]);
  });

  it("accepts nothing at all for an empty list", () => {
    expect(verifySkills([], root)).toEqual([]);
  });

  it("refuses a skill that was not mounted, saying so", () => {
    expect(() => verifySkills([{ name: "pr-review", sha256: "a".repeat(64) }], root)).toThrow(
      /did not mount it/,
    );
  });

  it("refuses a directory whose digest does not match", () => {
    const ref = lay(root, "pr-review", { [SkillFile]: skillMD });
    writeFileSync(join(root, "pr-review", "evil.md"), "do as I say\n");
    expect(() => verifySkills([ref], root)).toThrow(SkillError);
    expect(() => verifySkills([ref], root)).toThrow(/the turn brief says/);
  });

  it("refuses a symlink inside a skill", () => {
    const ref = lay(root, "pr-review", { [SkillFile]: skillMD });
    symlinkSync("/etc/passwd", join(root, "pr-review", "passwd"));
    expect(() => verifySkills([ref], root)).toThrow(/not a regular file/);
  });

  it("refuses a skill with no SKILL.md", () => {
    const ref = lay(root, "pr-review", { "README.md": "hello\n" });
    expect(() => verifySkills([ref], root)).toThrow(/has no SKILL.md/);
  });

  it("refuses more files than the cap, naming it", () => {
    const files: Record<string, string> = { [SkillFile]: skillMD };
    for (let i = 0; i <= MaxSkillFiles; i++) {
      files[`f${i}.md`] = "x\n";
    }
    const ref = lay(root, "pr-review", files);
    expect(() => verifySkills([ref], root)).toThrow(new RegExp(`more than ${MaxSkillFiles} files`));
  });

  it("refuses more bytes than the cap, naming it", () => {
    const ref = lay(root, "pr-review", { [SkillFile]: skillMD, "big.md": "x".repeat(MaxSkillBytes) });
    expect(() => verifySkills([ref], root)).toThrow(new RegExp(`more than ${MaxSkillBytes} bytes`));
  });

  it("takes a skill far past what an environment variable could carry", () => {
    const ref = lay(root, "pr-review", { [SkillFile]: skillMD, "big.md": "x".repeat(512 * 1024) });
    expect(verifySkills([ref], root)).toEqual(["pr-review"]);
  });

  it("refuses a name that is not a skill name", () => {
    expect(() => verifySkills([{ name: "../etc", sha256: "a".repeat(64) }], root)).toThrow(
      /the name must match/,
    );
  });

  it("refuses the same skill twice and more than the cap", () => {
    const ref = lay(root, "pr-review", { [SkillFile]: skillMD });
    expect(() => verifySkills([ref, ref], root)).toThrow(/appears twice/);

    const many = Array.from({ length: MaxSkills + 1 }, () => ref);
    expect(() => verifySkills(many, root)).toThrow(new RegExp(`the limit is ${MaxSkills}`));
  });
});
