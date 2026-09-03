import { describe, expect, it } from "vitest";
import { create } from "@bufbuild/protobuf";
import { TaskEventKind, TaskEventSchema } from "../gen/podium/v1/node_pb";
import { Accumulator, filterLines, sidecarNames, toRawText } from "./logs";
import { logEvent, sidecarLogEvent, stderr, stdout } from "../test/events";

describe("Accumulator", () => {
  it("emits lines in seq order across streams", () => {
    const acc = new Accumulator();
    const lines = [
      ...acc.append(logEvent(1, stdout, "one\n")),
      ...acc.append(logEvent(2, stderr, "boom\n")),
      ...acc.append(logEvent(3, stdout, "two\n")),
    ];
    expect(lines.map((l) => l.text)).toEqual(["one", "boom", "two"]);
    expect(lines.map((l) => l.stream)).toEqual(["stdout", "stderr", "stdout"]);
    expect(lines.map((l) => l.id)).toEqual([0, 1, 2]);
  });

  it("joins a line split across chunks of the same stream", () => {
    const acc = new Accumulator();
    expect(acc.append(logEvent(1, stdout, "tick "))).toEqual([]);
    expect(acc.append(logEvent(2, stderr, "err\n")).map((l) => l.text)).toEqual(["err"]);
    expect(acc.append(logEvent(3, stdout, "1\ntick 2\n")).map((l) => l.text)).toEqual([
      "tick 1",
      "tick 2",
    ]);
  });

  it("emits several lines from one chunk and keeps the unterminated tail", () => {
    const acc = new Accumulator();
    expect(acc.append(logEvent(1, stdout, "a\nb\nc")).map((l) => l.text)).toEqual(["a", "b"]);
    expect(acc.flush().map((l) => l.text)).toEqual(["c"]);
    expect(acc.flush()).toEqual([]);
  });

  it("strips a trailing CR", () => {
    const acc = new Accumulator();
    expect(acc.append(logEvent(1, stdout, "windows\r\n"))[0].text).toBe("windows");
  });

  it("ignores events that are not logs, including a kind with no payload", () => {
    const acc = new Accumulator();
    const started = create(TaskEventSchema, { seq: 7n, kind: TaskEventKind.STARTED });
    expect(acc.append(started)).toEqual([]);
  });
});

describe("filterLines", () => {
  const acc = new Accumulator();
  const lines = [
    ...acc.append(logEvent(1, stdout, "hello world\n")),
    ...acc.append(logEvent(2, stderr, "warning: HELLO\n")),
    ...acc.append(logEvent(3, stdout, "goodbye\n")),
  ];
  const all = { stdout: true, stderr: true, sidecars: {}, search: "" };

  it("filters by stream", () => {
    expect(filterLines(lines, { ...all, stderr: false })).toHaveLength(2);
    expect(filterLines(lines, { ...all, stdout: false })).toHaveLength(1);
  });

  it("searches case-insensitively", () => {
    const hits = filterLines(lines, { ...all, search: "hello" });
    expect(hits.map((l) => l.text)).toEqual(["hello world", "warning: HELLO"]);
  });

  it("combines stream and search", () => {
    const hits = filterLines(lines, { ...all, stdout: false, search: "hello" });
    expect(hits.map((l) => l.text)).toEqual(["warning: HELLO"]);
  });
});

describe("sidecar filtering", () => {
  const acc = new Accumulator();
  const lines = [
    ...acc.append(logEvent(1, stdout, "task line\n")),
    ...acc.append(sidecarLogEvent(2, "db", "db ready\n")),
    ...acc.append(sidecarLogEvent(3, "cache", "cache ready\n")),
    ...acc.append(sidecarLogEvent(4, "db", "db again\n")),
  ];
  const all = { stdout: true, stderr: true, sidecars: {}, search: "" };

  it("lists the sidecars that have produced output, in first-seen order", () => {
    expect(sidecarNames(lines)).toEqual(["db", "cache"]);
    expect(sidecarNames(lines.slice(0, 1))).toEqual([]);
  });

  it("hides one sidecar without touching the others or the task", () => {
    const hits = filterLines(lines, { ...all, sidecars: { db: false } });
    expect(hits.map((l) => l.text)).toEqual(["task line", "cache ready"]);
  });

  it("shows a sidecar it has never been told about", () => {
    const hits = filterLines(lines, { ...all, sidecars: { somethingElse: false } });
    expect(hits).toHaveLength(4);
  });

  it("keeps stdout and stderr independent of the sidecar filter", () => {
    const hits = filterLines(lines, { ...all, stdout: false, sidecars: { cache: false } });
    expect(hits.map((l) => l.text)).toEqual(["db ready", "db again"]);
  });

  it("buckets an unnamed sidecar chunk under \"sidecar\"", () => {
    const a = new Accumulator();
    const unnamed = a.append(sidecarLogEvent(1, "", "anonymous\n"));
    expect(sidecarNames(unnamed)).toEqual(["sidecar"]);
    expect(filterLines(unnamed, { ...all, sidecars: { sidecar: false } })).toHaveLength(0);
  });
});

describe("toRawText", () => {
  it("is newline terminated, and empty for no lines", () => {
    const acc = new Accumulator();
    const lines = acc.append(logEvent(1, stdout, "a\nb\n"));
    expect(toRawText(lines)).toBe("a\nb\n");
    expect(toRawText([])).toBe("");
  });
});
