/**
 * The Assistant screen edits the active assistant and saves it whole.
 *
 * The request is the definition, not a diff against a file. An empty skill list and a
 * step cap of zero are real values. UpdateProfile replaces the active definition, so
 * every save sends all of it. The id is empty until the first save, and is sent back
 * after that so a save keeps the same definition.
 */

export const DEFAULT_AGENT = "claude";
export const DEFAULT_TIMEOUT = "15m0s";
/** The brief that carries a turn is capped. A prompt past this cannot be saved. */
export const MAX_PROMPT_BYTES = 32 * 1024;

const NAME_RE = /^[a-z][a-z0-9-]{0,31}$/;

export const ASSISTANT_SECTIONS = ["identity", "instructions", "model", "reach", "limits"] as const;

export type AssistantSection = (typeof ASSISTANT_SECTIONS)[number];

export type AssistantDraft = {
  id: string;
  name: string;
  displayName: string;
  systemPrompt: string;
  agent: string;
  model: string;
  effort: string;
  skills: string[];
  mcpServers: string[];
  /** 0 is no cap. Negative means the cap field was cleared and is not saveable. */
  maxTurns: number;
  timeout: string;
  gitName: string;
  gitEmail: string;
};

/** The profile fields the screen reads. The install-file fields on the proto are ignored. */
export type AssistantProfileView = {
  id: string;
  name: string;
  displayName: string;
  systemPrompt: string;
  model: string;
  agent: string;
  effort: string;
  skills: string[];
  mcpServers: string[];
  maxTurns: number;
  timeout: string;
  gitName: string;
  gitEmail: string;
  updatedBy: string;
};

/** What UpdateProfile stores. Every field is the definition. */
export type ProfileUpdate = {
  id: string;
  name: string;
  displayName: string;
  model: string;
  agent: string;
  effort: string;
  systemPrompt: string;
  skills: string[];
  mcpServers: string[];
  maxTurns: number;
  timeout: string;
  gitName: string;
  gitEmail: string;
};

export type DraftIssue = "name" | "display-name" | "prompt" | "prompt-size" | "model" | "timeout" | "cap" | "git";

type ProfileLike = Partial<AssistantProfileView> & Pick<AssistantProfileView, "displayName" | "model">;

export function viewFromProfile(p: ProfileLike): AssistantProfileView {
  return {
    id: p.id ?? "",
    name: p.name ?? "",
    displayName: p.displayName,
    systemPrompt: p.systemPrompt ?? "",
    model: p.model,
    agent: p.agent ?? "",
    effort: p.effort ?? "",
    skills: p.skills ?? [],
    mcpServers: p.mcpServers ?? [],
    maxTurns: p.maxTurns ?? 0,
    timeout: p.timeout ?? "",
    gitName: p.gitName ?? "",
    gitEmail: p.gitEmail ?? "",
    updatedBy: p.updatedBy ?? "",
  };
}

export function draftFromProfile(p: AssistantProfileView, defaultAgent = DEFAULT_AGENT): AssistantDraft {
  return {
    id: p.id,
    name: p.name,
    displayName: p.displayName,
    systemPrompt: p.systemPrompt,
    agent: p.agent || defaultAgent,
    model: p.model,
    effort: p.effort,
    skills: [...p.skills],
    mcpServers: [...p.mcpServers],
    maxTurns: p.maxTurns,
    timeout: p.timeout || DEFAULT_TIMEOUT,
    gitName: p.gitName,
    gitEmail: p.gitEmail,
  };
}

const UNIT_NS: Record<string, number> = {
  ns: 1,
  us: 1_000,
  µs: 1_000,
  μs: 1_000,
  ms: 1_000_000,
  s: 1_000_000_000,
  m: 60_000_000_000,
  h: 3_600_000_000_000,
};

const DURATION_PART = /(\d+(?:\.\d*)?|\.\d+)(ns|us|µs|μs|ms|s|m|h)/y;

/** parseGoDuration reads a Go duration string. Null means it is not one. */
export function parseGoDuration(raw: string): number | null {
  let s = raw.trim();
  if (!s) return null;
  let sign = 1;
  if (s.startsWith("+") || s.startsWith("-")) {
    if (s.startsWith("-")) sign = -1;
    s = s.slice(1);
    if (!s) return null;
  }
  let total = 0;
  let i = 0;
  while (i < s.length) {
    DURATION_PART.lastIndex = i;
    const m = DURATION_PART.exec(s);
    if (!m || m.index !== i) return null;
    const n = Number(m[1]);
    const unit = UNIT_NS[m[2]];
    if (!Number.isFinite(n) || unit === undefined) return null;
    total += n * unit;
    i = DURATION_PART.lastIndex;
  }
  return sign * total;
}

export function durationsEqual(a: string, b: string): boolean {
  const da = parseGoDuration(a);
  const db = parseGoDuration(b);
  if (da === null || db === null) return a.trim() === b.trim();
  return da === db;
}

function sameNames(a: string[], b: string[]): boolean {
  return a.length === b.length && a.every((name, i) => name === b[i]);
}

function gitEqual(a: AssistantDraft, b: AssistantDraft): boolean {
  return a.gitName.trim() === b.gitName.trim() && a.gitEmail.trim() === b.gitEmail.trim();
}

export function updateFromDraft(draft: AssistantDraft): ProfileUpdate {
  return {
    id: draft.id,
    name: draft.name.trim(),
    displayName: draft.displayName.trim(),
    model: draft.model.trim(),
    agent: draft.agent,
    effort: draft.effort,
    systemPrompt: draft.systemPrompt.trim(),
    skills: draft.skills,
    mcpServers: draft.mcpServers,
    maxTurns: draft.maxTurns < 0 ? 0 : draft.maxTurns,
    timeout: draft.timeout.trim(),
    gitName: draft.gitName.trim(),
    gitEmail: draft.gitEmail.trim(),
  };
}

function samePayload(a: ProfileUpdate, b: ProfileUpdate): boolean {
  return (
    a.id === b.id &&
    a.name === b.name &&
    a.displayName === b.displayName &&
    a.model === b.model &&
    a.agent === b.agent &&
    a.effort === b.effort &&
    a.systemPrompt === b.systemPrompt &&
    sameNames(a.skills, b.skills) &&
    sameNames(a.mcpServers, b.mcpServers) &&
    a.maxTurns === b.maxTurns &&
    durationsEqual(a.timeout, b.timeout) &&
    a.gitName === b.gitName &&
    a.gitEmail === b.gitEmail
  );
}

/** isDirty compares save payloads, so 15m and 15m0s are the same clock. */
export function isDirty(draft: AssistantDraft, saved: AssistantDraft): boolean {
  return !samePayload(updateFromDraft(draft), updateFromDraft(saved));
}

export function gitIssue(draft: AssistantDraft): string | null {
  const name = draft.gitName.trim();
  const email = draft.gitEmail.trim();
  if (!name && !email) return null;
  if (!name || !email) return "Git needs both a name and an email, or neither.";
  if (/[<>\n]/.test(name) || /[<>\n]/.test(email)) {
    return "Git name and email cannot contain <, >, or a newline.";
  }
  if (!email.includes("@")) return "Git email needs an @ so GitHub can match an account.";
  return null;
}

function promptBytes(text: string): number {
  return new TextEncoder().encode(text).length;
}

export function draftIssues(draft: AssistantDraft): DraftIssue[] {
  const out: DraftIssue[] = [];
  if (!NAME_RE.test(draft.name.trim())) out.push("name");
  if (!draft.displayName.trim()) out.push("display-name");
  const prompt = draft.systemPrompt.trim();
  if (!prompt) out.push("prompt");
  else if (promptBytes(prompt) > MAX_PROMPT_BYTES) out.push("prompt-size");
  if (!draft.model.trim()) out.push("model");
  const timeout = parseGoDuration(draft.timeout);
  if (timeout === null || timeout <= 0) out.push("timeout");
  if (draft.maxTurns < 0) out.push("cap");
  if (gitIssue(draft)) out.push("git");
  return out;
}

export function canSave(draft: AssistantDraft, saved: AssistantDraft): boolean {
  return isDirty(draft, saved) && draftIssues(draft).length === 0;
}

export function dirtySections(draft: AssistantDraft, saved: AssistantDraft): AssistantSection[] {
  const out: AssistantSection[] = [];
  if (
    draft.name.trim() !== saved.name.trim() ||
    draft.displayName.trim() !== saved.displayName.trim() ||
    !gitEqual(draft, saved)
  ) {
    out.push("identity");
  }
  if (draft.systemPrompt.trim() !== saved.systemPrompt.trim()) out.push("instructions");
  if (draft.agent !== saved.agent || draft.model.trim() !== saved.model.trim() || draft.effort !== saved.effort) {
    out.push("model");
  }
  if (!sameNames(draft.skills, saved.skills) || !sameNames(draft.mcpServers, saved.mcpServers)) out.push("reach");
  if (draft.maxTurns !== saved.maxTurns || !durationsEqual(draft.timeout, saved.timeout)) out.push("limits");
  return out;
}

export function isAssistantSection(id: string): id is AssistantSection {
  return (ASSISTANT_SECTIONS as readonly string[]).includes(id);
}

/** assistantSection reads the path. The bare Assistants address, and the old profile address, are Identity. */
export function assistantSection(pathname: string): AssistantSection | null {
  if (pathname === "/agent/assistants" || pathname === "/agent/assistants/") return "identity";
  if (pathname.startsWith("/agent/assistants/podium")) {
    const id = pathname.replace(/^\/agent\/assistants\/podium\/?/, "").split("/").filter(Boolean)[0] ?? "";
    return id ? (isAssistantSection(id) ? id : null) : "identity";
  }
  if (!pathname.startsWith("/agent/profile")) return null;
  const id = pathname.replace(/^\/agent\/profile\/?/, "").split("/").filter(Boolean)[0] ?? "";
  if (!id) return "identity";
  return isAssistantSection(id) ? id : null;
}

export function assistantSectionHref(id: AssistantSection): string {
  return id === "identity" ? "/agent/assistants" : `/agent/assistants/podium/${id}`;
}
