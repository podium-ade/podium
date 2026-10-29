import { describe, expect, it } from "vitest";
import { TaskEventKind, type TaskEvent } from "../gen/podium/v1/node_pb";
import {
  accountingOf,
  detailOf,
  EntryStream,
  firstError,
  hasStory,
  haystack,
  isTranscript,
  type Entry,
} from "./entries";
import { messageTone } from "./format";
import {
  activityEvent,
  at,
  errorEvent,
  exitedEvent,
  lifecycleEvent,
  logEvent,
  messageEvent,
  sidecarLogEvent,
  stderr,
  stdout,
} from "../test/events";

function entries(...events: TaskEvent[]): Entry[] {
  const s = new EntryStream();
  return [...events.flatMap((ev) => s.append(ev)), ...s.flush()];
}

describe("detailOf", () => {
  it("summarises a message as its type and first line", () => {
    expect(detailOf(messageEvent(1, "final", "the answer\nand the rest"))).toBe("final: the answer");
  });

  it("truncates a long first line", () => {
    const detail = detailOf(messageEvent(1, "progress", "x".repeat(400)));
    expect(detail).toBe(`progress: ${"x".repeat(200)}…`);
  });
});

describe("messageTone", () => {
  it("distinguishes an answer from a progress note and from anything else", () => {
    expect(messageTone("final")).toBe("ok");
    expect(messageTone("progress")).toBe("run");
    expect(messageTone("question")).toBe("idle");
  });
});

describe("EntryStream", () => {
  it("makes one entry per completed line, keyed by the event and the line's place in it", () => {
    const es = entries(logEvent(1, stdout, "one\ntwo\nthr"), logEvent(2, stdout, "ee\n"), logEvent(3, stderr, "tail"));
    expect(es.map((e) => [e.key, e.summary, e.source])).toEqual([
      ["1.0", "one", "stdout"],
      ["1.1", "two", "stdout"],
      ["2.0", "three", "stdout"],
      ["end.0", "tail", "stderr"],
    ]);
    expect(es.map((e) => e.index)).toEqual([0, 1, 2, 3]);
  });

  it("keys the same stream the same way on a replay, so a link lands on the same row", () => {
    const events = () => [logEvent(1, stdout, "a\nb\n"), messageEvent(2, "progress", "hi"), logEvent(3, stdout, "c\n")];
    expect(entries(...events()).map((e) => e.key)).toEqual(entries(...events()).map((e) => e.key));
  });

  it("names a sidecar's lines by the sidecar", () => {
    const [e] = entries(sidecarLogEvent(1, "db", "ready\n"));
    expect(e.source).toBe("sidecar:db");
    expect(e.summary).toBe("[db] ready");
  });

  it("carries the last known time forward to an entry that has none", () => {
    const es = entries(at(logEvent(1, stdout, "a\n"), 5_000), logEvent(2, stdout, "b\n"));
    expect(es.map((e) => e.at)).toEqual([5_000, 5_000]);
  });

  it("reads a tool call out of an activity message", () => {
    const [e] = entries(
      activityEvent(1, {
        kind: "tool",
        tool: "bash",
        status: "completed",
        title: "printf …",
        input: JSON.stringify({ command: "cat /workspace/.podium/review.json" }),
        output: '{"verdict":"approve"}\n',
      }),
    );
    expect(e.kind).toBe("tool");
    expect(e.source).toBe("tool:bash");
    expect(e.summary).toBe("cat /workspace/.podium/review.json");
    expect(e.severity).toBe("info");
    expect(haystack(e)).toContain("approve");
  });

  it("marks a failed tool call as an error", () => {
    const [e] = entries(activityEvent(1, { kind: "tool", tool: "read", status: "error", error: "no such file" }));
    expect(e.severity).toBe("error");
  });

  it("reads a reasoning block", () => {
    const [e] = entries(activityEvent(1, { kind: "reasoning", text: "first\nsecond" }));
    expect([e.kind, e.source, e.summary]).toEqual(["reasoning", "agent", "first"]);
  });

  it("keeps an activity it does not understand as a plain message", () => {
    const [e] = entries(messageEvent(1, "activity", "not json"));
    expect(e.kind).toBe("message");
    expect(e.summary).toBe("activity: not json");
  });

  it("summarises accounting as turns and cost", () => {
    const text = JSON.stringify({ num_turns: 3, total_cost_usd: 0.0123 });
    const [e] = entries(messageEvent(1, "accounting", text));
    expect(e.summary).toBe("accounting: 3 turns, $0.0123");
    expect(accountingOf(text)).toEqual({ turns: 3, costUsd: 0.0123 });
    expect(isTranscript(e)).toBe(false);
  });

  it("grades lifecycle events by whether the run went wrong", () => {
    const es = entries(
      lifecycleEvent(1, TaskEventKind.STARTED),
      exitedEvent(2, 0),
      exitedEvent(3, 1),
      exitedEvent(4, 0, true),
      errorEvent(5, "registry down", true),
      errorEvent(6, "no such image"),
    );
    expect(es.map((e) => [e.summary, e.severity])).toEqual([
      ["started", "info"],
      ["exited · exit 0", "info"],
      ["exited · exit 1", "error"],
      ["exited · exit 0, OOM-killed", "error"],
      ["error · registry down (retryable)", "warn"],
      ["error · no such image", "error"],
    ]);
    expect(es.every((e) => e.source === "node")).toBe(true);
  });
});

describe("the story", () => {
  const es = entries(
    lifecycleEvent(1, TaskEventKind.STARTED),
    logEvent(2, stderr, "warn: slow\n"),
    messageEvent(3, "progress", "looking"),
    activityEvent(4, { kind: "tool", tool: "bash", status: "completed" }),
    errorEvent(5, "boom"),
  );

  it("is what the task said and did, and the node's errors", () => {
    expect(es.filter(isTranscript).map((e) => e.key)).toEqual(["3", "4", "5"]);
    expect(hasStory(es)).toBe(true);
    expect(hasStory(es.slice(0, 2))).toBe(false);
  });

  it("finds the first error, and no warning stands in for one", () => {
    expect(firstError(es)?.key).toBe("5");
    expect(firstError(es.slice(0, 2))).toBeUndefined();
  });
});
