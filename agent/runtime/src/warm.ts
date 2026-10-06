/** WarmEnv is the seconds the node copied from the task spec's workspace_warm. */
export const WarmEnv = "PODIUM_WORKSPACE_WARM_SECONDS";

/** DryRunEnv skips the model, and a dry run does not sit in the warm window. */
export const DryRunEnv = "PODIUM_AGENT_DRY_RUN";

/**
 * warmMillis is how long to wait for a follow-up after an answer. Zero means leave
 * when the turn ends. A dry run is always zero: the task has to exit so a test can
 * see it finish.
 */
export function warmMillis(env: Record<string, string | undefined>): number {
  if (env[DryRunEnv] === "1") {
    return 0;
  }
  const n = Number.parseInt(env[WarmEnv] ?? "", 10);
  if (!Number.isFinite(n) || n <= 0) {
    return 0;
  }
  return n * 1000;
}

/**
 * nextInboxLine waits up to timeoutMs for the next inbox line.
 *
 * The pending read is returned on timeout so a line that loses the race is not
 * dropped on the floor: the caller keeps it for the next wait. When this process
 * is about to exit, that pending read goes with it.
 */
export async function nextInboxLine(
  next: () => Promise<IteratorResult<string>>,
  pending: Promise<IteratorResult<string>> | undefined,
  timeoutMs: number,
  signal?: AbortSignal,
): Promise<{ text?: string; pending?: Promise<IteratorResult<string>> }> {
  if (signal?.aborted || timeoutMs <= 0) {
    return { pending };
  }
  const read = pending ?? next();
  let winner: "line" | "timeout" | undefined;
  const line = read.then((result) => {
    if (winner === undefined) {
      winner = "line";
    }
    return result;
  });
  let timer: ReturnType<typeof setTimeout> | undefined;
  const timeout = new Promise<void>((resolve) => {
    const stop = () => {
      if (timer !== undefined) {
        clearTimeout(timer);
      }
      if (winner === undefined) {
        winner = "timeout";
      }
      resolve();
    };
    timer = setTimeout(stop, timeoutMs);
    if (signal) {
      if (signal.aborted) {
        stop();
      } else {
        signal.addEventListener("abort", stop, { once: true });
      }
    }
  });
  await Promise.race([line.then(() => undefined), timeout]);
  if (winner === "line") {
    const result = await line;
    if (result.done) {
      return {};
    }
    return { text: result.value };
  }
  return { pending: read };
}
