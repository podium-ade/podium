// Heartbeat: a message that says nothing except that this process is still alive.
//
// Silence alone cannot tell a turn running a twenty-minute test suite from one that has
// wedged: the harness reports a tool only once it has finished, so both are quiet. The
// conductor watches for this message to tell them apart. It comes from a timer, so it stops
// the moment this process stops getting to its event loop, which is exactly the failure a
// quiet stream cannot reveal on its own. The conductor never shows it to anyone.

import { emitMessage, type RunnerInvoke } from "./emit.js";
import { messageOf, warn } from "./report.js";

/** HeartbeatMs is how often the heartbeat is sent. The conductor's wedged threshold is three. */
export const HeartbeatMs = 60_000;

/**
 * startHeartbeat sends one heartbeat every intervalMs until the returned stop is called. A
 * send still in flight when the next is due is not doubled up, and the timer never keeps the
 * process alive on its own.
 */
export function startHeartbeat(invoke: RunnerInvoke, intervalMs = HeartbeatMs): () => void {
  let inFlight = false;
  let failed = false;
  const timer = setInterval(() => {
    if (inFlight) {
      return;
    }
    inFlight = true;
    emitMessage("heartbeat", JSON.stringify({ v: 1 }), [], invoke)
      .then(() => {
        failed = false;
      })
      .catch((err: unknown) => {
        // Once per run of failures: a runner that refuses one will refuse the next sixty.
        if (!failed) {
          warn(`could not send a heartbeat: ${messageOf(err)}`);
        }
        failed = true;
      })
      .finally(() => {
        inFlight = false;
      });
  }, intervalMs);
  timer.unref();
  return () => clearInterval(timer);
}
