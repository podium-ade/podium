import { useEffect, useRef, useState } from "react";
import { TaskEventKind } from "../gen/podium/v1/node_pb";
import { errorMessage, tasks } from "../lib/client";
import { EntryStream, type Entry } from "../lib/entries";

export type StreamPhase = "connecting" | "streaming" | "finished" | "error";

const FLUSH_MS = 60;
const MAX_BACKOFF_MS = 10_000;

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
 * useTaskEvents follows StreamTaskEvents for one task, as entries (lib/entries).
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
  const [entries, setEntries] = useState<Entry[]>([]);
  const [phase, setPhase] = useState<StreamPhase>("connecting");
  const [error, setError] = useState<string>();
  const notify = useRef(onStatusEvent);
  useEffect(() => {
    notify.current = onStatusEvent;
  }, [onStatusEvent]);

  useEffect(() => {
    const ctrl = new AbortController();
    const stream = new EntryStream();
    let lastSeq = 0n;
    let pending: Entry[] = [];
    let timer: ReturnType<typeof setTimeout> | undefined;

    const flush = () => {
      timer = undefined;
      if (pending.length > 0) {
        const batch = pending;
        pending = [];
        setEntries((prev) => prev.concat(batch));
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
            pending = pending.concat(stream.append(ev));
            if (ev.kind !== TaskEventKind.LOG) notify.current();
            schedule();
          }
          pending = pending.concat(stream.flush());
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

  return { entries, phase, error };
}
