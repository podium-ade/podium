import { type Routine, type RoutineDraft } from "../gen/podium/agent/v1/agent_pb";
import { toDate } from "./format";

/** routineKey is where one chat's routine is cached. */
export const routineKey = (chatId: string) => ["agent", "routine", chatId];

/** routinesKey is where the viewer's list of routines is cached. */
export const routinesKey = ["agent", "routines"];

/** draftOf is what the editor starts from for an existing routine. */
export function draftOf(r: Routine): Partial<RoutineDraft> {
  return {
    name: r.name,
    schedule: r.schedule,
    timezone: r.timezone,
    instruction: r.instruction,
    notes: r.notes,
    personalityId: r.personalityId,
    model: r.model,
    effort: r.effort,
    repos: [...r.repos],
    dailyCapUsd: r.dailyCapUsd,
  };
}

/** Scope is who a routine runs as: a person, or nobody (a system routine admins set up). */
export type Scope = "personal" | "system";

/** isSystem is a routine that runs as nobody, configured by admins. */
export function isSystem(r: Pick<Routine, "scope">): boolean {
  return r.scope === "system";
}

/** REPO_PATTERN is a repository as GitHub names one: owner/name. */
const REPO_PATTERN = /^[A-Za-z0-9][A-Za-z0-9-]*\/[A-Za-z0-9._-]+$/;

/** parseRepo reads a repository as typed — owner/name, or its github.com address — or null. */
export function parseRepo(text: string): string | null {
  const r = text
    .trim()
    .replace(/^https?:\/\/github\.com\//i, "")
    .replace(/\.git$/i, "")
    .replace(/^\/+|\/+$/g, "");
  return REPO_PATTERN.test(r) ? r : null;
}

/**
 * CI_FIXER is the system routine phase 2 is proven on: it starts out only proposing, and an
 * admin lets it act by editing the instruction. The guard rails are written into it.
 */
export const CI_FIXER: Pick<RoutineDraft, "name" | "instruction" | "notes" | "schedule"> = {
  name: "Fix failing CI",
  schedule: "*/10 * * * *",
  instruction: [
    "Look at the open pull requests in this routine's repositories for failed CI checks.",
    "",
    "For each pull request whose latest commit has a failed check:",
    "1. Skip it if the same check is also failing on its base branch — the fault is not in the pull request. Say so once.",
    "2. Skip it if the state shows you already handled this commit (key: owner/repo#number@sha).",
    "3. Skip it, and say you are giving up, if the state shows 3 attempts on this pull request already.",
    "4. Read the failed job's log and work out the smallest change that fixes it.",
    "5. Propose it: comment on the pull request with what failed, why, and the change you would make. Do not push anything.",
    "",
    "Record every commit you handled and the attempts per pull request in the state. Finish idle when nothing failed since the last run.",
  ].join("\n"),
  notes: "- Never fix CI by disabling a lint rule or skipping a test.",
};

/** routineChatPath is the address of a routine's chat, under its assistant when it has one. */
export function routineChatPath(r: Pick<Routine, "personalityId" | "chatId">): string {
  return r.personalityId ? `/agent/chat/a/${r.personalityId}/${r.chatId}` : `/agent/chat/${r.chatId}`;
}

/** upcoming says a future moment the way a person reads one: "today 09:00", "Tue 09:00", "Nov 18 09:00". */
export function upcoming(ts: Parameters<typeof toDate>[0], now = new Date()): string {
  const d = toDate(ts);
  if (!d) return "—";
  // 24-hour, as the schedule itself is said ("weekdays at 09:00"), so the two read alike.
  const time = d.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit", hourCycle: "h23" });
  const days = Math.floor((new Date(d).setHours(0, 0, 0, 0) - new Date(now).setHours(0, 0, 0, 0)) / 86_400_000);
  if (days <= 0) return `today ${time}`;
  if (days === 1) return `tomorrow ${time}`;
  if (days < 7) return `${d.toLocaleDateString(undefined, { weekday: "short" })} ${time}`;
  return `${d.toLocaleDateString(undefined, { month: "short", day: "numeric" })} ${time}`;
}

/** money is a small spend, in dollars. */
export function money(usd: number): string {
  if (usd <= 0) return "$0";
  return usd < 0.01 ? "<$0.01" : `$${usd.toFixed(2)}`;
}

/** routineStatus is a routine's state in a few words: running, its next run, or why it is paused. */
export function routineStatus(r: Routine): string {
  if (r.running) return "Running now";
  if (r.enabled) return `Next ${upcoming(r.nextRunAt)}`;
  if (r.pausedUntil) return `Paused until ${upcoming(r.pausedUntil)}`;
  return r.pausedReason ? "Paused by Podium" : "Paused";
}

/** browserZone is where the person is, which is what most routines mean by "09:00". */
export function browserZone(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
  } catch {
    return "UTC";
  }
}
