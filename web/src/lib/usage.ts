/**
 * The arithmetic behind the usage screen: the ranges it can be asked for, and the money and
 * day keys the figures are rendered with.
 *
 * Every date function here works in the browser's own zone, deliberately. The screen asks
 * the conductor to bucket by the same offset (`tzOffsetMinutes`), so a day in the response
 * and a day on this screen are the same day — the operator's, not the server's.
 */

import type { TaskCost, UsageBackend, UsageDay } from "../gen/podium/agent/v1/agent_pb";

/** The offset the conductor is asked to bucket by: east-positive, unlike the browser's. */
export function tzOffsetMinutes(d = new Date()): number {
  return -d.getTimezoneOffset();
}

/** dayKey is the YYYY-MM-DD the server returns, computed locally so the two agree. */
export function dayKey(d: Date): string {
  const m = `${d.getMonth() + 1}`.padStart(2, "0");
  const day = `${d.getDate()}`.padStart(2, "0");
  return `${d.getFullYear()}-${m}-${day}`;
}

export function startOfDay(d: Date): Date {
  return new Date(d.getFullYear(), d.getMonth(), d.getDate());
}

export function addDays(d: Date, n: number): Date {
  return new Date(d.getFullYear(), d.getMonth(), d.getDate() + n);
}

export function startOfMonth(d: Date): Date {
  return new Date(d.getFullYear(), d.getMonth(), 1);
}

export function addMonths(d: Date, n: number): Date {
  return new Date(d.getFullYear(), d.getMonth() + n, 1);
}

export function dayLabel(d: Date): string {
  return d.toLocaleDateString(undefined, { month: "short", day: "numeric" });
}

/** parseDayKey turns a YYYY-MM-DD back into local midnight. `new Date(key)` would read it
 *  as UTC and land on the day before for anyone west of Greenwich. */
export function parseDayKey(key: string): Date {
  const [y, m, d] = key.split("-").map(Number);
  return new Date(y, m - 1, d);
}

/**
 * RangeID names a preset. `custom` is the one the operator picks two dates for; it is not
 * in PRESETS because it has no fixed span to compute.
 */
export type RangeID = "1d" | "2d" | "7d" | "30d" | "90d" | "month" | "last-month" | "custom";

/**
 * Range is a half-open window: from is inclusive, to is exclusive. Both are local midnights,
 * because every preset here is a whole number of the operator's days — "last 7 days" that
 * started at 14:23 would put two part-days at the ends and make the daily figures lie.
 */
export type Range = { id: RangeID; from: Date; to: Date; label: string };

/** The presets, in the order the picker shows them. */
export const PRESETS: { id: RangeID; label: string; short: string }[] = [
  { id: "1d", label: "Today", short: "1d" },
  { id: "2d", label: "Last 2 days", short: "2d" },
  { id: "7d", label: "Last 7 days", short: "7d" },
  { id: "30d", label: "Last 30 days", short: "30d" },
  { id: "90d", label: "Last 90 days", short: "90d" },
  { id: "month", label: "This month", short: "This month" },
  { id: "last-month", label: "Last month", short: "Last month" },
];

/** The trailing-day presets and how many whole days each covers, today included. */
const DAYS: Partial<Record<RangeID, number>> = { "1d": 1, "2d": 2, "7d": 7, "30d": 30, "90d": 90 };

/**
 * rangeFor builds a preset's window. A trailing range ends at the end of *today* rather than
 * at this instant, so today's spend is in it and the last bucket is a whole day like every
 * other one.
 */
export function rangeFor(id: RangeID, now = new Date()): Range {
  const today = startOfDay(now);
  const days = DAYS[id];
  if (days !== undefined) {
    return {
      id,
      from: addDays(today, -(days - 1)),
      to: addDays(today, 1),
      label: PRESETS.find((p) => p.id === id)?.label ?? `Last ${days} days`,
    };
  }
  if (id === "month") {
    const start = startOfMonth(now);
    return { id, from: start, to: addMonths(start, 1), label: "This month" };
  }
  if (id === "last-month") {
    const start = addMonths(startOfMonth(now), -1);
    return { id, from: start, to: startOfMonth(now), label: "Last month" };
  }
  // A custom id with no dates is meaningless; the last 7 days is the safe answer.
  return rangeFor("7d", now);
}

/**
 * customRange takes the two dates an operator typed, inclusive of both, and normalises them.
 * Dates entered backwards are swapped rather than refused: the intent is never in doubt.
 */
export function customRange(a: Date, b: Date): Range {
  const [lo, hi] = a <= b ? [a, b] : [b, a];
  const from = startOfDay(lo);
  const to = addDays(startOfDay(hi), 1);
  return {
    id: "custom",
    from,
    to,
    label:
      dayKey(from) === dayKey(startOfDay(hi))
        ? dayLabel(from)
        : `${dayLabel(from)} – ${dayLabel(startOfDay(hi))}`,
  };
}

/**
 * previousRange is what the headline figure is compared against: the window immediately
 * before this one, of the same length. "This month" is the exception — the useful comparison
 * there is last month itself, which is a different number of days.
 */
export function previousRange(r: Range): Range {
  if (r.id === "month") return rangeFor("last-month", r.from);
  if (r.id === "last-month") {
    return { id: "last-month", from: addMonths(r.from, -1), to: r.from, label: "the month before" };
  }
  const span = r.to.getTime() - r.from.getTime();
  return { id: r.id, from: new Date(r.from.getTime() - span), to: r.from, label: "the window before" };
}

/** spanDays is how many whole days a range covers. Used to size the daily trend. */
export function spanDays(r: Range): number {
  return Math.max(1, Math.round((r.to.getTime() - r.from.getTime()) / 86_400_000));
}

/**
 * eachDay lists every day in the range, including the ones nothing ran on. The server sends
 * only days that have rows — filling the gaps is the client's job, and a trend with holes
 * punched in it would misread as a trend that dipped.
 */
export function eachDay(r: Range): Date[] {
  const out: Date[] = [];
  for (let d = startOfDay(r.from); d < r.to; d = addDays(d, 1)) out.push(d);
  return out;
}

/** usd is the console's money format. Four decimals, because a turn routinely costs less
 *  than a cent and two would round most of them to nothing. */
export function usd(n: number): string {
  return `$${n.toFixed(4)}`;
}

/** usdShort is for a headline figure, where four decimals is noise rather than precision. */
export function usdShort(n: number): string {
  if (n <= 0) return "$0";
  if (n < 0.01) return `$${n.toFixed(4)}`;
  if (n < 1000) return `$${n.toFixed(2)}`;
  return `$${Math.round(n).toLocaleString()}`;
}

/** byDay indexes the response's day rows. Days nothing ran on are simply absent. */
export function byDay(days: UsageDay[]): Map<string, UsageDay> {
  return new Map(days.map((d) => [d.date, d]));
}

/** totalIn sums the day rows that fall inside a range. */
export function totalIn(days: UsageDay[], r: Range): number {
  return sumIn(days, r, (d) => d.costUsd);
}

/** sumIn adds up one field of every day row inside a range. */
export function sumIn(days: UsageDay[], r: Range, pick: (d: UsageDay) => number): number {
  const lo = dayKey(startOfDay(r.from));
  const hi = dayKey(addDays(startOfDay(r.to), -1));
  return days.reduce((sum, d) => (d.date >= lo && d.date <= hi ? sum + pick(d) : sum), 0);
}

/**
 * byTask indexes costs by the task each turn ran as. A turn whose task was never created
 * has no key to index it under and is dropped: it has no row in the table to join to.
 */
export function byTask(costs: TaskCost[]): Map<string, TaskCost> {
  const out = new Map<string, TaskCost>();
  for (const c of costs) if (c.taskId) out.set(c.taskId, c);
  return out;
}

/** Slice is one row of a breakdown: a name, what it cost, and how many turns it took. */
export type Slice = { key: string; cost: number; turns: number };

/**
 * breakdown groups costs by one of their own fields, largest first. A turn that reported no
 * cost still counts as a turn — dropping it would make a group look cheaper than it was
 * rather than less completely measured.
 */
export function breakdown(costs: TaskCost[], pick: (c: TaskCost) => string): Slice[] {
  const totals = new Map<string, Slice>();
  for (const c of costs) {
    const key = pick(c) || "—";
    const cur = totals.get(key) ?? { key, cost: 0, turns: 0 };
    cur.cost += c.costUsd ?? 0;
    cur.turns += 1;
    totals.set(key, cur);
  }
  return [...totals.values()].sort((a, b) => b.cost - a.cost || b.turns - a.turns);
}

/**
 * isUnrecorded marks the bucket for turns that ran before the conductor wrote down what they
 * ran on. Every field is empty together, which is why one check answers for all of them —
 * and why it is not the same as a recorded turn whose effort is simply the model's default.
 */
export function isUnrecorded(b: UsageBackend): boolean {
  return b.provider === "" && b.agent === "" && b.model === "";
}

/** backendSummary is the sentence above the by-model table. It counts only what is attributed. */
export function backendSummary(backends: UsageBackend[]): string {
  const known = backends.filter((b) => !isUnrecorded(b));
  if (known.length === 0) return "Nothing in this range recorded what it ran on.";
  const models = new Set(known.map((b) => b.model)).size;
  const providers = new Set(known.map((b) => b.provider)).size;
  const spent = usdShort(known.reduce((s, b) => s + b.costUsd, 0));
  return (
    `${models} ${models === 1 ? "model" : "models"} across ` +
    `${providers} ${providers === 1 ? "provider" : "providers"}, ${spent} attributed.`
  );
}

/**
 * ranBy names what spent a turn's money. An empty playbook is not missing data: a
 * conversation is answered by the assistant, which is not a playbook and so never had a
 * name to record on the row.
 */
export function ranBy(playbook: string): string {
  return playbook === "" ? "assistant" : playbook;
}
