import { create } from "@bufbuild/protobuf";
import { timestampFromMs } from "@bufbuild/protobuf/wkt";
import { describe, expect, it } from "vitest";
import { SnapshotSchema } from "../gen/podium/v1/snapshot_pb";
import { DAY_SECONDS, sweepPlan } from "./snapshots";

const now = Date.UTC(2026, 9, 28, 12);

function snap(id: string, daysOld: number, size: number, inUse = false) {
  return create(SnapshotSchema, {
    sessionId: id,
    sizeBytes: BigInt(size),
    updatedAt: timestampFromMs(now - daysOld * DAY_SECONDS * 1000),
    inUse,
  });
}

const rows = [snap("c", 2, 10), snap("a", 0, 10), snap("d", 3, 10), snap("b", 1, 10)];

describe("sweepPlan", () => {
  it("deletes nothing with both rules off", () => {
    expect(sweepPlan(rows, { maxAgeSeconds: 0, maxTotalBytes: 0 }, now).size).toBe(0);
  });

  it("deletes what is older than the age", () => {
    expect([...sweepPlan(rows, { maxAgeSeconds: 1.5 * DAY_SECONDS, maxTotalBytes: 0 }, now)].sort()).toEqual([
      "c",
      "d",
    ]);
  });

  it("keeps the newest under the total and deletes everything older", () => {
    expect([...sweepPlan(rows, { maxAgeSeconds: 0, maxTotalBytes: 25 }, now)].sort()).toEqual(["c", "d"]);
  });

  it("never deletes a snapshot in use", () => {
    const plan = sweepPlan([snap("a", 0, 10), snap("old", 9, 10, true)], { maxAgeSeconds: DAY_SECONDS, maxTotalBytes: 5 }, now);
    expect([...plan]).toEqual(["a"]);
  });
});
