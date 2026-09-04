import { createElement, type ReactNode } from "react";

/**
 * A deliberately small markdown subset, rendered as a React element tree.
 *
 * This is the one place in Podium where a task's output reaches a browser as markup, so it
 * is a security boundary and it is built as one:
 *
 * - **No raw HTML is ever emitted.** There is no `dangerouslySetInnerHTML` here and there
 *   must never be one. Every string ends up as a React text node, which React escapes by
 *   construction, so `<script>alert(1)</script>` in an answer is five words on a page.
 * - **Only `http:` and `https:` links are links.** Anything else — `javascript:`, `data:`,
 *   a bare `mailto:` — does not match the link rule at all and is rendered as the literal
 *   characters the agent wrote.
 * - **No library.** A markdown parser is a large dependency with a large attack surface for
 *   a feature that needs six constructs.
 *
 * What is supported, and nothing else: paragraphs separated by a blank line, fenced code
 * blocks (the language is ignored), inline code, `**bold**`, `*italic*`, `- ` lists, and
 * `[text](http(s)://…)`. Tables arrive as fenced blocks because the analyst prompt asks for
 * that; there is no table syntax here on purpose.
 */

/** FENCE opens and closes a code block. The info string after it is read and ignored. */
const FENCE = "```";

/** BULLET is the one list marker. `* ` is not one: it collides with italic. */
const BULLET = "- ";

/**
 * INLINE matches the four inline constructs, leftmost-first. The order inside the
 * alternation decides ties at the same position, which is why code comes before bold (so
 * `**` inside a code span is literal) and bold before italic (so `**x**` is not two
 * italics).
 */
const INLINE =
  /(`[^`\n]+`)|(\[[^\]\n]*\]\((https?:\/\/[^\s)]+)\))|(\*\*[^\n]+?\*\*)|(\*[^*\n]+\*)/;

export type Block =
  | { kind: "paragraph"; text: string }
  | { kind: "code"; text: string }
  | { kind: "list"; items: string[] };

/**
 * parseBlocks splits text into blocks. Exported for its own test: the block structure is
 * worth asserting without a renderer in the way.
 *
 * An unclosed fence runs to the end of the message, which is what a truncated answer looks
 * like — showing the rest as a code block is closer to the truth than showing the ``` as
 * text.
 */
export function parseBlocks(text: string): Block[] {
  const lines = text.replace(/\r\n?/g, "\n").split("\n");
  const blocks: Block[] = [];
  let paragraph: string[] = [];

  const flushParagraph = () => {
    if (paragraph.length > 0) {
      blocks.push({ kind: "paragraph", text: paragraph.join("\n") });
      paragraph = [];
    }
  };

  for (let i = 0; i < lines.length; i++) {
    const line = lines[i];

    if (line.trimStart().startsWith(FENCE)) {
      flushParagraph();
      const body: string[] = [];
      i++;
      while (i < lines.length && !lines[i].trimStart().startsWith(FENCE)) {
        body.push(lines[i]);
        i++;
      }
      blocks.push({ kind: "code", text: body.join("\n") });
      continue;
    }

    if (line.startsWith(BULLET)) {
      flushParagraph();
      const items: string[] = [];
      while (i < lines.length && lines[i].startsWith(BULLET)) {
        items.push(lines[i].slice(BULLET.length));
        i++;
      }
      i--;
      blocks.push({ kind: "list", items });
      continue;
    }

    if (line.trim() === "") {
      flushParagraph();
      continue;
    }
    paragraph.push(line);
  }
  flushParagraph();
  return blocks;
}

/**
 * renderInline turns one line into React nodes. Every branch produces either an element
 * with parsed children or a plain string; nothing produces markup.
 */
export function renderInline(text: string, keyPrefix = ""): ReactNode[] {
  const out: ReactNode[] = [];
  let rest = text;
  let n = 0;

  while (rest.length > 0) {
    const m = INLINE.exec(rest);
    if (!m || m.index === undefined) {
      out.push(rest);
      break;
    }
    if (m.index > 0) out.push(rest.slice(0, m.index));
    const key = `${keyPrefix}i${n++}`;
    const [whole, code, link, href, bold, italic] = m;

    if (code !== undefined) {
      out.push(
        createElement(
          "code",
          { key, className: "rounded bg-raised px-1 font-mono text-[0.9em]" },
          code.slice(1, -1),
        ),
      );
    } else if (link !== undefined && href !== undefined) {
      // The label is parsed for inline constructs; the href is the matched http(s) URL and
      // nothing else can reach this branch.
      const label = link.slice(1, link.indexOf("]("));
      out.push(
        createElement(
          "a",
          {
            key,
            href,
            target: "_blank",
            rel: "noreferrer noopener",
            className: "text-accent underline",
          },
          label === "" ? href : renderInline(label, `${key}-`),
        ),
      );
    } else if (bold !== undefined) {
      out.push(
        createElement(
          "strong",
          { key, className: "font-semibold" },
          renderInline(bold.slice(2, -2), `${key}-`),
        ),
      );
    } else if (italic !== undefined) {
      out.push(
        createElement("em", { key }, renderInline(italic.slice(1, -1), `${key}-`)),
      );
    }
    rest = rest.slice(m.index + whole.length);
  }
  return out;
}

/**
 * renderMarkdown is the whole public surface: a message's text as an array of block
 * elements, ready to drop into a bubble.
 */
export function renderMarkdown(text: string, keyPrefix = ""): ReactNode[] {
  return parseBlocks(text).map((block, i) => {
    const key = `${keyPrefix}b${i}`;
    switch (block.kind) {
      case "code":
        return createElement(
          "pre",
          {
            key,
            className:
              "my-2 overflow-x-auto rounded border border-border bg-bg px-3 py-2 text-xs",
          },
          createElement("code", { className: "font-mono" }, block.text),
        );
      case "list":
        return createElement(
          "ul",
          { key, className: "my-2 list-disc space-y-0.5 pl-5" },
          block.items.map((item, j) =>
            createElement("li", { key: `${key}l${j}` }, renderInline(item, `${key}l${j}-`)),
          ),
        );
      default:
        return createElement(
          "p",
          { key, className: "whitespace-pre-wrap break-words" },
          renderInline(block.text, `${key}-`),
        );
    }
  });
}
