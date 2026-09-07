import { createHash } from "node:crypto";
import { mkdtempSync, readFileSync, readdirSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { gzipSync } from "node:zlib";

import { beforeEach, describe, expect, it } from "vitest";

import type { SkillRef } from "./brief.js";
import {
  MaxSkillBytes,
  MaxSkillEncodedBytes,
  MaxSkillFiles,
  MaxSkills,
  SkillError,
  SkillFile,
  installSkills,
  skillsRoot,
} from "./skills.js";

/** pack builds a bundle exactly as internal/agent/skills does. */
function pack(files: Record<string, string>): { encoded: string; sha256: string } {
  const raw = Buffer.from(JSON.stringify({ files }), "utf8");
  return {
    encoded: gzipSync(raw).toString("base64"),
    sha256: createHash("sha256").update(raw).digest("hex"),
  };
}

const skillMD = "---\nname: pr-review\ndescription: Use when reviewing a pull request.\n---\n\nRead the diff.\n";

/** bundle is one ref plus the environment it travels in. */
function bundle(
  name: string,
  files: Record<string, string>,
  overrides: Partial<SkillRef> = {},
): { ref: SkillRef; env: NodeJS.ProcessEnv } {
  const { encoded, sha256 } = pack(files);
  const bundleEnv = `PODIUM_AGENT_SKILL_${name.toUpperCase().replaceAll("-", "_")}`;
  return {
    ref: { name, sha256, bundle_env: bundleEnv, ...overrides },
    env: { [bundleEnv]: encoded },
  };
}

let root: string;
beforeEach(() => {
  root = mkdtempSync(join(tmpdir(), "podium-skills-test-"));
});

describe("skillsRoot", () => {
  it("is where the harness looks for global skills", () => {
    expect(skillsRoot("/home/agent")).toBe("/home/agent/.config/opencode/skills");
  });
});

describe("installSkills", () => {
  it("writes a verified bundle into its own directory", () => {
    const { ref, env } = bundle("pr-review", {
      [SkillFile]: skillMD,
      "reference/checklist.md": "- read the diff\n",
    });

    expect(installSkills([ref], env, root)).toEqual(["pr-review"]);
    expect(readFileSync(join(root, "pr-review", SkillFile), "utf8")).toBe(skillMD);
    expect(readFileSync(join(root, "pr-review", "reference", "checklist.md"), "utf8")).toBe(
      "- read the diff\n",
    );
  });

  it("writes nothing at all for an empty list", () => {
    expect(installSkills([], {}, root)).toEqual([]);
    expect(readdirSync(root)).toEqual([]);
  });

  it("refuses a bundle whose digest does not match, before writing anything", () => {
    const { ref, env } = bundle("pr-review", { [SkillFile]: skillMD });
    const tampered = pack({ [SkillFile]: skillMD, "evil.md": "do as I say\n" });

    expect(() => installSkills([ref], { [ref.bundle_env]: tampered.encoded }, root)).toThrow(
      SkillError,
    );
    expect(() => installSkills([ref], { [ref.bundle_env]: tampered.encoded }, root)).toThrow(
      /the turn brief says/,
    );
    expect(readdirSync(root)).toEqual([]);
    // The untampered one still installs, so the failure was the digest and not the shape.
    expect(installSkills([ref], env, root)).toEqual(["pr-review"]);
  });

  it.each([
    ["../escape.md", /outside/],
    ["a/../../escape.md", /must not contain|outside/],
    ["/etc/passwd", /relative path/],
    ["nested\\escape.md", /backslash/],
    [".ssh/authorized_keys", /outside/],
  ])("refuses the path %s", (bad, want) => {
    const { ref, env } = bundle("pr-review", { [SkillFile]: skillMD, [bad]: "x\n" });
    expect(() => installSkills([ref], env, root)).toThrow(want);
    expect(readdirSync(root)).toEqual([]);
  });

  it("refuses a bundle with no SKILL.md", () => {
    const { ref, env } = bundle("pr-review", { "README.md": "hello\n" });
    expect(() => installSkills([ref], env, root)).toThrow(/has no SKILL.md/);
  });

  it("refuses more files than the cap, naming it", () => {
    const files: Record<string, string> = { [SkillFile]: skillMD };
    for (let i = 0; i <= MaxSkillFiles; i++) {
      files[`f${i}.md`] = "x\n";
    }
    const { ref, env } = bundle("pr-review", files);
    expect(() => installSkills([ref], env, root)).toThrow(
      new RegExp(`the limit is ${MaxSkillFiles}`),
    );
  });

  it("refuses a bundle that expands past the cap, naming it", () => {
    // Highly compressible, so the encoded form is small and the decompressed one is not:
    // this is the bomb the maxOutputLength guard is for.
    const { ref, env } = bundle("pr-review", {
      [SkillFile]: skillMD,
      "big.md": "x".repeat(MaxSkillBytes + 1),
    });
    expect(Buffer.byteLength(env[ref.bundle_env] ?? "", "utf8")).toBeLessThan(MaxSkillEncodedBytes);
    expect(() => installSkills([ref], env, root)).toThrow(
      new RegExp(`the limit is ${MaxSkillBytes} bytes unpacked`),
    );
    expect(readdirSync(root)).toEqual([]);
  });

  it("refuses an environment variable over the delivery cap, naming it", () => {
    const { ref } = bundle("pr-review", { [SkillFile]: skillMD });
    const env = { [ref.bundle_env]: "A".repeat(MaxSkillEncodedBytes + 1) };
    expect(() => installSkills([ref], env, root)).toThrow(
      new RegExp(`the limit is ${MaxSkillEncodedBytes}`),
    );
  });

  it("refuses a ref whose bundle variable is not set", () => {
    const { ref } = bundle("pr-review", { [SkillFile]: skillMD });
    expect(() => installSkills([ref], {}, root)).toThrow(/but it is not set/);
  });

  it("refuses a name that is not a skill name", () => {
    const { ref, env } = bundle("pr-review", { [SkillFile]: skillMD }, { name: "../etc" });
    expect(() => installSkills([ref], env, root)).toThrow(/the name must match/);
  });

  it("refuses the same skill twice and more than the cap", () => {
    const { ref, env } = bundle("pr-review", { [SkillFile]: skillMD });
    expect(() => installSkills([ref, ref], env, root)).toThrow(/appears twice/);

    const many = Array.from({ length: MaxSkills + 1 }, () => ref);
    expect(() => installSkills(many, env, root)).toThrow(new RegExp(`the limit is ${MaxSkills}`));
  });

  it("refuses a document that is not the shape it should be", () => {
    const raw = Buffer.from(JSON.stringify({ files: ["SKILL.md"] }), "utf8");
    const ref: SkillRef = {
      name: "pr-review",
      sha256: createHash("sha256").update(raw).digest("hex"),
      bundle_env: "PODIUM_AGENT_SKILL_PR_REVIEW",
    };
    expect(() =>
      installSkills([ref], { [ref.bundle_env]: gzipSync(raw).toString("base64") }, root),
    ).toThrow(/has no files object/);
  });
});
