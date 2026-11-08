import { describe as group, expect, it } from "vitest";
import { checkCron, describe, fromCron, parseMinute, parseTime, switchKind, toCron, type Repeat } from "./schedule";

group("toCron and fromCron", () => {
  const cases: [Repeat, string][] = [
    [{ kind: "minutes", every: 15, days: [0, 1, 2, 3, 4, 5, 6] }, "*/15 * * * *"],
    [{ kind: "minutes", every: 30, days: [1, 2, 3, 4, 5] }, "*/30 * * * 1-5"],
    [{ kind: "hourly", every: 1, minute: 0, days: [0, 1, 2, 3, 4, 5, 6] }, "0 * * * *"],
    [{ kind: "hourly", every: 2, minute: 30, days: [0, 6] }, "30 */2 * * 0,6"],
    [{ kind: "daily", hour: 9, minute: 0, days: [1, 2, 3, 4, 5] }, "0 9 * * 1-5"],
    [{ kind: "daily", hour: 17, minute: 45, days: [1, 3, 5] }, "45 17 * * 1,3,5"],
    [{ kind: "monthly", day: 1, hour: 8, minute: 0 }, "0 8 1 * *"],
  ];
  it.each(cases)("writes %o as %s and reads it back", (r, cron) => {
    expect(toCron(r)).toBe(cron);
    expect(fromCron(cron)).toEqual(r);
  });

  it("reads Sunday written as 7, and ranges and lists together", () => {
    expect(fromCron("0 9 * * 7")).toEqual({ kind: "daily", hour: 9, minute: 0, days: [0] });
    expect(fromCron("0 9 * * 1-3,5")).toEqual({ kind: "daily", hour: 9, minute: 0, days: [1, 2, 3, 5] });
  });

  it("leaves to cron what the builder cannot say", () => {
    for (const expr of ["0 9,17 * * *", "*/7 * * * *", "0 9 * 1 *", "0 9 1 * 1", "0 */5 * * *", "nonsense", "0 9 * *"]) {
      expect(fromCron(expr), expr).toBeNull();
    }
  });
});

group("describe", () => {
  it("says a schedule in a sentence", () => {
    expect(describe({ kind: "daily", hour: 9, minute: 0, days: [1, 2, 3, 4, 5] })).toBe("Weekdays at 09:00");
    expect(describe({ kind: "daily", hour: 9, minute: 0, days: [0, 1, 2, 3, 4, 5, 6] })).toBe("Every day at 09:00");
    expect(describe({ kind: "daily", hour: 18, minute: 30, days: [1, 3, 5] })).toBe("Mon, Wed and Fri at 18:30");
    expect(describe({ kind: "hourly", every: 2, minute: 15, days: [0, 6] })).toBe("Every 2 hours at :15 on weekends");
    expect(describe({ kind: "minutes", every: 30, days: [0, 1, 2, 3, 4, 5, 6] })).toBe("Every 30 minutes");
    expect(describe({ kind: "monthly", day: 22, hour: 9, minute: 0 })).toBe("Monthly on the 22nd at 09:00");
  });
});

group("switchKind", () => {
  it("keeps the time and the days across a change of frequency", () => {
    const daily: Repeat = { kind: "daily", hour: 7, minute: 30, days: [1, 2] };
    expect(switchKind(daily, "hourly")).toEqual({ kind: "hourly", every: 1, minute: 30, days: [1, 2] });
    expect(switchKind(daily, "monthly")).toEqual({ kind: "monthly", day: 1, hour: 7, minute: 30 });
  });
});

group("checkCron", () => {
  it("names the obvious mistakes", () => {
    expect(checkCron("0 9 * * 1-5")).toBe("");
    expect(checkCron("0 9 * *")).toContain("5 fields");
    expect(checkCron("")).toContain("Write a cron expression");
  });
});

group("parseTime and parseMinute", () => {
  it("reads a time the way people type one", () => {
    expect(parseTime("9")).toEqual({ hour: 9, minute: 0 });
    expect(parseTime("9:07")).toEqual({ hour: 9, minute: 7 });
    expect(parseTime("905")).toEqual({ hour: 9, minute: 5 });
    expect(parseTime("1730")).toEqual({ hour: 17, minute: 30 });
    expect(parseTime("17.45")).toEqual({ hour: 17, minute: 45 });
    expect(parseTime("5pm")).toEqual({ hour: 17, minute: 0 });
    expect(parseTime("5:30 PM")).toEqual({ hour: 17, minute: 30 });
    expect(parseTime("12am")).toEqual({ hour: 0, minute: 0 });
    expect(parseTime("12pm")).toEqual({ hour: 12, minute: 0 });
    for (const bad of ["25", "9:75", "13pm", "xyz", ""]) expect(parseTime(bad), bad).toBeNull();
  });

  it("reads a minute past the hour", () => {
    expect(parseMinute("7")).toBe(7);
    expect(parseMinute(":45")).toBe(45);
    expect(parseMinute("60")).toBeNull();
  });
});

group("a system routine's schedule", () => {
  it("reads every minute, and every few minutes, back into Repeat", () => {
    expect(fromCron("*/1 * * * *")).toEqual({ kind: "minutes", every: 1, days: [0, 1, 2, 3, 4, 5, 6] });
    expect(describe(fromCron("*/1 * * * *")!)).toBe("Every minute");
    expect(describe(fromCron("*/5 * * * 1-5")!)).toBe("Every 5 minutes on weekdays");
  });
});
