import { useEffect, useState } from "react";
import { Check, Copy } from "lucide-react";
import { parseBlocks, renderInline } from "../../../lib/markdown";
import { cn } from "../../../lib/utils";
import { Button } from "../../ui/button";

/** How long the copy button stays in its confirmed state. */
const COPIED_MS = 1600;

/**
 * ChatMarkdown lays out one message.
 *
 * The parsing and every inline element still come from lib/markdown — the security boundary
 * is there and stays there, so nothing on this side can turn an agent's text into markup.
 * What lives here is only the typography of a block and the affordances around it: an answer
 * is read, and a fenced block is usually a command or a diff somebody is about to run, so it
 * gets a copy button and scrolls in its own box rather than widening the transcript.
 *
 * className is the colour of the prose, for the caller with a reason to say. The layout is
 * not negotiable: a thought the task said on its way to an answer is the same markdown, in
 * the same column, at the same size — read in a quieter voice, not set apart.
 */
export function ChatMarkdown({
  text,
  keyPrefix,
  className,
}: {
  text: string;
  keyPrefix: string;
  className?: string;
}) {
  return (
    <div className={cn("min-w-0 space-y-2.5 text-sm leading-relaxed text-fg", className)}>
      {parseBlocks(text).map((block, i) => {
        const key = `${keyPrefix}b${i}`;
        if (block.kind === "code") return <CodeBlock key={key} text={block.text} />;
        if (block.kind === "list") {
          return (
            <ul key={key} className="list-disc space-y-1 pl-5 marker:text-faint">
              {block.items.map((item, j) => (
                <li key={`${key}l${j}`} className="pl-0.5 break-words">
                  {renderInline(item, `${key}l${j}-`)}
                </li>
              ))}
            </ul>
          );
        }
        return (
          <p key={key} className="break-words whitespace-pre-wrap">
            {renderInline(block.text, `${key}-`)}
          </p>
        );
      })}
    </div>
  );
}

function CodeBlock({ text }: { text: string }) {
  const [copied, setCopied] = useState(false);
  const lines = text === "" ? 0 : text.split("\n").length;

  useEffect(() => {
    if (!copied) return;
    const id = setTimeout(() => setCopied(false), COPIED_MS);
    return () => clearTimeout(id);
  }, [copied]);

  return (
    <figure className="overflow-hidden rounded-lg border border-border bg-bg">
      <figcaption className="flex items-center justify-between gap-2 border-b border-hairline bg-panel/70 py-1 pr-1 pl-2.5">
        <span className="tabular text-2xs text-faint">
          {lines} {lines === 1 ? "line" : "lines"}
        </span>
        <Button
          type="button"
          variant="ghost"
          size="icon-xs"
          aria-label={copied ? "Copied" : "Copy code"}
          onClick={() => {
            void navigator.clipboard?.writeText(text);
            setCopied(true);
          }}
        >
          {copied ? <Check className="text-ok" /> : <Copy />}
        </Button>
      </figcaption>
      <pre className="overflow-x-auto px-3 py-2.5 text-xs leading-relaxed">
        <code className="font-mono">{text}</code>
      </pre>
    </figure>
  );
}
