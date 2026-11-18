import { findIssueRefs } from "./linearIssues";

/** The few hast shapes this plugin touches. */
type HastText = { type: "text"; value: string };
type HastElement = { type: "element"; tagName: string; properties?: Record<string, unknown>; children: HastNode[] };
type HastNode = HastText | HastElement | { type: string; children?: HastNode[] };

/** Text inside these is literal, or already a link, and never becomes a chip. */
const SKIP = new Set(["code", "pre", "a", "kbd", "samp"]);

/** LINEAR_REF_ATTR is the attribute a candidate's span carries, as React sees it. */
export const LINEAR_REF_ATTR = "data-linear-ref";

/**
 * rehypeLinearRefs wraps every candidate Linear identifier in a message's text in a span marked
 * with the identifier, which MarkdownText's span renders as a chip once the conductor says the
 * viewer may see that issue. It runs AFTER rehype-sanitize, so the attribute is this plugin's and
 * survives; a span an agent wrote with the same attribute is only ever a question to the
 * conductor, which answers it for this viewer like any other.
 */
export function rehypeLinearRefs() {
  return (tree: HastNode) => {
    walk(tree);
  };
}

function walk(node: HastNode) {
  if (!("children" in node) || !node.children) return;
  if (node.type === "element" && SKIP.has((node as HastElement).tagName)) return;
  const out: HastNode[] = [];
  let changed = false;
  for (const child of node.children) {
    if (child.type !== "text") {
      walk(child);
      out.push(child);
      continue;
    }
    const value = (child as HastText).value;
    const matches = findIssueRefs(value);
    if (matches.length === 0) {
      out.push(child);
      continue;
    }
    changed = true;
    let at = 0;
    for (const m of matches) {
      if (m.start > at) out.push({ type: "text", value: value.slice(at, m.start) });
      out.push({
        type: "element",
        tagName: "span",
        properties: { dataLinearRef: m.ref },
        children: [{ type: "text", value: value.slice(m.start, m.end) }],
      });
      at = m.end;
    }
    if (at < value.length) out.push({ type: "text", value: value.slice(at) });
  }
  if (changed) node.children = out;
}
