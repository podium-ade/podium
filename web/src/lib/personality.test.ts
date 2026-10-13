import { describe, expect, it } from "vitest";
import {
  canSavePersonality,
  cleanDisplay,
  comparePersonalities,
  emptyPersonality,
  inheritedAssistant,
  personalityIssues,
} from "./personality";

const ok = {
  name: "night-owl",
  displayName: "Night Owl",
  instructions: "Speak briefly.",
  agent: "",
  model: "",
  effort: "",
};

describe("personality", () => {
  it("accepts a trimmed voice and ignores the stored name", () => {
    expect(
      personalityIssues({ ...ok, name: "", displayName: "  Night   Owl ", instructions: "  Speak briefly.  " }),
    ).toEqual([]);
    expect(cleanDisplay("  Night   Owl ")).toBe("Night Owl");
    expect(personalityIssues({ ...ok, name: "" })).toEqual([]);
    expect(personalityIssues({ ...ok, name: "Podium" })).toEqual([]);
    expect(personalityIssues({ ...ok, displayName: "Ow\u0000l" })).toContain("display-name");
  });

  it("orders rows by the display name", () => {
    const rows = [
      { name: "podium-2", displayName: "Podium" },
      { name: "night-owl", displayName: "Night Owl" },
    ];
    expect([...rows].sort(comparePersonalities).map((row) => row.displayName)).toEqual(["Night Owl", "Podium"]);
  });

  it("caps the display name and the instructions in characters, not bytes", () => {
    const display = "é".repeat(80);
    expect(personalityIssues({ ...ok, displayName: display })).toEqual([]);
    expect(personalityIssues({ ...ok, displayName: display + "é" })).toContain("display-name");
    const instructions = "é".repeat(4000);
    expect(personalityIssues({ ...ok, instructions })).toEqual([]);
    expect(personalityIssues({ ...ok, instructions: instructions + "é" })).toContain("instructions");
    expect(personalityIssues({ ...ok, instructions: "   " })).toContain("instructions");
  });

  it("saves only a draft that differs and is valid", () => {
    expect(canSavePersonality(ok, ok)).toBe(false);
    expect(canSavePersonality({ ...ok, instructions: "Speak once." }, ok)).toBe(true);
    expect(canSavePersonality({ ...ok, agent: "grok", model: "grok-4.6" }, ok)).toBe(true);
    expect(canSavePersonality(emptyPersonality(), emptyPersonality())).toBe(false);
    expect(canSavePersonality(ok, emptyPersonality())).toBe(true);
  });

  it("shows a voice's model without renaming the bubbles", () => {
    const podium = { displayName: "Podium", agent: "claude", model: "claude-opus-5", effort: "high" };
    expect(inheritedAssistant(podium, undefined)).toEqual(podium);
    expect(inheritedAssistant(podium, { agent: "", model: "", effort: "" })).toEqual(podium);
    expect(inheritedAssistant(podium, { agent: "grok", model: "grok-4.6", effort: "" })).toEqual({
      displayName: "Podium",
      agent: "grok",
      model: "grok-4.6",
      effort: "",
    });
    expect(inheritedAssistant(podium, { agent: "", model: "", effort: "low" })).toEqual({
      ...podium,
      effort: "low",
    });
  });
});
