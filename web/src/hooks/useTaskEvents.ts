import { useEffect, useRef, useState } from "react";
import type { Timestamp } from "@bufbuild/protobuf/wkt";
import { TaskEventKind, type TaskEvent } from "../gen/podium/v1/node_pb";
import { errorMessage, tasks } from "../lib/client";
import { Accumulator, type LogLine } from "../lib/logs";

export interface TimelineEntry {
  seq: bigint;
  kind: TaskEventKind;
  ts?: Timestamp;
  detail: string;
}

export type StreamPhase = "connecting" | "streaming" | "finished" | "error";

const FLUSH_MS = 60;
const MAX_BACKOFF_MS = 10_000;

// Replayed events carry an unset payload for the kinds that have none, so every read below is
// guarded by the oneof case rather than by the kind.
function detailOf(ev: TaskEvent): string {
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
    default:
      return "";
  }
}

function sleep(ms: number, signal: AbortSignal): Promise<void> {
  return new Promise((resolve) => {
    const t = setTimeout(resolve, ms);
    signal.addEventListener(
      "abort",
      () => {
        clearTimeout(t);
        resolve();
      },
      { once: true },
    );
  });
}

/**
 * useTaskEvents follows StreamTaskEvents for one task.
 *
 * from_seq is exclusive, so reconnecting with the highest seq already rendered is exactly-once:
 * no gap and no repeat. A clean end of stream means the task is terminal and everything has been
 * delivered — that is when the task is re-read for its exit code. An error is not terminal: it
 * reconnects with backoff.
 *
 * onStatusEvent fires for every non-log event and once more at end of stream. Those are exactly
 * the events the server turns into status transitions, so re-reading the task on them keeps the
 * badge honest instead of up to one poll interval behind the logs.
 */
export function useTaskEvents(taskId: string, onStatusEvent: () => void) {
  const [lines, setLines] = useState<LogLine[]>([]);
  const [timeline, setTimeline] = useState<TimelineEntry[]>([]);
  const [phase, setPhase] = useState<StreamPhase>("connecting");
  const [error, setError] = useState<string>();
  const notify = useRef(onStatusEvent);
  useEffect(() => {
    notify.current = onStatusEvent;
  }, [onStatusEvent]);

  useEffect(() => {
    const ctrl = new AbortController();
    const acc = new Accumulator();
    let lastSeq = 0n;
    let pendingLines: LogLine[] = [];
    let pendingEvents: TimelineEntry[] = [];
    let timer: ReturnType<typeof setTimeout> | undefined;

    const flush = () => {
      timer = undefined;
      if (pendingLines.length > 0) {
        const batch = pendingLines;
        pendingLines = [];
        setLines((prev) => prev.concat(batch));
      }
      if (pendingEvents.length > 0) {
        const batch = pendingEvents;
        pendingEvents = [];
        setTimeline((prev) => prev.concat(batch));
      }
    };
    const schedule = () => {
      if (timer === undefined) timer = setTimeout(flush, FLUSH_MS);
    };

    void (async () => {
      let backoff = 500;
      while (!ctrl.signal.aborted) {
        try {
          for await (const ev of tasks.streamTaskEvents(
            { taskId, fromSeq: lastSeq },
            { signal: ctrl.signal },
          )) {
            if (ev.seq > lastSeq) lastSeq = ev.seq;
            backoff = 500;
            setPhase("streaming");
            if (ev.kind === TaskEventKind.LOG) {
              pendingLines = pendingLines.concat(acc.append(ev));
            } else {
              pendingEvents.push({ seq: ev.seq, kind: ev.kind, ts: ev.ts, detail: detailOf(ev) });
              notify.current();
            }
            schedule();
          }
          pendingLines = pendingLines.concat(acc.flush());
          flush();
          if (!ctrl.signal.aborted) {
            setPhase("finished");
            notify.current();
          }
          return;
        } catch (err) {
          if (ctrl.signal.aborted) return;
          setError(errorMessage(err));
          setPhase("error");
          await sleep(backoff, ctrl.signal);
          backoff = Math.min(backoff * 2, MAX_BACKOFF_MS);
        }
      }
    })();

    return () => {
      ctrl.abort();
      if (timer !== undefined) clearTimeout(timer);
    };
  }, [taskId]);

  return { lines, timeline, phase, error };
}
