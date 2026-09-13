import { describe, expect, it } from "vitest";
import { zipSkillFolder } from "./skillZip";

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
