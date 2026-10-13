/** A personal assistant is a display name, instructions, and an optional model. The stored name is generated. */

export const MAX_PERSONALITY_DISPLAY = 80;
export const MAX_PERSONALITY_INSTRUCTIONS = 4000;

export type PersonalityDraft = {
  name: string;
  displayName: string;
  instructions: string;
  /** Empty agent, model, and effort follow Podium. */
  agent: string;
  model: string;
  effort: string;
};

export const emptyPersonality = (): PersonalityDraft => ({
  name: "",
  displayName: "",
  instructions: "",
  agent: "",
  model: "",
  effort: "",
});

function runes(value: string): number {
  return Array.from(value).length;
}

function hasControl(value: string): boolean {
  for (const ch of value) {
    const code = ch.codePointAt(0) ?? 0;
    if (code <= 0x1f || code === 0x7f) return true;
  }
  return false;
}

/** cleanDisplay collapses whitespace the way the store does, so a save round-trips. */
export function cleanDisplay(value: string): string {
  return value.trim().split(/\s+/).filter(Boolean).join(" ");
}

export function personalityIssues(draft: PersonalityDraft): string[] {
  const issues: string[] = [];
  const display = cleanDisplay(draft.displayName);
  if (display === "" || runes(display) > MAX_PERSONALITY_DISPLAY || hasControl(display)) {
    issues.push("display-name");
  }
  const instructions = draft.instructions.trim();
  if (instructions === "" || runes(instructions) > MAX_PERSONALITY_INSTRUCTIONS) issues.push("instructions");
  return issues;
}

function sameModel(a: PersonalityDraft, b: PersonalityDraft): boolean {
  return (
    a.agent.trim() === b.agent.trim() &&
    a.model.trim() === b.model.trim() &&
    a.effort.trim() === b.effort.trim()
  );
}

/** personalityModelDirty is the unsaved mark on the Model section. */
export function personalityModelDirty(draft: PersonalityDraft, saved: PersonalityDraft): boolean {
  return !sameModel(draft, saved);
}

export function isPersonalityDirty(draft: PersonalityDraft, saved: PersonalityDraft): boolean {
  return (
    cleanDisplay(draft.displayName) !== cleanDisplay(saved.displayName) ||
    draft.instructions.trim() !== saved.instructions.trim() ||
    !sameModel(draft, saved)
  );
}

/**
 * inheritedAssistant is the model a chat shows when nobody has picked one in that chat.
 * A voice that names nothing follows Podium. A voice that names a model keeps an empty
 * effort as Auto. The display name is left alone, because the bubbles stay Podium's.
 */
export function inheritedAssistant<T extends { agent: string; model: string; effort: string }>(
  podium: T | undefined,
  voice: { agent?: string; model?: string; effort?: string } | undefined,
): T | undefined {
  if (!podium) return undefined;
  const agent = voice?.agent ?? "";
  const model = voice?.model ?? "";
  const effort = voice?.effort ?? "";
  if (agent === "" && model === "" && effort === "") return podium;
  const namesModel = agent !== "" || model !== "";
  return {
    ...podium,
    agent: agent || podium.agent,
    model: model || podium.model,
    effort: namesModel ? effort : effort || podium.effort,
  };
}

export function canSavePersonality(draft: PersonalityDraft, saved: PersonalityDraft): boolean {
  return isPersonalityDirty(draft, saved) && personalityIssues(draft).length === 0;
}

/** Side lists show the display name, so that is the order. The stored name is only a tie-break. */
export function comparePersonalities(
  a: { name: string; displayName: string },
  b: { name: string; displayName: string },
): number {
  const byDisplay = (a.displayName || a.name).localeCompare(b.displayName || b.name, undefined, {
    sensitivity: "base",
  });
  if (byDisplay !== 0) return byDisplay;
  return a.name.localeCompare(b.name);
}
