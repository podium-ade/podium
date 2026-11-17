import { describe, expect, it } from "vitest";
import { rehypeLinearRefs } from "./rehypeLinearRefs";

type Node = { type: string; value?: string; tagName?: string; properties?: Record<string, unknown>; children?: Node[] };

const text = (value: string): Node => ({ type: "text", value });
const el = (tagName: string, ...children: Node[]): Node => ({ type: "element", tagName, properties: {}, children });

function run(tree: Node): Node {
  rehypeLinearRefs()(tree as never);
  return tree;
}

describe("rehypeLinearRefs", () => {
  it("marks each candidate identifier in a paragraph with a span of its own", () => {
    const tree = run({ type: "root", children: [el("p", text("see EME-6 and ENG-1."))] });
    expect(tree.children![0].children).toEqual([
      text("see "),
      { type: "element", tagName: "span", properties: { dataLinearRef: "EME-6" }, children: [text("EME-6")] },
      text(" and "),
      { type: "element", tagName: "span", properties: { dataLinearRef: "ENG-1" }, children: [text("ENG-1")] },
      text("."),
    ]);
  });

  it("goes into emphasis and lists", () => {
    const tree = run({ type: "root", children: [el("ul", el("li", el("strong", text("EME-6"))))] });
    const strong = tree.children![0].children![0].children![0];
    expect(strong.children![0].properties).toEqual({ dataLinearRef: "EME-6" });
  });

  it("leaves code, links and keys as they are", () => {
    for (const tag of ["code", "pre", "a", "kbd"]) {
      const tree = run({ type: "root", children: [el(tag, text("EME-6"))] });
      expect(tree.children![0].children).toEqual([text("EME-6")]);
    }
  });

  it("leaves text with no candidate alone", () => {
    const p = el("p", text("nothing here"));
    run({ type: "root", children: [p] });
    expect(p.children).toEqual([text("nothing here")]);
  });
});
