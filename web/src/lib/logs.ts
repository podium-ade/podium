import { LogChunk_Stream, TaskEventKind, type TaskEvent } from "../gen/podium/v1/node_pb";

export type StreamName = "stdout" | "stderr" | "sidecar";

export interface LogLine {
  id: number;
  seq: bigint;
  stream: StreamName;
  /** Set only for sidecar output. */
  source: string;
  text: string;
}

function streamName(s: LogChunk_Stream): StreamName {
  switch (s) {
    case LogChunk_Stream.STDERR:
      return "stderr";
    case LogChunk_Stream.SIDECAR:
      return "sidecar";
    default:
      return "stdout";
  }
}

/**
 * Accumulator turns the byte chunks of StreamTaskEvents into whole lines.
 *
 * Log bytes are raw, not lines: one chunk may split a line in the middle and the next chunk of
 * that same stream finishes it. Chunks of different streams interleave, so each stream keeps its
 * own partial line and its own incremental UTF-8 decoder. Events arrive in strictly ascending
 * seq order, which is the order lines come out in.
 */
export class Accumulator {
  private readonly decoders = new Map<string, TextDecoder>();
  private readonly partial = new Map<string, string>();
  private nextId = 0;

  /** append returns the lines completed by this event; a non-log event returns none. */
  append(ev: TaskEvent): LogLine[] {
    if (ev.kind !== TaskEventKind.LOG || ev.payload.case !== "log") return [];
    const chunk = ev.payload.value;
    const stream = streamName(chunk.stream);
    const key = stream === "sidecar" ? `sidecar:${chunk.sidecarName}` : stream;

    let decoder = this.decoders.get(key);
    if (!decoder) {
      decoder = new TextDecoder("utf-8");
      this.decoders.set(key, decoder);
    }
    const text = (this.partial.get(key) ?? "") + decoder.decode(chunk.bytes, { stream: true });

    const parts = text.split("\n");
    this.partial.set(key, parts.pop() ?? "");
    return parts.map((raw) => this.line(ev.seq, stream, chunk.sidecarName, raw));
  }

  /** flush emits the unterminated tail of every stream. Call it when the task is finished. */
  flush(): LogLine[] {
    const out: LogLine[] = [];
    for (const [key, text] of this.partial) {
      if (text === "") continue;
      const stream: StreamName = key.startsWith("sidecar:") ? "sidecar" : (key as StreamName);
      out.push(this.line(0n, stream, key.startsWith("sidecar:") ? key.slice(8) : "", text));
    }
    this.partial.clear();
    return out;
  }

  private line(seq: bigint, stream: StreamName, source: string, raw: string): LogLine {
    return {
      id: this.nextId++,
      seq,
      stream,
      source,
      text: raw.endsWith("\r") ? raw.slice(0, -1) : raw,
    };
  }
}

export interface LogFilter {
  stdout: boolean;
  stderr: boolean;
  search: string;
}

export function filterLines(lines: readonly LogLine[], f: LogFilter): LogLine[] {
  const needle = f.search.toLowerCase();
  return lines.filter((l) => {
    if (l.stream === "stdout" && !f.stdout) return false;
    if (l.stream === "stderr" && !f.stderr) return false;
    if (needle !== "" && !l.text.toLowerCase().includes(needle)) return false;
    return true;
  });
}

export function toRawText(lines: readonly LogLine[]): string {
  return lines.map((l) => l.text).join("\n") + (lines.length > 0 ? "\n" : "");
}
