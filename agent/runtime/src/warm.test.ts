import { afterEach, describe, expect, it, vi } from "vitest";

import { nextInboxLine, warmMillis } from "./warm.js";

afterEach(() => {
  vi.useRealTimers();
});

describe("warmMillis", () => {
  it("is zero unless the node set a positive number of seconds", () => {
    expect(warmMillis({})).toBe(0);
    expect(warmMillis({ PODIUM_WORKSPACE_WARM_SECONDS: "0" })).toBe(0);
    expect(warmMillis({ PODIUM_WORKSPACE_WARM_SECONDS: "300" })).toBe(300_000);
  });

  it("stays zero for a dry run", () => {
    expect(
      warmMillis({ PODIUM_AGENT_DRY_RUN: "1", PODIUM_WORKSPACE_WARM_SECONDS: "300" }),
    ).toBe(0);
  });
});

describe("nextInboxLine", () => {
  it("returns the line that arrives first", async () => {
    const got = await nextInboxLine(
      () => Promise.resolve({ value: "again", done: false }),
      undefined,
      5_000,
    );
    expect(got.text).toBe("again");
    expect(got.pending).toBeUndefined();
  });

  it("returns on the timeout and keeps the unread line", async () => {
    vi.useFakeTimers();
    let resolve: (result: IteratorResult<string>) => void = () => {};
    const pending = new Promise<IteratorResult<string>>((r) => {
      resolve = r;
    });
    const wait = nextInboxLine(() => pending, undefined, 5_000);
    await vi.advanceTimersByTimeAsync(5_000);
    const got = await wait;
    expect(got.text).toBeUndefined();
    expect(got.pending).toBeDefined();
    resolve({ value: "late", done: false });
    await expect(got.pending).resolves.toEqual({ value: "late", done: false });
  });
});
