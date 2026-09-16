import { describe, expect, it } from "vitest";
import { markdownFiles } from "./SkillEditor";

describe("markdownFiles", () => {
  it("keeps markdown and SKILL.md, and drops everything else", () => {
    const files = [
      new File(["a"], "SKILL.md"),
      new File(["b"], "notes.md"),
      new File(["c"], "run.sh"),
      new File(["d"], "readme.markdown"),
    ];
    expect(markdownFiles(files).map((f) => f.name)).toEqual(["SKILL.md", "notes.md", "readme.markdown"]);
  });
});
