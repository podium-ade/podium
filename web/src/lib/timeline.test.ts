import { describe, expect, it } from "vitest";
import { TaskEventKind, type TaskEvent } from "../gen/podium/v1/node_pb";
import { EntryStream, type Entry } from "./entries";
import { density, laneOf, marksOf, phasesOf, ticks } from "./timeline";
import { activityEvent, at, errorEvent, exitedEvent, lifecycleEvent, logEvent, messageEvent, stderr, stdout } from "../test/events";

function build(...events: TaskEvent[]): Entry[] {
  const s = new EntryStream();
  return events.flatMap((ev) => s.append(ev));
}

const run = build(
  at(lifecycleEvent(1, TaskEventKind.PROVISIONING), 1_000),
  at(lifecycleEvent(2, TaskEventKind.STARTED), 3_000),
  at(logEvent(3, stdout, "hello\n"), 4_000),
  at(messageEvent(4, "progress", "looking"), 5_000),
  at(activityEvent(5, { kind: "tool", tool: "bash", status: "completed" }), 9_000),
  at(logEvent(6, stderr, "warn: slow\n"), 9_500),
  at(activityEvent(7, { kind: "tool", tool: "read", status: "error" }), 10_000),
  at(messageEvent(8, "final", "done"), 12_000),
  at(errorEvent(9, "lease lost"), 12_500),
  at(exitedEvent(10, 0), 13_000),
);

describe("laneOf", () => {
  it("puts every entry with the actor that produced it", () => {
    expect(run.map(laneOf)).toEqual(["node", "node", "output", "agent", "tools", "output", "tools", "agent", "node", "node"]);
  });
});

describe("marksOf", () => {
  const marks = marksOf(run);

  it("draws every entry but the log lines", () => {
    expect(marks.map((m) => m.entry.key)).toEqual(["1", "2", "4", "5", "7", "8", "9", "10"]);
  });

  it("runs a tool call's bar from the step before it to when it finished", () => {
    const bash = marks.find((m) => m.entry.key === "5")!;
    expect([bash.from, bash.to]).toEqual([5_000, 9_000]);
    const read = marks.find((m) => m.entry.key === "7")!;
    expect([read.from, read.to]).toEqual([9_000, 10_000]);
  });

  it("colours by what happened, not by who", () => {
    const tone = (key: string) => marks.find((m) => m.entry.key === key)!.tone;
    expect(tone("7")).toBe("error");
    expect(tone("8")).toBe("answer");
    expect(tone("9")).toBe("error");
    expect(tone("4")).toBe("info");
  });
});

describe("phasesOf", () => {
  it("bounds each stage by the node's events", () => {
    expect(phasesOf(run, 13_000)).toEqual([
      { name: "provisioning", from: 1_000, to: 3_000 },
      { name: "running", from: 3_000, to: 13_000 },
    ]);
  });

  it("leaves a run that has not stopped running to the end of what is known", () => {
    expect(phasesOf(run.slice(0, 4), 6_000).at(-1)).toEqual({ name: "running", from: 3_000, to: 6_000 });
  });
});

describe("density", () => {
  it("counts lines per slice, and which of them were stderr", () => {
    const bins = density(run, { from: 0, to: 10_000 }, 10);
    expect(bins[4]).toMatchObject({ count: 1, warn: 0 });
    expect(bins[9]).toMatchObject({ count: 1, warn: 1 });
    expect(bins.reduce((n, b) => n + b.count, 0)).toBe(2);
  });
});

describe("ticks", () => {
  it("picks a round step for the span", () => {
    expect(ticks({ from: 0, to: 19 * 60_000 }).map((t) => t.label)).toEqual(["0s", "5m", "10m", "15m"]);
    expect(ticks({ from: 0, to: 25_000 }).map((t) => t.label)).toEqual(["0s", "5s", "10s", "15s", "20s", "25s"]);
    expect(ticks({ from: 0, to: 0 })).toEqual([]);
  });
});
