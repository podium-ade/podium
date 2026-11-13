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
    dailyCapUsd: r.dailyCapUsd,
    trigger: r.trigger,
    events: r.events,
    teams: r.teams,
    chatMode: r.chatMode,
  };
}

/** Trigger is what starts a routine's runs: its schedule, or Linear events. */
export type Trigger = "schedule" | "linear";

/** ChatMode is where a routine on Linear events runs: all in its own chat, or a chat per ticket. */
export type ChatMode = "routine" | "subject";

/** perTicket is a routine on Linear events whose runs each happen in the chat of the ticket they are about. */
export function perTicket(r: Pick<Routine, "trigger" | "chatMode">): boolean {
  return isLinear(r) && r.chatMode === "subject";
}

/** isLinear is a routine that runs on Linear events instead of a schedule. */
export function isLinear(r: Pick<Routine, "trigger">): boolean {
  return r.trigger === "linear";
}

/** LINEAR_TYPES are the Linear entities a routine can run on, as the server lists them, with their names in words. */
export const LINEAR_TYPES: { type: string; label: string }[] = [
  { type: "Issue", label: "Issue" },
  { type: "Comment", label: "Comment" },
  { type: "Project", label: "Project" },
  { type: "ProjectUpdate", label: "Project update" },
  { type: "Cycle", label: "Cycle" },
  { type: "IssueLabel", label: "Issue label" },
];

/** LINEAR_ACTIONS are what can happen to one, as Linear names it, with the word a label uses. */
export const LINEAR_ACTIONS: { action: string; label: string }[] = [
  { action: "create", label: "created" },
  { action: "update", label: "updated" },
  { action: "remove", label: "removed" },
];

/** LINEAR_EVENTS is every event a routine can run on, "Issue.create" each, with a plain label: "Issue created". */
export const LINEAR_EVENTS: { event: string; label: string }[] = LINEAR_TYPES.flatMap((t) =>
  LINEAR_ACTIONS.map((a) => ({ event: `${t.type}.${a.action}`, label: `${t.label} ${a.label}` })),
);

/** teamKeys reads the Teams field: comma- or space-separated keys, upper case, each once. */
export function teamKeys(text: string): string[] {
  const keys = text
    .split(/[\s,]+/)
    .map((k) => k.trim().toUpperCase())
    .filter((k) => k !== "");
  return [...new Set(keys)];
}

/** runsText says when a routine runs: "Runs every day at 09:00", "Runs on Linear: issue created (ENG)". */
export function runsText(r: Pick<Routine, "trigger" | "scheduleText">): string {
  if (isLinear(r)) {
    return `Runs ${r.scheduleText.charAt(0).toLowerCase()}${r.scheduleText.slice(1)}`;
  }
  return `Runs ${r.scheduleText}`;
}

/** waitingText is how many Linear events wait for a routine's next run, in words. */
export function waitingText(n: number): string {
  if (n <= 0) return "none";
  return n === 1 ? "1 event" : `${n} events`;
}

/** Scope is who a routine runs as: a person, or nobody (a system routine admins set up). */
export type Scope = "personal" | "system";

/** isSystem is a routine that runs as nobody, configured by admins. */
export function isSystem(r: Pick<Routine, "scope">): boolean {
  return r.scope === "system";
}

/**
 * CI_FIXER is the system routine phase 2 is proven on: it starts out only proposing, and an
 * admin lets it act by editing the instruction. The guard rails are written into it.
 */
export const CI_FIXER: Pick<RoutineDraft, "name" | "instruction" | "notes" | "schedule"> = {
  name: "Fix failing CI",
  schedule: "*/10 * * * *",
  instruction: [
    "Look at the open pull requests in [name the repositories, like \"the API and the web app\"] for failed CI checks.",
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
  if (r.enabled && isLinear(r)) {
    return r.pendingEvents > 0 ? `${waitingText(r.pendingEvents)} waiting` : "Waiting for Linear events";
  }
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
