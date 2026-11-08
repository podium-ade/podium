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
  };
}

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
