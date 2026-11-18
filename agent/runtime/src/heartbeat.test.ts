import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { startHeartbeat } from "./heartbeat.js";

describe("startHeartbeat", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  it("sends a heartbeat message every interval until stopped", async () => {
    const sent: { argv: string[]; text: string }[] = [];
    const stop = startHeartbeat(async (argv, text) => {
      sent.push({ argv, text });
    }, 1_000);

    await vi.advanceTimersByTimeAsync(3_500);
    expect(sent).toHaveLength(3);
    expect(sent[0]?.argv).toEqual(["message", "--type", "heartbeat", "-"]);
    expect(JSON.parse(sent[0]?.text ?? "")).toEqual({ v: 1 });

    stop();
    await vi.advanceTimersByTimeAsync(5_000);
    expect(sent).toHaveLength(3);
  });

  it("does not stack sends behind a runner that is slow to answer", async () => {
    let calls = 0;
    let release: (() => void) | undefined;
    const stop = startHeartbeat(() => {
      calls += 1;
      return new Promise<void>((resolve) => {
        release = resolve;
      });
    }, 1_000);

    await vi.advanceTimersByTimeAsync(5_500);
    expect(calls).toBe(1);
    release?.();
    await vi.advanceTimersByTimeAsync(1_000);
    expect(calls).toBe(2);
    stop();
  });

  it("survives a runner that refuses it", async () => {
    let calls = 0;
    const stderr = vi.spyOn(process.stderr, "write").mockImplementation(() => true);
    const stop = startHeartbeat(async () => {
      calls += 1;
      throw new Error("no runner");
    }, 1_000);

    await vi.advanceTimersByTimeAsync(3_500);
    expect(calls).toBe(3);
    // Warned once for the run of failures, not once a minute.
    expect(stderr).toHaveBeenCalledTimes(1);
    stop();
    stderr.mockRestore();
  });
});
