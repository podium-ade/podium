import { memo, useEffect, useState, type CSSProperties, type FC } from "react";
import {
  MarkdownTextPrimitive,
  unstable_memoizeMarkdownComponents as memoizeMarkdownComponents,
  useIsMarkdownCodeBlock,
  type CodeHeaderProps,
  type SyntaxHighlighterProps,
} from "@assistant-ui/react-markdown";
import { createHighlighterCore, type HighlighterCore } from "@shikijs/core";
import { createJavaScriptRegexEngine } from "@shikijs/engine-javascript";
import remarkGfm from "remark-gfm";
import { Check, Copy } from "lucide-react";
import { cn } from "../../../lib/utils";
import { Button } from "../../ui/button";

/**
 * MarkdownText is one text part of a message, as GitHub-flavoured markdown.
 *
 * Agent text is untrusted. react-markdown never renders raw HTML (there is no rehype-raw
 * here, and there must never be one) and its URL transform drops `javascript:` and every
 * other non-web scheme, so `<script>` in an answer is text on a page and a hostile link is
 * inert.
 */
export const MarkdownText = memo(function MarkdownText({ className }: { className?: string }) {
  return (
    <MarkdownTextPrimitive
      remarkPlugins={[remarkGfm]}
      className={cn("min-w-0 text-base leading-7 text-fg break-words", className)}
      components={components}
    />
  );
});

/** COPIED_MS is how long the copy button stays in its confirmed state. */
const COPIED_MS = 1600;

const CodeHeader: FC<CodeHeaderProps> = ({ language, code }) => {
  const [copied, setCopied] = useState(false);
  useEffect(() => {
    if (!copied) return;
    const id = setTimeout(() => setCopied(false), COPIED_MS);
    return () => clearTimeout(id);
  }, [copied]);
  const lines = code ? code.replace(/\n$/, "").split("\n").length : 0;
  return (
    <div className="mt-3 flex items-center justify-between gap-2 rounded-t-xl border border-b-0 border-border bg-panel/80 py-1 pr-1 pl-3">
      <span className="flex min-w-0 items-center gap-2">
        {language && language !== "unknown" ? (
          <span className="font-mono text-2xs font-medium text-muted lowercase">{language}</span>
        ) : null}
        <span className="tabular text-2xs text-faint">
          {lines} {lines === 1 ? "line" : "lines"}
        </span>
      </span>
      <Button
        type="button"
        variant="ghost"
        size="icon-xs"
        aria-label={copied ? "Copied" : "Copy code"}
        onClick={() => {
          if (!code) return;
          void navigator.clipboard?.writeText(code);
          setCopied(true);
        }}
      >
        {copied ? <Check className="text-ok" /> : <Copy />}
      </Button>
    </div>
  );
};

type Highlighter = HighlighterCore;

let highlighter: Promise<Highlighter> | undefined;

/**
 * loadHighlighter builds the one highlighter, on first use: the languages an agent actually
 * writes in, both themes, and the JavaScript regex engine — the full bundle is hundreds of
 * grammars and a WebAssembly engine, all of it embedded in the server binary.
 */
function loadHighlighter(): Promise<Highlighter> {
  highlighter ??= createHighlighterCore({
    themes: [import("@shikijs/themes/github-light"), import("@shikijs/themes/github-dark")],
    langs: [
      import("@shikijs/langs/bash"),
      import("@shikijs/langs/css"),
      import("@shikijs/langs/diff"),
      import("@shikijs/langs/docker"),
      import("@shikijs/langs/go"),
      import("@shikijs/langs/html"),
      import("@shikijs/langs/javascript"),
      import("@shikijs/langs/json"),
      import("@shikijs/langs/make"),
      import("@shikijs/langs/markdown"),
      import("@shikijs/langs/proto"),
      import("@shikijs/langs/python"),
      import("@shikijs/langs/rust"),
      import("@shikijs/langs/sql"),
      import("@shikijs/langs/toml"),
      import("@shikijs/langs/typescript"),
      import("@shikijs/langs/yaml"),
    ],
    engine: createJavaScriptRegexEngine(),
  });
  return highlighter;
}

function useHighlighter(): Highlighter | undefined {
  const [h, setH] = useState<Highlighter>();
  useEffect(() => {
    let live = true;
    void loadHighlighter().then((loaded) => live && setH(loaded));
    return () => {
      live = false;
    };
  }, []);
  return h;
}

const CODE_CLASS =
  "chat-code overflow-x-auto rounded-b-xl border border-t-0 border-border bg-bg px-3 py-2.5 font-mono text-sm leading-relaxed";

/**
 * SyntaxHighlighter colours a fenced block. Tokens become React spans, never HTML, and each
 * carries both themes' colours: index.css picks one with the rest of the palette. A language
 * it does not know, and every block before the highlighter has loaded, is plain text.
 */
const SyntaxHighlighter: FC<SyntaxHighlighterProps> = ({ code, language }) => {
  const h = useHighlighter();
  const text = code.replace(/\n$/, "");
  const known = h?.getLoadedLanguages().includes(language);
  const lines = known
    ? h!.codeToTokens(text, {
        lang: language,
        themes: { light: "github-light", dark: "github-dark" },
        defaultColor: "dark",
      }).tokens
    : undefined;
  return (
    <pre className={CODE_CLASS}>
      <code>
        {lines
          ? lines.map((line, i) => (
              <span key={i}>
                {line.map((t, j) => (
                  <span key={j} style={t.htmlStyle as CSSProperties}>
                    {t.content}
                  </span>
                ))}
                {i < lines.length - 1 ? "\n" : null}
              </span>
            ))
          : text}
      </code>
    </pre>
  );
};

const components = memoizeMarkdownComponents({
  h1: ({ className, ...props }) => (
    <h1 className={cn("mt-5 mb-2 text-xl font-semibold tracking-tight first:mt-0", className)} {...props} />
  ),
  h2: ({ className, ...props }) => (
    <h2 className={cn("mt-5 mb-2 text-lg font-semibold tracking-tight first:mt-0", className)} {...props} />
  ),
  h3: ({ className, ...props }) => (
    <h3 className={cn("mt-4 mb-1.5 text-base font-semibold first:mt-0", className)} {...props} />
  ),
  h4: ({ className, ...props }) => (
    <h4 className={cn("mt-3 mb-1 text-base font-medium first:mt-0", className)} {...props} />
  ),
  p: ({ className, ...props }) => <p className={cn("my-3 first:mt-0 last:mb-0", className)} {...props} />,
  a: ({ className, ...props }) => (
    <a
      className={cn("text-accent underline underline-offset-2 hover:opacity-80", className)}
      target="_blank"
      rel="noreferrer noopener"
      {...props}
    />
  ),
  blockquote: ({ className, ...props }) => (
    <blockquote className={cn("my-3 border-s-2 border-border ps-4 text-muted", className)} {...props} />
  ),
  ul: ({ className, ...props }) => (
    <ul className={cn("my-3 ms-5 list-disc marker:text-faint [&>li]:mt-1", className)} {...props} />
  ),
  ol: ({ className, ...props }) => (
    <ol className={cn("my-3 ms-5 list-decimal marker:text-faint [&>li]:mt-1", className)} {...props} />
  ),
  hr: ({ className, ...props }) => <hr className={cn("my-4 border-hairline", className)} {...props} />,
  table: ({ className, ...props }) => (
    <div className="my-3 overflow-x-auto rounded-lg border border-border">
      <table className={cn("w-full border-collapse text-sm", className)} {...props} />
    </div>
  ),
  thead: ({ className, ...props }) => <thead className={cn("bg-panel", className)} {...props} />,
  th: ({ className, ...props }) => (
    <th
      className={cn(
        "border-b border-border px-3 py-1.5 text-start font-medium text-fg [[align=center]]:text-center [[align=right]]:text-right",
        className,
      )}
      {...props}
    />
  ),
  td: ({ className, ...props }) => (
    <td
      className={cn(
        "border-b border-hairline px-3 py-1.5 text-start align-top [[align=center]]:text-center [[align=right]]:text-right",
        className,
      )}
      {...props}
    />
  ),
  tr: ({ className, ...props }) => <tr className={cn("last:[&>td]:border-b-0", className)} {...props} />,
  strong: ({ className, ...props }) => <strong className={cn("font-semibold", className)} {...props} />,
  pre: ({ className, ...props }) => (
    <pre
      className={cn(
        "overflow-x-auto rounded-b-xl border border-t-0 border-border bg-bg px-3 py-2.5 text-sm leading-relaxed",
        className,
      )}
      {...props}
    />
  ),
  code: function Code({ className, ...props }) {
    const block = useIsMarkdownCodeBlock();
    return (
      <code
        className={cn(!block && "rounded bg-raised px-1 font-mono text-[0.9em]", block && "font-mono", className)}
        {...props}
      />
    );
  },
  CodeHeader,
  SyntaxHighlighter,
});
