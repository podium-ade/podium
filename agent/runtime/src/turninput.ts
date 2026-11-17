// TurnInput: the two things a working turn waits on at once, the harness's next event and the
// next inject from the inbox, and which of them arrives first.
//
// Two properties are the point of this file, and both were learned from a turn that ran out
// of heap within seconds of being cancelled:
//
//   - Each pending read is wrapped once and the wrapper reused. The inbox read can stay pending
//     for a whole turn while thousands of events go by, and racing it afresh for every event
//     hangs one more reaction, holding that event, on the same promise each time.
//   - An inbox that has ended is not read again. A cancel ends it, and an ended iterator
//     answers `done` at once, so racing it again settles on microtasks alone: the loop never
//     yields to the event loop, never sees the harness exit, and allocates until V8 gives up.

/** TurnInputItem is what arrived first. An inbox that ends is never reported. */
export type TurnInputItem<E, I> = { kind: "event"; r: IteratorResult<E> } | { kind: "inject"; value: I };

type Tagged<E, I> = { kind: "event"; r: IteratorResult<E> } | { kind: "inject"; r: IteratorResult<I> };

export class TurnInput<E, I> {
  private pendingEvent: Promise<Tagged<E, I>> | undefined;
  private injectRead: Promise<IteratorResult<I>> | undefined;
  private injectTagged: Promise<Tagged<E, I>> | undefined;
  private inboxEnded: boolean;

  constructor(private readonly inbox: AsyncIterator<I> | undefined) {
    this.inboxEnded = inbox === undefined;
  }

  /**
   * next resolves with the next event from `events` or the next inject, whichever is first.
   * `events` is the current harness run's iterator; a turn that restarts the harness passes
   * the new one after `resetEvents`.
   */
  async next(events: AsyncIterator<E>): Promise<TurnInputItem<E, I>> {
    for (;;) {
      this.pendingEvent ??= events.next().then((r) => ({ kind: "event" as const, r }));
      const waiters = [this.pendingEvent];
      if (!this.inboxEnded && this.inbox) {
        if (this.injectTagged === undefined) {
          const read = (this.injectRead ??= this.inbox.next());
          this.injectTagged = read.then((r) => ({ kind: "inject" as const, r }));
        }
        waiters.push(this.injectTagged);
      }
      const winner = await Promise.race(waiters);
      if (winner.kind === "event") {
        this.pendingEvent = undefined;
        return winner;
      }
      this.injectRead = undefined;
      this.injectTagged = undefined;
      if (winner.r.done) {
        this.inboxEnded = true;
        continue;
      }
      return { kind: "inject", value: winner.r.value };
    }
  }

  /** resetEvents forgets the read of a harness run that has ended. */
  resetEvents(): void {
    this.pendingEvent = undefined;
  }

  /**
   * takePendingInject hands over an inbox read still in flight, for a wait that reads the
   * inbox on its own (the warm window), so a line that arrives meanwhile is not lost.
   */
  takePendingInject(): Promise<IteratorResult<I>> | undefined {
    const read = this.injectRead;
    this.injectRead = undefined;
    this.injectTagged = undefined;
    return read;
  }

  /** givePendingInject puts back a read that a separate wait left in flight. */
  givePendingInject(read: Promise<IteratorResult<I>> | undefined): void {
    this.injectRead = read;
    this.injectTagged = undefined;
  }
}
