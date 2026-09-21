import { describe, expect, it } from "vitest";
import { bundleSkillFolders, zipSkillFolder } from "./skillZip";

function entry(path: string, text: string) {
  return { path, data: new TextEncoder().encode(text) };
}

describe("zipSkillFolder", () => {
  it("writes a zip the conductor would recognise, with SKILL.md at the path given", () => {
    const body = new TextEncoder().encode("---\nname: pr-review\n---\n");
    const zip = zipSkillFolder([{ path: "pr-review/SKILL.md", data: body }]);
    expect(Array.from(zip.slice(0, 4))).toEqual([0x50, 0x4b, 0x03, 0x04]);
    const asText = new TextDecoder().decode(zip);
    expect(asText).toContain("pr-review/SKILL.md");
    expect(asText).toContain("name: pr-review");
  });
});

describe("bundleSkillFolders", () => {
  it("zips one skill directory with SKILL.md at the archive root", () => {
    const bundles = bundleSkillFolders([
      entry("pr-review/SKILL.md", "---\nname: pr-review\n---\n"),
      entry("pr-review/reference/checklist.md", "- read the diff\n"),
      entry("pr-review/scripts/run.sh", "#!/bin/sh\necho hi\n"),
      entry("README.md", "beside the skill\n"),
    ]);
    expect(bundles).toHaveLength(1);
    expect(bundles?.[0]?.filename).toBe("pr-review.zip");
    const asText = new TextDecoder().decode(bundles?.[0]?.content ?? new Uint8Array());
    expect(asText).toContain("reference/checklist.md");
    expect(asText).toContain("scripts/run.sh");
    expect(asText).toContain("#!/bin/sh\necho hi\n");
    expect(asText).not.toContain("pr-review/SKILL.md");
    expect(asText).not.toContain("beside the skill");
  });

  it("returns null when the folder has no SKILL.md", () => {
    expect(bundleSkillFolders([entry("notes.md", "hello\n"), entry("other.md", "x\n")])).toBeNull();
  });

  it("zips each skill directory in a parent folder", () => {
    const bundles = bundleSkillFolders([
      entry("skills/pr-review/SKILL.md", "a"),
      entry("skills/pr-review/scripts/run.sh", "b"),
      entry("skills/release-notes/SKILL.md", "c"),
    ]);
    expect(bundles?.map((b) => b.filename)).toEqual(["pr-review.zip", "release-notes.zip"]);
    const first = new TextDecoder().decode(bundles?.[0]?.content ?? new Uint8Array());
    expect(first).toContain("scripts/run.sh");
    expect(first).not.toContain("skills/pr-review/");
  });
});
