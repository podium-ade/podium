/**
 * The arithmetic behind the usage screen: the ranges it can be asked for, and the money and
 * day keys the figures are rendered with.
 *
 * Every date function here works in the browser's own zone, deliberately. The screen asks
 * the conductor to bucket by the same offset (`tzOffsetMinutes`), so a day in the response
 * and a day on this screen are the same day — the operator's, not the server's.
 */

import type { MessageInitShape } from "@bufbuild/protobuf";
import { UsageFilterSchema } from "../gen/podium/agent/v1/agent_pb";
import type {
  TaskCost,
  UsageBackend,
  UsageDay,
  UsageGroup,
  UsageTotals,
} from "../gen/podium/agent/v1/agent_pb";

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
/** lastDay is the last day a half-open range covers: the day before its exclusive end. */
export function lastDay(r: { to: Date }): Date {
  return addDays(r.to, -1);
}

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
  // In days, not milliseconds: a window across a clock change is an hour longer or shorter
  // than its days, and the one before it would start an hour off midnight.
  return { id: r.id, from: addDays(r.from, -spanDays(r)), to: r.from, label: "the window before" };
}

/** spanDays is how many whole days a range covers. Rounded, because a day across a clock
 *  change is 23 or 25 hours. */
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

/**
 * Totals is UsageTotals with plain numbers. The wire type carries tokens as bigint (int64),
 * which arithmetic and toLocaleString both want as a number; no real range is near 2^53.
 */
export type Totals = {
  cost: number;
  subscription: number;
  turns: number;
  modelTurns: number;
  unpriced: number;
  input: number;
  output: number;
  reasoning: number;
  cacheRead: number;
  cacheWrite: number;
};

export const ZERO: Totals = {
  cost: 0,
  subscription: 0,
  turns: 0,
  modelTurns: 0,
  unpriced: 0,
  input: 0,
  output: 0,
  reasoning: 0,
  cacheRead: 0,
  cacheWrite: 0,
};

/** totals reads a UsageTotals, which a server from before it existed leaves unset. */
export function totals(t: UsageTotals | undefined): Totals {
  if (!t) return ZERO;
  return {
    cost: t.costUsd,
    subscription: t.subscriptionCostUsd,
    turns: t.turns,
    modelTurns: t.modelTurns,
    unpriced: t.unpriced,
    input: Number(t.inputTokens),
    output: Number(t.outputTokens),
    reasoning: Number(t.reasoningTokens),
    cacheRead: Number(t.cacheReadTokens),
    cacheWrite: Number(t.cacheWriteTokens),
  };
}

export function addTotals(a: Totals, b: Totals): Totals {
  return {
    cost: a.cost + b.cost,
    subscription: a.subscription + b.subscription,
    turns: a.turns + b.turns,
    modelTurns: a.modelTurns + b.modelTurns,
    unpriced: a.unpriced + b.unpriced,
    input: a.input + b.input,
    output: a.output + b.output,
    reasoning: a.reasoning + b.reasoning,
    cacheRead: a.cacheRead + b.cacheRead,
    cacheWrite: a.cacheWrite + b.cacheWrite,
  };
}

/**
 * dayTotals is one day row's totals. A day row from a server that predates UsageTotals still
 * has its cost and counts, so those are what it falls back to, with no subscription share.
 */
export function dayTotals(d: UsageDay): Totals {
  return groupTotals(d);
}

/**
 * groupTotals is a by-model or by-person row's totals, falling back like dayTotals for a
 * server that sent only the older cost and count fields.
 */
export function groupTotals(g: {
  totals?: UsageTotals;
  costUsd: number;
  turns: number;
  modelTurns: number;
  unpriced: number;
}): Totals {
  if (g.totals) return totals(g.totals);
  return { ...ZERO, cost: g.costUsd, turns: g.turns, modelTurns: g.modelTurns, unpriced: g.unpriced };
}

/** totalsIn adds up every day row inside a range. */
export function totalsIn(days: UsageDay[], r: Range): Totals {
  const lo = dayKey(startOfDay(r.from));
  const hi = dayKey(addDays(startOfDay(r.to), -1));
  return days.reduce((sum, d) => (d.date >= lo && d.date <= hi ? addTotals(sum, dayTotals(d)) : sum), ZERO);
}

/**
 * billed is what was actually charged: everything reported, less what a subscription covered.
 * A subscription turn's cost is the harness's list-price figure for the same work, which is
 * worth knowing and worth showing, but it is not money that left the company.
 */
export function billed(t: Totals): number {
  return Math.max(0, t.cost - t.subscription);
}

/** tokens is a token count for a cell: 950, 12.4k, 3.1M. */
export function tokens(n: number): string {
  if (n < 1000) return `${Math.round(n)}`;
  if (n < 1_000_000) return `${trim(n / 1000)}k`;
  if (n < 1_000_000_000) return `${trim(n / 1_000_000)}M`;
  return `${trim(n / 1_000_000_000)}B`;
}

function trim(n: number): string {
  return n >= 100 ? `${Math.round(n)}` : n.toFixed(1).replace(/\.0$/, "");
}

/** optTokens is a row's own token count, where unset means the runtime reported none. */
export function optTokens(n: bigint | undefined): number | undefined {
  return n === undefined ? undefined : Number(n);
}

/** ORIGINS is how each origin (TaskCost.origin) is named on the screen. */
export const ORIGINS: Record<string, string> = {
  chat: "Podium chat",
  slack: "Slack",
  github: "GitHub",
  linear: "Linear",
  routine: "Routine",
  housekeeping: "Housekeeping",
  dev: "Dev",
};

export function originLabel(origin: string): string {
  return ORIGINS[origin] ?? (origin || "Unknown");
}

/** markFor is the OriginMark glyph for an origin: a Podium chat is drawn as Podium's own. */
export function markFor(origin: string): string {
  return origin === "chat" ? "web" : origin;
}

/** BACKEND_MARK is the BackendMark id for each provider a turn can be billed by. */
const BACKEND_MARK: Record<string, string> = {
  anthropic: "claude",
  openai: "openai",
  xai: "grok",
  google: "gemini",
};

/** backendMark is the BackendMark id for a provider, or "" for one it has no mark for. */
export function backendMark(provider: string): string {
  return BACKEND_MARK[provider] ?? "";
}

export const PROVIDERS: Record<string, string> = {
  anthropic: "Anthropic",
  openai: "OpenAI",
  xai: "xAI",
  google: "Google",
};

export function providerLabel(provider: string): string {
  return PROVIDERS[provider] ?? provider;
}

/** authLabel names how a credential was obtained, or says the row predates recording it. */
export function authLabel(authKind: string): string {
  if (authKind === "subscription") return "Subscription";
  if (authKind === "api_key") return "API key";
  return "Unrecorded";
}

/** ownerLabel is whose credential paid: a person's own, or the company's. */
export function ownerLabel(owner: string): string {
  return owner === "" ? "Company" : owner;
}

/**
 * credentialKey is the UsageGroup key the server gives a credential and a filter takes back:
 * `auth_kind|provider|owner`. Built here only to match a row to its group.
 */
export function credentialKey(authKind: string, provider: string, owner: string): string {
  return `${authKind}|${provider}|${owner}`;
}

/** credentialLabel is one line naming what paid, for a filter or a table row. */
export function credentialLabel(g: Pick<UsageGroup, "authKind" | "provider" | "owner">): string {
  if (g.authKind === "") return "Unrecorded";
  const who = ownerLabel(g.owner);
  const kind = g.authKind === "subscription" ? "subscription" : authLabel(g.authKind);
  const what = `${providerLabel(g.provider)} ${kind}`;
  return `${what} · ${who}`;
}

/** ROUTINE_PREFIX is turns.started_by for a system routine's run, which nobody started. */
const ROUTINE_PREFIX = "routine:";

/** routineOf is the routine id behind a started_by, or "" when a person started it. */
export function routineOf(startedBy: string): string {
  return startedBy.startsWith(ROUTINE_PREFIX) ? startedBy.slice(ROUTINE_PREFIX.length) : "";
}

/** started_by values that are not a person, and what to call them. */
export const NOT_A_PERSON: Record<string, { label: string; hint: string }> = {
  "": {
    label: "Unattributed",
    hint: "Turns from before the conductor recorded who started them, the conductor's own housekeeping, and turns started with the shared dev token.",
  },
  "github-app": {
    label: "GitHub App",
    hint: "Automatic reviews, and review commands from people outside Podium. They run as the App, so nobody's account pays for them.",
  },
};

/**
 * personLabel names whoever a started_by is: a login, the GitHub App, nobody, or a system
 * routine by its name when the range's routines say what it is called.
 */
export function personLabel(startedBy: string, routines: Map<string, string> = new Map()): string {
  const special = NOT_A_PERSON[startedBy];
  if (special) return special.label;
  const routine = routineOf(startedBy);
  if (routine) return routines.get(routine) ?? "System routine";
  return startedBy;
}

/** isPerson is whether a started_by is somebody's login rather than a stand-in. */
export function isPerson(startedBy: string): boolean {
  return !(startedBy in NOT_A_PERSON) && routineOf(startedBy) === "";
}

/** routineNames indexes the response's routine groups by id. */
export function routineNames(groups: UsageGroup[]): Map<string, string> {
  return new Map(groups.map((g) => [g.key, g.label]));
}

/** KIND_LABELS is what each kind of spend row is called. */
export const KIND_LABELS: Record<string, string> = {
  turn: "Turn",
  delegation: "Task",
  summary: "Summary",
};

export function kindLabel(kind: string): string {
  return KIND_LABELS[kind] ?? kind;
}

/**
 * Filters is the table's narrowing as the URL holds it. Every field is "" for "any", and the
 * same strings go to the server's UsageFilter unchanged.
 */
export type Filters = { origin: string; person: string; model: string; credential: string };

export const NO_FILTERS: Filters = { origin: "", person: "", model: "", credential: "" };

/** FILTER_PARAMS are the search params each filter lives under, so a link can share a view. */
export const FILTER_PARAMS: Record<keyof Filters, string> = {
  origin: "source",
  person: "person",
  model: "model",
  credential: "paid",
};

export function readFilters(p: URLSearchParams): Filters {
  return {
    origin: p.get(FILTER_PARAMS.origin) ?? "",
    person: p.get(FILTER_PARAMS.person) ?? "",
    model: p.get(FILTER_PARAMS.model) ?? "",
    credential: p.get(FILTER_PARAMS.credential) ?? "",
  };
}

export function isFiltered(f: Filters): boolean {
  return f.origin !== "" || f.person !== "" || f.model !== "" || f.credential !== "";
}

/** toUsageFilter is the request's filter. A person filter of "-" means nobody recorded. */
export function toUsageFilter(f: Filters): MessageInitShape<typeof UsageFilterSchema> {
  return { origin: f.origin, startedBy: f.person, model: f.model, credential: f.credential };
}

/** rowCost is a spend row's cost split into what was billed and what a subscription covered. */
export function rowCost(c: TaskCost): { billed?: number; covered?: number } {
  if (c.costUsd === undefined) return {};
  return c.authKind === "subscription" ? { covered: c.costUsd } : { billed: c.costUsd };
}

/** hrefOf is where a spend row opens: its task, else its conversation, else nowhere. */
export function hrefOf(c: TaskCost): string {
  if (c.taskId) return `/tasks/${c.taskId}`;
  if (c.chatId) return `/agent/chat/${c.chatId}`;
  return "";
}
