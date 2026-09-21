import { describe, expect, it } from "vitest";
import { buildSkillTree } from "./SkillFileTree";

describe("buildSkillTree", () => {
  it("nests paths and lists folders before files", () => {
    const tree = buildSkillTree([
      "SKILL.md",
      "scripts/run.sh",
      "reference/checklist.md",
      "reference/notes.md",
    ]);
    expect(tree.map((n) => n.name)).toEqual(["reference", "scripts", "SKILL.md"]);
    expect(tree[0]?.children.map((n) => n.path)).toEqual([
      "reference/checklist.md",
      "reference/notes.md",
    ]);
    expect(tree[1]?.children.map((n) => n.path)).toEqual(["scripts/run.sh"]);
    expect(tree[2]).toMatchObject({ kind: "file", path: "SKILL.md" });
  });
});
