import { describe, expect, it } from "vitest";
import {
  assistantSection,
  canSave,
  draftFromProfile,
  isDirty,
  parseGoDuration,
  updateFromDraft,
  viewFromProfile,
  type AssistantDraft,
  type AssistantProfileView,
} from "./assistantProfile";

function view(over: Partial<AssistantProfileView> = {}): AssistantProfileView {
  return viewFromProfile({
    id: "ast_1",
    name: "podium",
    displayName: "Podium",
    systemPrompt: "you are Podium",
    model: "claude-opus-5",
    agent: "claude",
    effort: "",
    skills: ["review"],
    mcpServers: ["linear"],
    maxTurns: 0,
    timeout: "15m0s",
    gitName: "File",
    gitEmail: "file@users.noreply.github.com",
    ...over,
  });
}

function saved(): AssistantDraft {
  return draftFromProfile(view());
}

describe("assistantProfile", () => {
  it("reads an empty agent and an unset clock as the values a turn runs", () => {
    const draft = draftFromProfile(view({ agent: "", timeout: "" }));
    expect(draft.agent).toBe("claude");
    expect(draft.timeout).toBe("15m0s");
    expect(parseGoDuration("15m")).toBe(parseGoDuration("15m0s"));
  });

  it("sends the whole definition, and treats 15m and 15m0s as the same clock", () => {
    const draft = saved();
    const update = updateFromDraft(draft);
    expect(update).toMatchObject({
      id: "ast_1",
      name: "podium",
      displayName: "Podium",
      systemPrompt: "you are Podium",
      agent: "claude",
      model: "claude-opus-5",
      skills: ["review"],
      mcpServers: ["linear"],
      maxTurns: 0,
      gitName: "File",
      gitEmail: "file@users.noreply.github.com",
    });
    expect(isDirty({ ...draft, timeout: "15m" }, draft)).toBe(false);
    expect(canSave({ ...draft, timeout: "15m" }, draft)).toBe(false);
  });

  it("stores an empty grant and a cap of none as the definition", () => {
    const draft = { ...saved(), skills: [], mcpServers: [], maxTurns: 0 };
    const update = updateFromDraft(draft);
    expect(update.skills).toEqual([]);
    expect(update.mcpServers).toEqual([]);
    expect(update.maxTurns).toBe(0);
    expect(canSave(draft, saved())).toBe(true);
  });

  it("clears a persona by saving both fields empty", () => {
    const draft = saved();
    const cleared = { ...draft, gitName: "", gitEmail: "" };
    const update = updateFromDraft(cleared);
    expect(update.gitName).toBe("");
    expect(update.gitEmail).toBe("");
    expect(canSave(cleared, draft)).toBe(true);
    expect(canSave({ ...draft, gitName: "Only", gitEmail: "" }, draft)).toBe(false);
  });

  it("refuses a prompt that would not fit a turn and a name the sessions cannot store", () => {
    const draft = saved();
    expect(canSave({ ...draft, systemPrompt: "a".repeat(32 * 1024 + 1) }, draft)).toBe(false);
    expect(canSave({ ...draft, name: "Night Shift" }, draft)).toBe(false);
    expect(canSave({ ...draft, name: "Podium" }, draft)).toBe(false);
    expect(canSave({ ...draft, name: "night" }, draft)).toBe(true);
  });

  it("reads the address, and treats the bare path as Identity", () => {
    expect(assistantSection("/agent/profile")).toBe("identity");
    expect(assistantSection("/agent/profile/limits")).toBe("limits");
    expect(assistantSection("/agent/profile/source")).toBeNull();
    expect(assistantSection("/agent/profile/nope")).toBeNull();
    expect(assistantSection("/agent/assistants")).toBe("identity");
    expect(assistantSection("/agent/assistants/podium/model")).toBe("model");
    expect(assistantSection("/agent/assistants/new")).toBeNull();
  });
});
