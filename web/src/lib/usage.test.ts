import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { create } from "@bufbuild/protobuf";
import { UsageTotalsSchema, type TaskCost, type UsageBackend, type UsageDay } from "../gen/podium/agent/v1/agent_pb";
import {
  backendSummary,
  billed,
  credentialLabel,
  personLabel,
  readFilters,
  rowCost,
  tokens,
  totals,
  totalsIn,
  toUsageFilter,
  customRange,
  dayKey,
  lastDay,
  eachDay,
  isUnrecorded,
  previousRange,
  rangeFor,
  spanDays,
  sumIn,
  totalIn,
  tzOffsetMinutes,
  usd,
  usdShort,
} from "./usage";

const day = (over: Partial<UsageDay>): UsageDay =>
  ({ date: "2026-09-01", costUsd: 0, turns: 0, modelTurns: 0, unpriced: 0, ...over }) as UsageDay;

const cost = (over: Partial<TaskCost>): TaskCost =>
  ({
    taskId: "",
    turnId: "trn_1",
    sessionId: "ses_1",
    sourceKind: "slack",
    sourceKey: "slack:C1:1",
    playbook: "triage",
    profile: "default",
    status: "succeeded",
    startedAt: timestampFromDate(new Date("2026-09-03T10:00:00Z")),
    ...over,
  }) as TaskCost;

// A Thursday, mid-month, so month and trailing-day maths are both exercised away from edges.
const NOW = new Date(2026, 8, 17, 14, 30);

describe("dayKey", () => {
  it("keys a date in local time, not UTC", () => {
    expect(dayKey(new Date(2026, 8, 30, 23, 30))).toBe("2026-09-30");
  });

  it("pads single-digit months and days", () => {
    expect(dayKey(new Date(2026, 0, 5))).toBe("2026-01-05");
  });

  it("reports the offset east-positive, opposite to the browser's", () => {
    const d = new Date();
    expect(tzOffsetMinutes(d)).toBe(-d.getTimezoneOffset());
  });
});

describe("rangeFor", () => {
  it("makes 'today' a single whole day that includes right now", () => {
    const r = rangeFor("1d", NOW);
    expect(r.from).toEqual(new Date(2026, 8, 17));
    expect(r.to).toEqual(new Date(2026, 8, 18));
    expect(spanDays(r)).toBe(1);
    expect(r.from <= NOW && NOW < r.to).toBe(true);
  });

  it("counts trailing ranges inclusive of today", () => {
    // "Last 7 days" is today and the six before it, not today minus seven.
    for (const [id, days, first] of [
      ["2d", 2, 16],
      ["7d", 7, 11],
    ] as const) {
      const r = rangeFor(id, NOW);
      expect(spanDays(r)).toBe(days);
      expect(r.to).toEqual(new Date(2026, 8, 18));
      expect(r.from.getDate()).toBe(first);
    }
    expect(spanDays(rangeFor("30d", NOW))).toBe(30);
    expect(spanDays(rangeFor("90d", NOW))).toBe(90);
  });

  it("ends a trailing range after now, so today's spend is inside it", () => {
    expect(rangeFor("7d", NOW).to.getTime()).toBeGreaterThan(NOW.getTime());
  });

  it("starts a trailing range at midnight, never at the current time of day", () => {
    // A range beginning at 14:30 would leave a part-day at each end and make the per-day
    // figures disagree with the total above them.
    const r = rangeFor("7d", NOW);
    expect([r.from.getHours(), r.from.getMinutes(), r.from.getSeconds()]).toEqual([0, 0, 0]);
  });

  it("makes 'this month' the calendar month", () => {
    const r = rangeFor("month", NOW);
    expect(r.from).toEqual(new Date(2026, 8, 1));
    expect(r.to).toEqual(new Date(2026, 9, 1));
    expect(r.label).toBe("This month");
  });

  it("makes 'last month' the whole previous calendar month", () => {
    const r = rangeFor("last-month", NOW);
    expect(r.from).toEqual(new Date(2026, 7, 1));
    expect(r.to).toEqual(new Date(2026, 8, 1));
    expect(spanDays(r)).toBe(31); // August
  });

  it("handles last month across a year boundary", () => {
    const r = rangeFor("last-month", new Date(2026, 0, 9));
    expect(r.from).toEqual(new Date(2025, 11, 1));
    expect(r.to).toEqual(new Date(2026, 0, 1));
  });

  it("survives a trailing range crossing a month and a year boundary", () => {
    const r = rangeFor("7d", new Date(2026, 0, 2, 9, 0));
    expect(r.from).toEqual(new Date(2025, 11, 27));
    expect(r.to).toEqual(new Date(2026, 0, 3));
    expect(spanDays(r)).toBe(7);
  });
});

describe("customRange", () => {
  it("includes both days the operator typed", () => {
    const r = customRange(new Date(2026, 8, 3), new Date(2026, 8, 5));
    expect(r.from).toEqual(new Date(2026, 8, 3));
    expect(r.to).toEqual(new Date(2026, 8, 6));
    expect(spanDays(r)).toBe(3);
    expect(r.id).toBe("custom");
  });

  it("swaps dates entered backwards rather than refusing them", () => {
    const a = customRange(new Date(2026, 8, 5), new Date(2026, 8, 3));
    const b = customRange(new Date(2026, 8, 3), new Date(2026, 8, 5));
    expect(a.from).toEqual(b.from);
    expect(a.to).toEqual(b.to);
  });

  it("labels a one-day range as a day, not as a span", () => {
    const r = customRange(new Date(2026, 8, 3), new Date(2026, 8, 3));
    expect(spanDays(r)).toBe(1);
    expect(r.label).not.toContain("–");
  });

  it("ignores the time of day it is handed", () => {
    const r = customRange(new Date(2026, 8, 3, 23, 59), new Date(2026, 8, 4, 0, 1));
    expect(r.from).toEqual(new Date(2026, 8, 3));
    expect(r.to).toEqual(new Date(2026, 8, 5));
  });
});

describe("previousRange", () => {
  it("is the window of equal length immediately before a trailing range", () => {
    const r = rangeFor("7d", NOW);
    const p = previousRange(r);
    expect(p.to).toEqual(r.from);
    expect(spanDays(p)).toBe(spanDays(r));
  });

  it("compares this month against last month, not against an equal number of days", () => {
    // September has 30 days and August 31; an equal-length window would reach into July.
    const p = previousRange(rangeFor("month", NOW));
    expect(p.from).toEqual(new Date(2026, 7, 1));
    expect(p.to).toEqual(new Date(2026, 8, 1));
  });

  it("compares last month against the month before it", () => {
    const p = previousRange(rangeFor("last-month", NOW));
    expect(p.from).toEqual(new Date(2026, 6, 1));
    expect(p.to).toEqual(new Date(2026, 7, 1));
  });

  it("never overlaps the range it is compared with", () => {
    for (const id of ["1d", "2d", "7d", "30d", "90d", "month", "last-month"] as const) {
      const r = rangeFor(id, NOW);
      expect(previousRange(r).to.getTime()).toBeLessThanOrEqual(r.from.getTime());
    }
  });
});

describe("eachDay", () => {
  it("lists every day in the range, including ones nothing ran on", () => {
    const days = eachDay(rangeFor("7d", NOW));
    expect(days).toHaveLength(7);
    expect(dayKey(days[0])).toBe("2026-09-11");
    expect(dayKey(days[6])).toBe("2026-09-17");
  });

  it("returns one day for a one-day range", () => {
    expect(eachDay(rangeFor("1d", NOW))).toHaveLength(1);
  });
});

describe("sumIn and totalIn", () => {
  const days = [
    day({ date: "2026-09-10", costUsd: 5, turns: 2 }),
    day({ date: "2026-09-11", costUsd: 1, turns: 1 }),
    day({ date: "2026-09-17", costUsd: 2, turns: 3 }),
    day({ date: "2026-09-18", costUsd: 9, turns: 9 }),
  ];

  it("counts only the days inside the range", () => {
    // Last 7 days from the 17th is 11..17: the 10th falls before it and the 18th after.
    expect(totalIn(days, rangeFor("7d", NOW))).toBe(3);
    expect(sumIn(days, rangeFor("7d", NOW), (d) => d.turns)).toBe(4);
  });

  it("includes the first and the last day of the range", () => {
    expect(totalIn(days, customRange(new Date(2026, 8, 10), new Date(2026, 8, 17)))).toBe(8);
  });

  it("is zero for a range nothing falls in", () => {
    expect(totalIn(days, customRange(new Date(2026, 7, 1), new Date(2026, 7, 2)))).toBe(0);
  });
});

describe("money", () => {
  it("keeps four decimals, because a turn often costs less than a cent", () => {
    expect(usd(0.0123)).toBe("$0.0123");
    expect(usd(0)).toBe("$0.0000");
  });

  it("shortens a headline figure but not a tiny one", () => {
    expect(usdShort(0)).toBe("$0");
    expect(usdShort(0.0042)).toBe("$0.0042");
    expect(usdShort(12.4831)).toBe("$12.48");
    expect(usdShort(1999.6)).toBe("$2,000");
  });
});

describe("tokens", () => {
  it("abbreviates as the count grows", () => {
    expect(tokens(0)).toBe("0");
    expect(tokens(950)).toBe("950");
    expect(tokens(12_400)).toBe("12.4k");
    expect(tokens(12_000)).toBe("12k");
    expect(tokens(250_000)).toBe("250k");
    expect(tokens(3_140_000)).toBe("3.1M");
    expect(tokens(2_000_000_000)).toBe("2B");
  });
});

describe("totals and billed", () => {
  it("reads bigint tokens as numbers and an unset total as zero", () => {
    const t = totals(create(UsageTotalsSchema, { costUsd: 5, subscriptionCostUsd: 2, inputTokens: 1200n }));
    expect(t.input).toBe(1200);
    expect(billed(t)).toBe(3);
    expect(totals(undefined).cost).toBe(0);
  });

  it("falls back to a day's older fields when it has no totals", () => {
    const days = [day({ date: dayKey(NOW), costUsd: 4, turns: 2 })];
    const t = totalsIn(days, { id: "1d", from: new Date(2026, 8, 17), to: new Date(2026, 8, 18), label: "" });
    expect(t.cost).toBe(4);
    expect(t.subscription).toBe(0);
    expect(t.turns).toBe(2);
  });
});

describe("rowCost", () => {
  it("puts a subscription row's cost in covered, never billed", () => {
    expect(rowCost(cost({ costUsd: 1.5, authKind: "subscription" }))).toEqual({ covered: 1.5 });
    expect(rowCost(cost({ costUsd: 1.5, authKind: "api_key" }))).toEqual({ billed: 1.5 });
    // Unrecorded rows predate subscriptions being told apart, so they count as billed.
    expect(rowCost(cost({ costUsd: 1.5, authKind: "" }))).toEqual({ billed: 1.5 });
    expect(rowCost(cost({ costUsd: undefined }))).toEqual({});
  });
});

describe("personLabel", () => {
  it("names stand-ins and system routines", () => {
    expect(personLabel("")).toBe("Unattributed");
    expect(personLabel("github-app")).toBe("GitHub App");
    expect(personLabel("routine:r1", new Map([["r1", "Nightly triage"]]))).toBe("Nightly triage");
    expect(personLabel("routine:r2")).toBe("System routine");
    expect(personLabel("ana@acme.com")).toBe("ana@acme.com");
  });
});

describe("credentialLabel", () => {
  it("says what paid and whose", () => {
    expect(credentialLabel({ authKind: "subscription", provider: "anthropic", owner: "" })).toBe(
      "Anthropic subscription · Company",
    );
    expect(credentialLabel({ authKind: "api_key", provider: "openai", owner: "ana@acme.com" })).toBe(
      "OpenAI API key · ana@acme.com",
    );
    expect(credentialLabel({ authKind: "", provider: "", owner: "" })).toBe("Unrecorded");
  });
});

describe("filters", () => {
  it("round-trips through the URL and onto the request", () => {
    const f = readFilters(new URLSearchParams("source=slack&person=-&paid=subscription%7Canthropic%7C"));
    expect(f).toEqual({ origin: "slack", person: "-", model: "", credential: "subscription|anthropic|" });
    expect(toUsageFilter(f)).toEqual({
      origin: "slack",
      startedBy: "-",
      model: "",
      credential: "subscription|anthropic|",
    });
  });
});

const backend = (over: Partial<UsageBackend>): UsageBackend =>
  ({
    provider: "anthropic",
    agent: "claude",
    model: "claude-opus-5",
    effort: "",
    costUsd: 0,
    turns: 0,
    modelTurns: 0,
    unpriced: 0,
    ...over,
  }) as UsageBackend;

describe("isUnrecorded", () => {
  it("is true only when nothing at all was recorded", () => {
    expect(isUnrecorded(backend({ provider: "", agent: "", model: "" }))).toBe(true);
  });

  it("is false for a recorded turn whose effort is the model's own default", () => {
    // An empty effort is a real answer — "the model decides" — and must not be mistaken
    // for the bucket of turns that predate the columns.
    expect(isUnrecorded(backend({ effort: "" }))).toBe(false);
  });
});

describe("backendSummary", () => {
  it("counts distinct models and providers, and only what is attributed", () => {
    const s = backendSummary([
      backend({ model: "claude-opus-5", costUsd: 10 }),
      backend({ model: "claude-sonnet-5", costUsd: 5 }),
      backend({ provider: "xai", agent: "grok", model: "grok-4.6", costUsd: 1 }),
      // Unrecorded spend is real but unattributable, so it is left out of the sentence.
      backend({ provider: "", agent: "", model: "", costUsd: 99 }),
    ]);
    expect(s).toBe("3 models across 2 providers, $16.00 attributed.");
  });

  it("says so when nothing recorded what it ran on", () => {
    expect(backendSummary([backend({ provider: "", agent: "", model: "", costUsd: 9 })])).toBe(
      "Nothing in this range recorded what it ran on.",
    );
  });

  it("is singular for one model on one provider", () => {
    expect(backendSummary([backend({ costUsd: 2 })])).toBe("1 model across 1 provider, $2.00 attributed.");
  });
});

// A week across a clock change is 169 or 167 hours. Pinned to a zone that has one, so the
// test means the same wherever it runs.
describe("across a daylight saving change", () => {
  let tz: string | undefined;
  beforeAll(() => {
    tz = process.env.TZ;
    process.env.TZ = "America/Toronto";
  });
  afterAll(() => {
    if (tz === undefined) delete process.env.TZ;
    else process.env.TZ = tz;
  });

  it("starts the window before at midnight when the clocks went back in it", () => {
    // 2026-11-01 is 25 hours long in Toronto.
    const r = rangeFor("7d", new Date(2026, 10, 3, 9));
    const p = previousRange(r);
    expect(p.to).toEqual(r.from);
    expect(p.from).toEqual(new Date(2026, 9, 21));
    expect(p.from.getHours()).toBe(0);
  });

  it("starts the window before at midnight when the clocks went forward in it", () => {
    // 2026-03-08 is 23 hours long in Toronto.
    const r = rangeFor("7d", new Date(2026, 2, 10, 9));
    expect(previousRange(r).from).toEqual(new Date(2026, 1, 25));
  });

  it("names the last day of a range that ends after a 23-hour day", () => {
    // to is 2026-03-09 00:00; 24 hours before it is 2026-03-07 23:00, the wrong day.
    expect(dayKey(lastDay({ to: new Date(2026, 2, 9) }))).toBe("2026-03-08");
  });
});
