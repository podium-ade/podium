import { describe, expect, it } from "vitest";
import { commonZones, formatOffset, zoneLabel } from "./timezones";

const january = new Date(Date.UTC(2026, 0, 15, 12));
const july = new Date(Date.UTC(2026, 6, 15, 12));

describe("commonZones", () => {
  it("is one row per timezone, sorted by its offset now", () => {
    const zones = commonZones([], january);
    const minutes = zones.map((z) => z.minutes);
    expect(minutes).toEqual([...minutes].sort((a, b) => a - b));
    expect(new Set(zones.map((z) => z.id)).size).toBe(zones.length);
    // UTC sits at zero, between the Americas and Europe.
    expect(zones.find((z) => z.id === "UTC")?.minutes).toBe(0);
  });

  it("lets the person's own zone stand in for the row whose clock it shares all year", () => {
    const zones = commonZones(["America/Toronto"], january);
    expect(zones.some((z) => z.id === "America/New_York")).toBe(false);
    const eastern = zones.find((z) => z.id === "America/Toronto");
    expect(eastern).toMatchObject({ name: "Eastern Time", places: "Toronto", minutes: -300 });
    expect(zoneLabel(eastern, "America/Toronto")).toBe("Eastern Time · Toronto");
  });

  it("keeps a zone without daylight saving apart from one that has it", () => {
    // Mexico City (and Regina with it) is UTC-6 all year; Chicago is UTC-6 only in winter.
    const zones = commonZones(["America/Regina"], january);
    const regina = zones.find((z) => z.id === "America/Regina");
    const chicago = zones.find((z) => z.id === "America/Chicago");
    expect(regina).toMatchObject({ name: "Central, no DST", minutes: -360 });
    expect(zones.some((z) => z.id === "America/Mexico_City")).toBe(false);
    expect(chicago?.minutes).toBe(-360);
    expect(commonZones([], july).find((z) => z.id === "America/Chicago")?.minutes).toBe(-300);
  });

  it("gives a zone no common place shares a row of its own", () => {
    const zones = commonZones(["Asia/Chatham_Islands_Nowhere", "Pacific/Chatham"], january);
    expect(zones.find((z) => z.id === "Pacific/Chatham")).toMatchObject({ name: "Chatham" });
  });
});

describe("formatOffset", () => {
  it("says an offset in UTC terms", () => {
    expect(formatOffset(0)).toBe("UTC");
    expect(formatOffset(-300)).toBe("UTC-5");
    expect(formatOffset(330)).toBe("UTC+5:30");
    expect(formatOffset(-210)).toBe("UTC-3:30");
  });
});
