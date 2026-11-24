/** The few mdast shapes this plugin touches. */
type MdText = { type: "text"; value: string };
type MdNode = MdText | { type: string; url?: string; children?: MdNode[]; data?: Record<string, unknown> };

/** MENTION_ATTR is the attribute a person chip's span carries, as React sees it. */
export const MENTION_ATTR = "data-mention";

/**
 * remarkMentions turns a person chip, `@[Name](mention:login)`, into a span that
 * MarkdownText draws as `@Name`. The composer writes that token (lib/mentions.ts) and the
 * record keeps it as typed, so it reaches the renderer as a link to a `mention:` URL: a
 * scheme no browser opens and the sanitizer drops, which left the name as a bare, unlinked
 * word behind a stray "@".
 *
 * It runs before rehype-raw and rehype-sanitize, so the span is built from the link's own
 * text, never from HTML. A span an agent writes with the same attribute is just a span
 * with text in it.
 */
export function remarkMentions() {
  return (tree: MdNode) => {
    walk(tree);
  };
}

function walk(node: MdNode) {
  const kids = "children" in node ? node.children : undefined;
  if (!kids) return;
  for (let i = 0; i < kids.length; i++) {
    const child = kids[i];
    if (child.type === "link" && "url" in child && child.url?.startsWith("mention:")) {
      // The "@" the composer writes in front of the link belongs to the chip, which draws
      // its own, so it comes out of the text before it.
      const before = kids[i - 1];
      if (before?.type === "text" && (before as MdText).value.endsWith("@")) {
        (before as MdText).value = (before as MdText).value.slice(0, -1);
      }
      const name = textOf(child.children ?? []);
      child.data = { hName: "span", hProperties: { dataMention: "" } };
      child.children = [{ type: "text", value: `@${name}` }];
      continue;
    }
    walk(child);
  }
}

function textOf(nodes: MdNode[]): string {
  return nodes.map((n) => ("value" in n ? String(n.value) : textOf(n.children ?? []))).join("");
}
