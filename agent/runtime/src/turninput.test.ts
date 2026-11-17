import { setTimeout as sleep } from "node:timers/promises";

import { describe, expect, it } from "vitest";

import { TurnInput } from "./turninput.js";

/** pending is an iterator whose next read settles only when release is called. */
function pending<T>(): { iter: AsyncIterator<T>; release: (r: IteratorResult<T>) => void; reads: () => number } {
  let reads = 0;
  let settle: ((r: IteratorResult<T>) => void) | undefined;
  return {
    iter: {
      next: () => {
        reads += 1;
        return new Promise<IteratorResult<T>>((resolve) => {
          settle = resolve;
        });
      },
    },
    release: (r) => settle?.(r),
    reads: () => reads,
  };
}

/** ended is an inbox that has already finished, as a cancelled turn's inbox is. */
function ended<T>(): { iter: AsyncIterator<T>; reads: () => number } {
  let reads = 0;
  return {
    iter: {
      next: async () => {
        reads += 1;
        return { done: true, value: undefined };
      },
    },
    reads: () => reads,
  };
}

describe("TurnInput", () => {
  // The incident: a cancel ends the inbox while the harness is still exiting. The wait must
  // stay on the event loop, so a timer still fires and the harness's exit is still seen.
  it("does not spin on an inbox that has ended", async () => {
    const events = pending<string>();
    const inbox = ended<string>();
    const input = new TurnInput<string, string>(inbox.iter);

    const next = input.next(events.iter);
    await sleep(20);
    events.release({ done: true, value: undefined });

    await expect(next).resolves.toEqual({ kind: "event", r: { done: true, value: undefined } });
    expect(inbox.reads()).toBe(1);
  });

  it("reads the inbox once however many events go by", async () => {
    const inbox = pending<string>();
    const input = new TurnInput<number, string>(inbox.iter);
    let n = 0;
    const events: AsyncIterator<number> = { next: async () => ({ done: false, value: n++ }) };

    for (let i = 0; i < 1_000; i++) {
      const got = await input.next(events);
      expect(got.kind).toBe("event");
    }
    expect(inbox.reads()).toBe(1);

    inbox.release({ done: false, value: "steer" });
    // The settled inject beats a fresh event read only if it was already settled; let it.
    await sleep(0);
    await expect(input.next(events)).resolves.toEqual({ kind: "inject", value: "steer" });
  });

  it("works with no inbox at all", async () => {
    const input = new TurnInput<string, string>(undefined);
    const events: AsyncIterator<string> = { next: async () => ({ done: false, value: "e" }) };
    await expect(input.next(events)).resolves.toEqual({ kind: "event", r: { done: false, value: "e" } });
  });

  it("keeps an in-flight inbox read across a separate wait", async () => {
    const inbox = pending<string>();
    const input = new TurnInput<string, string>(inbox.iter);
    const events = pending<string>();

    const first = input.next(events.iter);
    events.release({ done: true, value: undefined });
    await first;

    const read = input.takePendingInject();
    expect(read).toBeDefined();
    input.givePendingInject(read);

    inbox.release({ done: false, value: "later" });
    input.resetEvents();
    await expect(input.next(pending<string>().iter)).resolves.toEqual({ kind: "inject", value: "later" });
    expect(inbox.reads()).toBe(1);
  });
});
