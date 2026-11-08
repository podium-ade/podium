/**
 * A routine's schedule as a person builds it — every N minutes, hourly, on chosen days at a
 * time, or monthly on a day — and the cron expression the server stores. Anything the builder
 * cannot say is written as cron directly.
 */

/** Day is a day of the week as cron counts it: 0 Sunday … 6 Saturday. */
export type Day = 0 | 1 | 2 | 3 | 4 | 5 | 6;

export const ALL_DAYS: Day[] = [0, 1, 2, 3, 4, 5, 6];
export const WEEKDAYS: Day[] = [1, 2, 3, 4, 5];

/** The intervals offered: each divides the hour or the day evenly, and none is under 15 minutes. */
export const MINUTE_STEPS = [15, 20, 30] as const;
/** A system routine may run as often as every minute: its spend cap, not its floor, bounds it. */
export const SYSTEM_MINUTE_STEPS = [1, 5, 10, 15, 20, 30] as const;
/** Every step that divides the hour, which is what Repeat can read back from cron. */
const READABLE_MINUTE_STEPS = [1, 2, 3, 4, 5, 6, 10, 12, 15, 20, 30];
export const HOUR_STEPS = [1, 2, 3, 4, 6, 8, 12] as const;

export type Repeat =
  | { kind: "minutes"; every: number; days: Day[] }
  | { kind: "hourly"; every: number; minute: number; days: Day[] }
  | { kind: "daily"; hour: number; minute: number; days: Day[] }
  | { kind: "monthly"; day: number; hour: number; minute: number };

export type Kind = Repeat["kind"];

/** DEFAULT_REPEAT is weekdays at 09:00, the schedule most routines want. */
export const DEFAULT_REPEAT: Repeat = { kind: "daily", hour: 9, minute: 0, days: WEEKDAYS };

/** switchKind keeps what carries over (the time, the days) when the frequency changes. */
export function switchKind(r: Repeat, kind: Kind): Repeat {
  const days = "days" in r ? r.days : ALL_DAYS;
  const hour = "hour" in r ? r.hour : 9;
  const minute = "minute" in r ? r.minute : 0;
  switch (kind) {
    case "minutes":
      return { kind, every: 30, days };
    case "hourly":
      return { kind, every: 1, minute, days };
    case "daily":
      return { kind, hour, minute, days };
    case "monthly":
      return { kind, day: 1, hour, minute };
  }
}

// --- to cron --------------------------------------------------------------------------------

function dayField(days: Day[]): string {
  const set = [...new Set(days)].sort((a, b) => a - b);
  if (set.length === 0 || set.length === 7) return "*";
  // Runs of three or more read better as a range: 1-5, not 1,2,3,4,5.
  const parts: string[] = [];
  for (let i = 0; i < set.length; ) {
    let j = i;
    while (j + 1 < set.length && set[j + 1] === set[j]! + 1) j++;
    parts.push(j - i >= 2 ? `${set[i]}-${set[j]}` : set.slice(i, j + 1).join(","));
    i = j + 1;
  }
  return parts.join(",");
}

export function toCron(r: Repeat): string {
  switch (r.kind) {
    case "minutes":
      return `*/${r.every} * * * ${dayField(r.days)}`;
    case "hourly":
      return `${r.minute} ${r.every === 1 ? "*" : `*/${r.every}`} * * ${dayField(r.days)}`;
    case "daily":
      return `${r.minute} ${r.hour} * * ${dayField(r.days)}`;
    case "monthly":
      return `${r.minute} ${r.hour} ${r.day} * *`;
  }
}

// --- from cron ------------------------------------------------------------------------------

function num(s: string, min: number, max: number): number | null {
  if (!/^\d{1,2}$/.test(s)) return null;
  const n = Number(s);
  return n >= min && n <= max ? n : null;
}

function parseDays(field: string): Day[] | null {
  if (field === "*") return ALL_DAYS;
  const out = new Set<Day>();
  for (const part of field.split(",")) {
    const range = /^(\d)-(\d)$/.exec(part);
    if (range) {
      const a = Number(range[1]);
      const b = Number(range[2]);
      if (a > b || b > 7) return null;
      for (let d = a; d <= b; d++) out.add((d % 7) as Day);
      continue;
    }
    const d = num(part, 0, 7);
    if (d === null) return null;
    out.add((d % 7) as Day);
  }
  return [...out].sort((a, b) => a - b);
}

/** fromCron reads an expression the builder can show, or null for anything else. */
export function fromCron(expr: string): Repeat | null {
  const f = expr.trim().split(/\s+/);
  if (f.length !== 5) return null;
  const [min, hour, dom, mon, dow] = f as [string, string, string, string, string];
  if (mon !== "*") return null;
  const days = parseDays(dow);
  if (!days) return null;

  const step = /^\*\/(\d+)$/.exec(min);
  if (step && hour === "*" && dom === "*") {
    const every = Number(step[1]);
    return READABLE_MINUTE_STEPS.includes(every) ? { kind: "minutes", every, days } : null;
  }
  const minute = num(min, 0, 59);
  if (minute === null) return null;
  if (dom === "*") {
    if (hour === "*") return { kind: "hourly", every: 1, minute, days };
    const hstep = /^\*\/(\d+)$/.exec(hour);
    if (hstep) {
      const every = Number(hstep[1]);
      return (HOUR_STEPS as readonly number[]).includes(every) ? { kind: "hourly", every, minute, days } : null;
    }
    const h = num(hour, 0, 23);
    return h === null ? null : { kind: "daily", hour: h, minute, days };
  }
  const day = num(dom, 1, 31);
  const h = num(hour, 0, 23);
  if (day === null || h === null || dow !== "*") return null;
  return { kind: "monthly", day, hour: h, minute };
}

// --- in words -------------------------------------------------------------------------------

const DAY_SHORT = ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"];

export function hhmm(hour: number, minute: number): string {
  return `${String(hour).padStart(2, "0")}:${String(minute).padStart(2, "0")}`;
}

function ordinal(n: number): string {
  const s = n % 100 >= 11 && n % 100 <= 13 ? "th" : (["th", "st", "nd", "rd"][n % 10] ?? "th");
  return `${n}${s}`;
}

function list(items: string[]): string {
  return items.length <= 1 ? items.join("") : `${items.slice(0, -1).join(", ")} and ${items.at(-1)}`;
}

/** onDays says which days, or "" for every day: "on weekdays", "on weekends", "on Mon, Wed and Fri". */
function onDays(days: Day[]): string {
  const key = dayField(days);
  if (key === "*") return "";
  if (key === "1-5") return "on weekdays";
  if (key === "0,6") return "on weekends";
  return `on ${list([...days].sort((a, b) => a - b).map((d) => DAY_SHORT[d]!))}`;
}

/** describe says a schedule in a sentence: "Weekdays at 09:00", "Every 2 hours at :30 on weekends". */
export function describe(r: Repeat): string {
  switch (r.kind) {
    case "minutes":
      return [r.every === 1 ? "Every minute" : `Every ${r.every} minutes`, onDays(r.days)].filter(Boolean).join(" ");
    case "hourly":
      return [
        `${r.every === 1 ? "Every hour" : `Every ${r.every} hours`} at :${String(r.minute).padStart(2, "0")}`,
        onDays(r.days),
      ]
        .filter(Boolean)
        .join(" ");
    case "daily": {
      const at = `at ${hhmm(r.hour, r.minute)}`;
      const key = dayField(r.days);
      if (key === "*") return `Every day ${at}`;
      if (key === "1-5") return `Weekdays ${at}`;
      if (key === "0,6") return `Weekends ${at}`;
      return `${list([...r.days].sort((a, b) => a - b).map((d) => DAY_SHORT[d]!))} ${at}`;
    }
    case "monthly":
      return `Monthly on the ${ordinal(r.day)} at ${hhmm(r.hour, r.minute)}`;
  }
}

/** checkCron is the obvious mistakes in a cron expression, said before the server is asked; "" when it looks right. */
export function checkCron(expr: string): string {
  const f = expr.trim().split(/\s+/).filter(Boolean);
  if (f.length === 0) return "Write a cron expression, or build the schedule under Repeat.";
  if (f.length !== 5) return `A cron expression has 5 fields; this has ${f.length}.`;
  if (!f.every((x) => /^[\d*/,\-A-Za-z]+$/.test(x))) return "Use digits, *, /, - and , only.";
  return "";
}

/**
 * parseTime reads a time the way people type one: "9", "9:05", "905", "17.30", "5pm",
 * "5:30 pm", "12am". Null when it is not a time.
 */
export function parseTime(text: string): { hour: number; minute: number } | null {
  const q = text.trim().toLowerCase().replace(/\s+/g, "");
  const m = /^(\d{1,2})(?:[:.h]?(\d{2}))?(am|pm|a|p)?$/.exec(q) ?? /^(\d{1,2})[:.h](\d)(am|pm|a|p)?$/.exec(q);
  if (!m) return null;
  let hour = Number(m[1]);
  const minute = m[2] === undefined ? 0 : Number(m[2]);
  const half = m[3]?.[0];
  if (minute > 59) return null;
  if (half) {
    if (hour < 1 || hour > 12) return null;
    hour = (hour % 12) + (half === "p" ? 12 : 0);
  }
  return hour > 23 ? null : { hour, minute };
}

/** parseMinute reads a minute past the hour: "5", ":05", "45". Null when it is not one. */
export function parseMinute(text: string): number | null {
  const m = /^:?(\d{1,2})$/.exec(text.trim());
  if (!m) return null;
  const n = Number(m[1]);
  return n <= 59 ? n : null;
}
