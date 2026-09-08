import { describe, expect, it } from "vitest";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import type { TaskCost, UsageDay } from "../gen/podium/agent/v1/agent_pb";
import {
  breakdown,
  byTask,
  customRange,
  dayKey,
  eachDay,
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

describe("byTask", () => {
  it("indexes by task id", () => {
    const map = byTask([cost({ taskId: "tsk_a" }), cost({ taskId: "tsk_b" })]);
    expect(map.get("tsk_a")?.taskId).toBe("tsk_a");
    expect(map.size).toBe(2);
  });

  it("drops a turn whose task was never created", () => {
    // It spent money and still counts towards a total, but there is no row to join it to.
    expect(byTask([cost({ taskId: "" })]).size).toBe(0);
  });
});

describe("breakdown", () => {
  const costs = [
    cost({ playbook: "triage", costUsd: 3 }),
    cost({ playbook: "code", costUsd: 5 }),
    cost({ playbook: "triage", costUsd: 1 }),
  ];

  it("groups and orders by spend, largest first", () => {
    expect(breakdown(costs, (c) => c.playbook)).toEqual([
      { key: "code", cost: 5, turns: 1 },
      { key: "triage", cost: 4, turns: 2 },
    ]);
  });

  it("counts a turn that reported no cost without pretending it was free", () => {
    const [only] = breakdown([cost({ playbook: "triage", costUsd: undefined })], (c) => c.playbook);
    expect(only).toEqual({ key: "triage", cost: 0, turns: 1 });
  });

  it("labels an empty group rather than keying on the empty string", () => {
    expect(breakdown([cost({ playbook: "" })], (c) => c.playbook)[0].key).toBe("—");
  });
});
