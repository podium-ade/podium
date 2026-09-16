import { describe, expect, it } from "vitest";
import { docToYaml, parseYaml, problemText, YamlReader } from "./yaml";

describe("parseYaml", () => {
  it("indexes every key so a later walk can point at the source", () => {
    const { value, problems, lines } = parseYaml(
      "image: alpine:3\nprivileged: true\nsidecars:\n  db:\n    hostname: x\n",
    );
    expect(problems).toEqual([]);
    expect(value).toMatchObject({ image: "alpine:3", privileged: true });
    expect(lines.get("image")?.line).toBe(1);
    expect(lines.get("privileged")?.line).toBe(2);
    expect(lines.get("sidecars")?.line).toBe(3);
    expect(lines.get("sidecars.db")?.line).toBe(4);
    expect(lines.get("sidecars.db.hostname")?.line).toBe(5);
  });

  it("returns a syntax error instead of a value", () => {
    const { value, problems } = parseYaml("image: [unclosed\n");
    expect(value).toBeUndefined();
    expect(problems).toHaveLength(1);
    expect(problems[0].message.length).toBeGreaterThan(0);
    expect(problems[0].line).toBeDefined();
  });

  it("treats a comments-only document as empty", () => {
    const { problems } = parseYaml("# just a comment\n");
    expect(problems).toEqual([{ path: "", message: "the document is empty" }]);
  });
});

describe("YamlReader", () => {
  it("refuses a key the schema does not have and keeps the line", () => {
    const { value, lines } = parseYaml("image: alpine:3\nprivileged: true\n");
    const r = new YamlReader(lines);
    r.object("", value, ["image"], "task spec field");
    expect(r.problems).toHaveLength(1);
    expect(problemText(r.problems[0])).toContain("privileged");
    expect(problemText(r.problems[0])).toContain("is not a task spec field");
    expect(r.problems[0].line).toBe(2);
  });

  it("reports a root value that is not a mapping the way the spec decoder always has", () => {
    const { value, lines } = parseYaml("- a\n- b\n");
    const r = new YamlReader(lines);
    r.object("", value, ["image"], "task spec field");
    expect(r.problems.map(problemText)).toEqual([": must be a mapping"]);
  });
});

describe("docToYaml", () => {
  it("round-trips a mapping without wrapping", () => {
    const text = docToYaml({ image: "alpine:3", labels: ["linux"] });
    expect(text).toBe("image: alpine:3\nlabels:\n  - linux\n");
  });
});
