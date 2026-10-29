import { describe, expect, it } from "vitest";
import { EntryStream, type Entry } from "./entries";
import { parseQuery, tokenize } from "./entryQuery";
import { activityEvent, errorEvent, logEvent, stderr, stdout } from "../test/events";

const s = new EntryStream();
const all: Entry[] = [
  ...s.append(logEvent(1, stdout, "connecting to db\n")),
  ...s.append(logEvent(2, stderr, "warn: request timeout\n")),
  ...s.append(activityEvent(3, { kind: "tool", tool: "bash", status: "completed", input: '{"command":"go test ./..."}', output: "ok all" })),
  ...s.append(activityEvent(4, { kind: "tool", tool: "read", status: "error", error: "no such file" })),
  ...s.append(errorEvent(5, "Time out waiting")),
];

function keys(q: string): string[] {
  const query = parseQuery(q);
  return all.filter(query.test).map((e) => e.key);
}

describe("tokenize", () => {
  it("keeps a quoted phrase and its negation together", () => {
    expect(tokenize('a "b c" -"d e" -f')).toEqual(["a", '"b c"', '-"d e"', "-f"]);
    expect(tokenize("/time ?out/ x -/a b/")).toEqual(["/time ?out/", "x", "-/a b/"]);
  });
});

describe("parseQuery", () => {
  it("matches everything when empty", () => {
    expect(parseQuery("  ").empty).toBe(true);
    expect(keys("")).toHaveLength(all.length);
  });

  it("searches text, ignoring case, including a tool call's input and output", () => {
    expect(keys("TIMEOUT")).toEqual(["2.0"]);
    expect(keys("all")).toEqual(["3"]);
  });

  it("ANDs terms and negates with a minus", () => {
    expect(keys("source:tool -source:tool:read")).toEqual(["3"]);
    expect(keys("source:std -warn")).toEqual(["1.0"]);
  });

  it("matches a quoted phrase and a regular expression", () => {
    expect(keys('"request timeout"')).toEqual(["2.0"]);
    expect(keys("/time ?out/")).toEqual(["2.0", "5"]);
  });

  it("filters by kind and by severity", () => {
    expect(keys("kind:tool")).toEqual(["3", "4"]);
    expect(keys("severity:error")).toEqual(["4", "5"]);
    expect(keys("severity>=warn")).toEqual(["2.0", "4", "5"]);
  });

  it("reports a term it cannot read and applies the rest", () => {
    const q = parseQuery("/(/ severity:loud source:tool");
    expect(q.error).toMatch(/regular expression/);
    expect(all.filter(q.test).map((e) => e.key)).toEqual(["3", "4"]);
  });
});
