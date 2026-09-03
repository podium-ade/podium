import { describe, expect, it } from "vitest";
import { render } from "@testing-library/react";
import { createElement } from "react";
import { parseBlocks, renderMarkdown } from "./markdown";

/** show renders a message and returns the container, so the assertions read as HTML. */
function show(text: string): HTMLElement {
  const { container } = render(createElement("div", null, renderMarkdown(text)));
  return container;
}

describe("parseBlocks", () => {
  it("splits paragraphs on a blank line and keeps soft line breaks", () => {
    expect(parseBlocks("one\ntwo\n\nthree")).toEqual([
      { kind: "paragraph", text: "one\ntwo" },
      { kind: "paragraph", text: "three" },
    ]);
  });

  it("reads a fenced block and ignores the language", () => {
    expect(parseBlocks("before\n\n```sql\nselect 1;\n```\n\nafter")).toEqual([
      { kind: "paragraph", text: "before" },
      { kind: "code", text: "select 1;" },
      { kind: "paragraph", text: "after" },
    ]);
  });

  it("runs an unclosed fence to the end of the message", () => {
    // A truncated answer looks exactly like this, and showing the rest as code is closer to
    // the truth than showing the ``` as text.
    expect(parseBlocks("here:\n```\nselect 1;\nselect 2;")).toEqual([
      { kind: "paragraph", text: "here:" },
      { kind: "code", text: "select 1;\nselect 2;" },
    ]);
  });

  it("keeps blank lines inside a fenced block", () => {
    expect(parseBlocks("```\na\n\nb\n```")).toEqual([{ kind: "code", text: "a\n\nb" }]);
  });

  it("groups consecutive bullets into one list", () => {
    expect(parseBlocks("- one\n- two\n\ntext\n- three")).toEqual([
      { kind: "list", items: ["one", "two"] },
      { kind: "paragraph", text: "text" },
      { kind: "list", items: ["three"] },
    ]);
  });

  it("has no blocks for an empty message", () => {
    expect(parseBlocks("")).toEqual([]);
    expect(parseBlocks("\n\n  \n")).toEqual([]);
  });
});

describe("renderMarkdown", () => {
  it("renders a paragraph", () => {
    expect(show("4,812 active accounts in August.").textContent).toBe(
      "4,812 active accounts in August.",
    );
  });

  it("renders a fenced block as pre > code", () => {
    const el = show("```sql\nselect count(*) from accounts;\n```");
    const pre = el.querySelector("pre > code");
    expect(pre?.textContent).toBe("select count(*) from accounts;");
  });

  it("renders inline code", () => {
    const el = show("the column is `is_internal`");
    expect(el.querySelectorAll("pre")).toHaveLength(0);
    expect(el.querySelector("code")?.textContent).toBe("is_internal");
  });

  it("renders bold and italic", () => {
    const el = show("**loud** and *quiet*");
    expect(el.querySelector("strong")?.textContent).toBe("loud");
    expect(el.querySelector("em")?.textContent).toBe("quiet");
  });

  it("renders inline code nested inside bold", () => {
    const el = show("**run `psql --version` first**");
    const strong = el.querySelector("strong");
    expect(strong?.textContent).toBe("run psql --version first");
    expect(strong?.querySelector("code")?.textContent).toBe("psql --version");
  });

  it("does not read markdown inside a code span", () => {
    const el = show("`**not bold**`");
    expect(el.querySelector("strong")).toBeNull();
    expect(el.querySelector("code")?.textContent).toBe("**not bold**");
  });

  it("renders a list", () => {
    const el = show("- one\n- two with `code`");
    const items = el.querySelectorAll("li");
    expect(items).toHaveLength(2);
    expect(items[0].textContent).toBe("one");
    expect(items[1].querySelector("code")?.textContent).toBe("code");
  });

  it("renders an http(s) link that opens safely", () => {
    const el = show("see [the docs](https://example.com/a?b=1) for more");
    const a = el.querySelector("a");
    expect(a?.getAttribute("href")).toBe("https://example.com/a?b=1");
    expect(a?.textContent).toBe("the docs");
    expect(a?.getAttribute("target")).toBe("_blank");
    // Without noopener a new tab can reach back into this one through window.opener.
    expect(a?.getAttribute("rel")).toContain("noopener");
    expect(a?.getAttribute("rel")).toContain("noreferrer");
  });

  it("renders a bare http link with the URL as its own label", () => {
    const el = show("[](https://example.com/x)");
    expect(el.querySelector("a")?.textContent).toBe("https://example.com/x");
  });
});

describe("renderMarkdown is a security boundary", () => {
  it("renders a javascript: link as literal text", () => {
    // The acceptance item. Nothing about this may become an anchor, and the characters the
    // agent wrote are what a human sees.
    const el = show("[x](javascript:alert(1))");
    expect(el.querySelectorAll("a")).toHaveLength(0);
    expect(el.textContent).toBe("[x](javascript:alert(1))");
  });

  it.each([
    "[x](data:text/html;base64,PHNjcmlwdD5hbGVydCgxKTwvc2NyaXB0Pg==)",
    "[x](vbscript:msgbox(1))",
    "[x](JavaScript:alert(1))",
    "[x](  javascript:alert(1))",
    "[x](/relative/path)",
    "[x](//example.com)",
    "[x](mailto:someone@example.com)",
    "[x](ftp://example.com/f)",
    "[x](file:///etc/passwd)",
  ])("renders %s as literal text", (input) => {
    const el = show(input);
    expect(el.querySelectorAll("a")).toHaveLength(0);
    expect(el.textContent).toBe(input);
  });

  it("never emits raw HTML", () => {
    const el = show(
      "<script>alert(1)</script>\n\n<img src=x onerror=alert(1)>\n\n" +
        "<iframe src=\"https://evil.example\"></iframe> &lt;b&gt;",
    );
    expect(el.querySelectorAll("script")).toHaveLength(0);
    expect(el.querySelectorAll("img")).toHaveLength(0);
    expect(el.querySelectorAll("iframe")).toHaveLength(0);
    // The tags are text, character for character, entities included.
    expect(el.textContent).toContain("<script>alert(1)</script>");
    expect(el.textContent).toContain("<img src=x onerror=alert(1)>");
    expect(el.textContent).toContain("&lt;b&gt;");
  });

  it("never emits raw HTML from inside a code block either", () => {
    const el = show("```\n<script>alert(1)</script>\n```");
    expect(el.querySelectorAll("script")).toHaveLength(0);
    expect(el.querySelector("pre > code")?.textContent).toBe("<script>alert(1)</script>");
  });

  it("puts no attribute anywhere it did not choose itself", () => {
    // An href is the only agent-supplied attribute value in the whole renderer, and it is
    // matched against http(s) before it gets there. Everything else — a link label, a code
    // span, a bullet — becomes a text node.
    const el = show('[a" onmouseover="alert(1)](https://example.com)');
    const a = el.querySelector("a");
    expect(a?.getAttribute("href")).toBe("https://example.com");
    expect(a?.getAttribute("onmouseover")).toBeNull();
    expect(a?.textContent).toBe('a" onmouseover="alert(1)');
  });
});
