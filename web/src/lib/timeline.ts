import { isTranscript, type Entry } from "./entries";

/**
 * The run timeline is a task's entries on a time axis, one lane per kind of actor: what
 * the node did, what the agent said, the tools it called, and what the container printed.
 * It is drawn from these pure pieces so the geometry can be tested without a browser.
 */

export interface TimeRange {
  from: number;
  to: number;
}

export type Lane = "node" | "agent" | "tools" | "output";

export const LANES: readonly Lane[] = ["node", "agent", "tools", "output"];

export const LANE_LABEL: Record<Lane, string> = {
  node: "node",
  agent: "agent",
  tools: "tools",
  output: "output",
};

export function laneOf(e: Entry): Lane {
  if (e.line) return "output";
  if (e.kind === "tool") return "tools";
  if (e.kind === "reasoning" || e.kind === "message") return "agent";
  return "node";
}

export type MarkTone = "info" | "warn" | "error" | "answer";

/** Mark is one entry on its lane: a point, or for a tool call the stretch it covers. */
export interface Mark {
  entry: Entry;
  lane: Lane;
  from: number;
  to: number;
  tone: MarkTone;
}

/**
 * marksOf places every entry that is not a log line. A tool call is reported when it
 * finishes, so its bar runs from the transcript step before it — when the agent could
 * have started it at the earliest — to its own time: an upper bound on how long it took,
 * which is what the eye wants from a waterfall.
 */
export function marksOf(entries: readonly Entry[]): Mark[] {
  const out: Mark[] = [];
  let lastStep = 0;
  for (const e of entries) {
    if (e.line || e.at === 0) continue;
    const lane = laneOf(e);
    let from = e.at;
    if (lane === "tools" && lastStep > 0 && lastStep <= e.at) from = lastStep;
    const tone: MarkTone =
      e.severity === "error"
        ? "error"
        : e.message?.type === "final"
          ? "answer"
          : e.severity === "warn" || e.message?.type === "question"
            ? "warn"
            : "info";
    out.push({ entry: e, lane, from, to: e.at, tone });
    if (isTranscript(e)) lastStep = e.at;
  }
  return out;
}

/** Phase is one stage of the run, as the node's lifecycle events bound it. */
export interface Phase {
  name: string;
  from: number;
  to: number;
}

/** phasesOf reads the starting and running stages off the node's events. */
export function phasesOf(entries: readonly Entry[], end: number): Phase[] {
  const first = (kind: string) => entries.find((e) => e.kind === kind && e.at > 0)?.at;
  const provisioning = first("provisioning");
  const pulling = first("pulling");
  const started = first("started");
  const stopped = first("exited") ?? first("finished") ?? end;
  const out: Phase[] = [];
  if (provisioning !== undefined) out.push({ name: "provisioning", from: provisioning, to: pulling ?? started ?? stopped });
  if (pulling !== undefined) out.push({ name: "pulling", from: pulling, to: started ?? stopped });
  if (started !== undefined) out.push({ name: "running", from: started, to: stopped });
  return out.filter((p) => p.to >= p.from);
}

/** Bin is one slice of the output lane: how many lines fell in it, and how many were stderr. */
export interface Bin {
  from: number;
  to: number;
  count: number;
  warn: number;
}

/** density spreads the log lines over n equal slices of the span. */
export function density(entries: readonly Entry[], span: TimeRange, n: number): Bin[] {
  const width = Math.max(1, (span.to - span.from) / n);
  const bins: Bin[] = Array.from({ length: n }, (_, i) => ({
    from: span.from + i * width,
    to: span.from + (i + 1) * width,
    count: 0,
    warn: 0,
  }));
  for (const e of entries) {
    if (!e.line || e.at === 0) continue;
    const i = Math.min(n - 1, Math.max(0, Math.floor((e.at - span.from) / width)));
    bins[i].count++;
    if (e.severity !== "info") bins[i].warn++;
  }
  return bins;
}

const STEPS_MS = [
  1_000, 2_000, 5_000, 10_000, 15_000, 30_000, 60_000, 2 * 60_000, 5 * 60_000, 10 * 60_000, 15 * 60_000,
  30 * 60_000, 3_600_000, 2 * 3_600_000, 6 * 3_600_000,
];

/** Tick is one label of the axis, as time since the run began. */
export interface Tick {
  at: number;
  label: string;
}

/** ticks picks a round step that gives about `target` labels across the span. */
export function ticks(span: TimeRange, target = 6): Tick[] {
  const width = span.to - span.from;
  if (width <= 0) return [];
  const step = STEPS_MS.find((s) => width / s <= target) ?? STEPS_MS[STEPS_MS.length - 1];
  const out: Tick[] = [];
  for (let t = 0; t <= width; t += step) out.push({ at: span.from + t, label: tickLabel(t) });
  return out;
}

/** tickLabel is an offset on a round step: 30s, 2m, 1h 30m. */
function tickLabel(ms: number): string {
  if (ms < 60_000) return `${ms / 1000}s`;
  if (ms < 3_600_000) return `${ms / 60_000}m`;
  const h = Math.floor(ms / 3_600_000);
  const m = Math.round((ms % 3_600_000) / 60_000);
  return m ? `${h}h ${m}m` : `${h}h`;
}
