import { Fragment, type ReactNode, useState } from "react";
import { Check, Copy } from "lucide-react";
import { Button } from "../ui/button";
import { Tooltip } from "../ui/tooltip";
import { cn } from "../../lib/utils";

const COPIED_MS = 1400;

/**
 * CopyValue is a monospace value with a quiet copy affordance.
 *
 * Operators paste these straight into a terminal, and a ULID or an image reference selected by
 * hand is a mis-copy waiting to happen.
 */
export function CopyValue({
  value,
  label,
  className,
  children,
}: {
  value: string;
  /** What is being copied, for the button's accessible name: "Copy image". */
  label: string;
  className?: string;
  children?: ReactNode;
}) {
  const [copied, setCopied] = useState(false);
  return (
    <span className={cn("inline-flex min-w-0 items-start gap-1", className)}>
      <span className="min-w-0 font-mono break-words">{children ?? value}</span>
      <Tooltip label={copied ? "Copied" : `Copy ${label}`}>
        <Button
          type="button"
          variant="ghost"
          size="icon-xs"
          aria-label={`Copy ${label}`}
          className="-my-1 shrink-0 text-faint hover:text-fg"
          onClick={() => {
            void navigator.clipboard?.writeText(value);
            setCopied(true);
            setTimeout(() => setCopied(false), COPIED_MS);
          }}
        >
          {copied ? <Check className="text-ok" /> : <Copy />}
        </Button>
      </Tooltip>
    </span>
  );
}

/**
 * PathText wraps a long reference at its own boundaries. `break-all` snaps an image tag in the
 * middle of a token, which makes a perfectly valid reference look corrupt.
 */
export function PathText({ text }: { text: string }) {
  const parts = text.split(/(?<=[/:@])/);
  return (
    <>
      {parts.map((part, i) => (
        <Fragment key={i}>
          {part}
          {i < parts.length - 1 ? <wbr /> : null}
        </Fragment>
      ))}
    </>
  );
}
