import { timestampMs, type Timestamp } from "@bufbuild/protobuf/wkt";
import { toJson } from "@bufbuild/protobuf";
import { TaskEventKind, TaskEventSchema, type Message, type TaskEvent } from "../gen/podium/v1/node_pb";
import { parseActivity, toolSummary, type Activity } from "./activity";
import { eventKindLabel } from "./format";
import { Accumulator, sidecarOf, type LogLine } from "./logs";

/**
 * Entries are the one model both task views read: every event a task's stream delivered,
 * normalised into rows that carry a time, a severity, a source and a one-line summary.
 *
 * A log chunk is bytes, not lines, so it becomes as many entries as the lines it completes;
 * a `message` whose type is `activity` is a tool call or a reasoning block, parsed. Every
 * other event is one entry, and keeps the event itself for the explorer's JSON view.
 */

export type Severity = "info" | "warn" | "error";

export const SEVERITIES: readonly Severity[] = ["info", "warn", "error"];

export function severityRank(s: Severity): number {
  return SEVERITIES.indexOf(s);
}

export interface Entry {
  /**
   * key names the entry in a link: the seq of the event it came from, and for a log line
   * its place among the lines that event completed. Replaying the stream from the start
   * produces the same keys, which is what makes a pasted link land on the same row.
   */
  key: string;
  /** index is the entry's place in the whole stream. */
  index: number;
  seq: bigint;
  ts?: Timestamp;
  /** at is ts in ms, carried forward from the entry before when an entry has none. */
  at: number;
  /** kind is `log`, `tool`, `reasoning`, `message`, or a lifecycle event's label. */
  kind: string;
  /** source is `node`, `stdout`, `stderr`, `sidecar:<name>`, `message`, `agent` or `tool:<name>`. */
  source: string;
  severity: Severity;
  summary: string;
  line?: LogLine;
  message?: Message;
  activity?: Activity;
  /** event is kept for every entry that is not a log line. */
  event?: TaskEvent;
}

/** How much of a message's first line a summary shows. */
const MAX_DETAIL = 200;

function firstLine(text: string): string {
  const line = text.split("\n", 1)[0];
  return line.length > MAX_DETAIL ? `${line.slice(0, MAX_DETAIL)}…` : line;
}

// Replayed events carry an unset payload for the kinds that have none, so every read below is
// guarded by the oneof case rather than by the kind.
export function detailOf(ev: TaskEvent): string {
  switch (ev.payload.case) {
    case "error":
      return ev.payload.value.retryable
        ? `${ev.payload.value.message} (retryable)`
        : ev.payload.value.message;
    case "exited":
      return ev.payload.value.oomKilled
        ? `exit ${ev.payload.value.exitCode}, OOM-killed`
        : `exit ${ev.payload.value.exitCode}`;
    case "finished": {
      const u = ev.payload.value.usage;
      const exit = `exit ${ev.payload.value.exitCode}`;
      return u ? `${exit}, ${u.cpuSeconds.toFixed(2)}s cpu, ${u.peakMemoryMb} MB peak` : exit;
    }
    case "step":
      return `${ev.payload.value.name}: ${ev.payload.value.status}`;
    case "message":
      return `${ev.payload.value.type}: ${firstLine(ev.payload.value.text)}`;
    case "preview":
      return `published over ${ev.payload.value.via} on ${ev.payload.value.address}`;
    case "sandbox":
      return `${ev.payload.value.sandboxId} on ${ev.payload.value.provider}`;
    case "artifact":
      return ev.payload.value.name;
    default:
      return "";
  }
}

/** Accounting is the part of an `accounting` message the page shows (agent/runtime/src/artifacts.ts). */
export interface Accounting {
  turns?: number;
  costUsd?: number;
}

export function accountingOf(text: string): Accounting | undefined {
  try {
    const v: unknown = JSON.parse(text);
    if (typeof v !== "object" || v === null) return undefined;
    const o = v as Record<string, unknown>;
    const num = (k: string) => (typeof o[k] === "number" ? (o[k] as number) : undefined);
    return { turns: num("num_turns"), costUsd: num("total_cost_usd") };
  } catch {
    return undefined;
  }
}

function accountingSummary(text: string): string {
  const a = accountingOf(text);
  if (!a) return "accounting";
  const parts: string[] = [];
  if (a.turns !== undefined) parts.push(`${a.turns} ${a.turns === 1 ? "turn" : "turns"}`);
  if (a.costUsd !== undefined) parts.push(`$${a.costUsd.toFixed(4)}`);
  return parts.length > 0 ? `accounting: ${parts.join(", ")}` : "accounting";
}

/** A non-zero exit, an OOM kill and a non-retryable error are the moments a task went wrong. */
function lifecycleSeverity(ev: TaskEvent): Severity {
  switch (ev.payload.case) {
    case "error":
      return ev.payload.value.retryable ? "warn" : "error";
    case "exited":
      return ev.payload.value.exitCode !== 0 || ev.payload.value.oomKilled ? "error" : "info";
    case "finished":
      return ev.payload.value.exitCode !== 0 ? "error" : "info";
    default:
      return ev.kind === TaskEventKind.ERROR ? "error" : "info";
  }
}

function lineSource(l: LogLine): string {
  return l.stream === "sidecar" ? `sidecar:${sidecarOf(l)}` : l.stream;
}

/**
 * EntryStream turns StreamTaskEvents into entries, in order. Feed it every event and flush
 * it once at the end of the stream for the unterminated tails of the logs.
 */
export class EntryStream {
  private readonly acc = new Accumulator();
  private index = 0;
  private lastAt = 0;

  append(ev: TaskEvent): Entry[] {
    if (ev.kind === TaskEventKind.LOG) {
      return this.acc.append(ev).map((l, i) => this.lineEntry(l, `${ev.seq}.${i}`));
    }
    return [this.eventEntry(ev)];
  }

  flush(): Entry[] {
    return this.acc.flush().map((l, i) => this.lineEntry(l, `end.${i}`));
  }

  private at(ts?: Timestamp): number {
    if (ts) this.lastAt = timestampMs(ts);
    return this.lastAt;
  }

  private lineEntry(line: LogLine, key: string): Entry {
    return {
      key,
      index: this.index++,
      seq: line.seq,
      ts: line.ts,
      at: this.at(line.ts),
      kind: "log",
      source: lineSource(line),
      severity: line.stream === "stderr" ? "warn" : "info",
      summary: line.stream === "sidecar" && line.source !== "" ? `[${line.source}] ${line.text}` : line.text,
      line,
    };
  }

  private eventEntry(ev: TaskEvent): Entry {
    const base = {
      key: String(ev.seq),
      index: this.index++,
      seq: ev.seq,
      ts: ev.ts,
      at: this.at(ev.ts),
      event: ev,
    };
    if (ev.payload.case === "message") {
      const message = ev.payload.value;
      if (message.type === "activity") {
        const activity = parseActivity(message.text);
        if (activity?.kind === "tool") {
          return {
            ...base,
            kind: "tool",
            source: `tool:${activity.tool}`,
            severity: activity.status === "error" ? "error" : "info",
            summary: toolSummary(activity),
            message,
            activity,
          };
        }
        if (activity?.kind === "reasoning") {
          return { ...base, kind: "reasoning", source: "agent", severity: "info", summary: firstLine(activity.text), message, activity };
        }
      }
      return {
        ...base,
        kind: "message",
        source: "message",
        severity: "info",
        summary: message.type === "accounting" ? accountingSummary(message.text) : detailOf(ev),
        message,
      };
    }
    const label = eventKindLabel(ev.kind);
    const detail = detailOf(ev);
    return {
      ...base,
      kind: label,
      source: "node",
      severity: lifecycleSeverity(ev),
      summary: detail ? `${label} · ${detail}` : label,
    };
  }
}

/** haystack is everything a search over an entry looks at, not just its one line. */
export function haystack(e: Entry): string {
  const parts = [e.summary];
  if (e.activity?.kind === "tool") {
    for (const s of [e.activity.title, e.activity.input, e.activity.output, e.activity.error]) {
      if (s) parts.push(s);
    }
  } else if (e.activity?.kind === "reasoning") {
    parts.push(e.activity.text);
  } else if (e.message) {
    parts.push(e.message.text);
  }
  return parts.join("\n");
}

/** isTranscript says whether an entry is part of the story: what the task said and did. */
export function isTranscript(e: Entry): boolean {
  if (e.kind === "tool" || e.kind === "reasoning") return true;
  if (e.kind === "message") return e.message?.type !== "accounting";
  return e.kind === eventKindLabel(TaskEventKind.ERROR);
}

/** hasStory says whether the stream has a story to tell: a task that spoke or used tools. */
export function hasStory(entries: readonly Entry[]): boolean {
  return entries.some((e) => e.kind === "tool" || e.kind === "reasoning" || e.kind === "message");
}

/** firstProblem is the first error, or failing that the first warning. */
export function firstProblem(entries: readonly Entry[]): Entry | undefined {
  return entries.find((e) => e.severity === "error") ?? entries.find((e) => e.severity === "warn");
}

/** lines is the log lines among the entries, in order: what the raw download is made of. */
export function linesOf(entries: readonly Entry[]): LogLine[] {
  const out: LogLine[] = [];
  for (const e of entries) if (e.line) out.push(e.line);
  return out;
}

/** entryJson is what the explorer shows and copies for one entry: the record, not the bytes. */
export function entryJson(e: Entry): Record<string, unknown> {
  const out: Record<string, unknown> = {
    key: e.key,
    seq: String(e.seq),
    ts: e.at ? new Date(e.at).toISOString() : undefined,
    kind: e.kind,
    source: e.source,
    severity: e.severity,
  };
  if (e.line) out.text = e.line.text;
  else if (e.activity) out.activity = e.activity;
  else if (e.event) out.event = toJson(TaskEventSchema, e.event);
  return out;
}

/** linkTo is the address of one entry in the explorer, under the query it was found with. */
export function linkTo(key: string, query: string): string {
  const url = new URL(window.location.href);
  url.searchParams.set("view", "logs");
  url.searchParams.set("entry", key);
  if (query) url.searchParams.set("q", query);
  else url.searchParams.delete("q");
  return url.toString();
}

export interface Field {
  path: string;
  value: string;
  /** term is the query that keeps entries with this value; absent when none would be exact. */
  term?: string;
}

const FIELD_TERM: Record<string, string> = { source: "source", kind: "kind", severity: "severity" };
/** Text the body already shows in full is not repeated as a field. */
const BODY_FIELDS = new Set(["text", "activity.input", "activity.output", "activity.error", "activity.text", "event.message.text"]);

/** fieldsOf flattens an entry's record into the rows the explorer's field table shows. */
export function fieldsOf(e: Entry): Field[] {
  const out: Field[] = [];
  const walk = (v: unknown, path: string) => {
    if (v === undefined || v === null || BODY_FIELDS.has(path)) return;
    if (typeof v === "object" && !Array.isArray(v)) {
      for (const [k, child] of Object.entries(v)) walk(child, path ? `${path}.${k}` : k);
      return;
    }
    const value = Array.isArray(v) ? v.join(", ") : String(v);
    const field = FIELD_TERM[path];
    const term = field
      ? `${field}:${value}`
      : value.length > 0 && value.length <= 80 && !value.includes('"') && !/\s{2}/.test(value)
        ? `"${value}"`
        : undefined;
    out.push({ path, value, term });
  };
  walk(entryJson(e), "");
  return out;
}
