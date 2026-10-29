import type { ReactNode } from "react";
import { ChevronRight, Wrench, X } from "lucide-react";
import { pretty } from "../../lib/activity";
import { cn } from "../../lib/utils";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "../ui/collapsible";

/**
 * ToolCallRow is one finished tool call: a line to scan — the tool and what it was pointed
 * at — and its input and output to open. The chat's trail and the task's transcript both
 * draw tool calls with it, so an agent's work reads the same wherever it is shown.
 */
export function ToolCallRow({
  tool,
  summary,
  input,
  output,
  isError = false,
  aside,
  testId = "chat-tool",
}: {
  tool: string;
  summary?: string;
  input?: string;
  output?: string;
  isError?: boolean;
  /** aside is drawn at the end of the line, outside the trigger: a time, an action. */
  aside?: ReactNode;
  testId?: string;
}) {
  const shownInput = pretty(input ?? "");
  return (
    <Collapsible data-testid={testId} data-error={isError || undefined} className="min-w-0">
      <div className="flex min-w-0 items-center gap-2">
        <CollapsibleTrigger className="group/trigger flex min-w-0 flex-1 items-center gap-2 text-left text-sm text-muted transition-colors hover:text-fg">
          {isError ? <X className="size-3.5 shrink-0 text-err" /> : <Wrench className="size-3.5 shrink-0" />}
          <span className={cn("shrink-0 font-mono text-xs", isError ? "text-err" : "text-fg")}>{tool}</span>
          {summary ? <span className="min-w-0 truncate font-mono text-xs">{summary}</span> : null}
          <ChevronRight className="size-3.5 shrink-0 transition-transform group-data-[state=open]/trigger:rotate-90" />
        </CollapsibleTrigger>
        {aside}
      </div>
      <CollapsibleContent className="data-[state=closed]:animate-collapsible-up data-[state=open]:animate-collapsible-down overflow-hidden">
        <div className="mt-1.5 space-y-1.5">
          {shownInput ? <Pre label="Input">{shownInput}</Pre> : null}
          {output ? (
            <Pre label={isError ? "Error" : "Output"} error={isError}>
              {output}
            </Pre>
          ) : null}
        </div>
      </CollapsibleContent>
    </Collapsible>
  );
}

export function Pre({ label, error, children }: { label: string; error?: boolean; children: string }) {
  return (
    <figure className="overflow-hidden rounded-lg border border-border bg-bg">
      <figcaption className="border-b border-hairline bg-panel px-2.5 py-0.5 text-2xs text-faint">{label}</figcaption>
      <pre
        className={cn(
          "max-h-64 overflow-auto px-2.5 py-2 font-mono text-xs leading-relaxed break-words whitespace-pre-wrap",
          error && "text-err",
        )}
      >
        {children}
      </pre>
    </figure>
  );
}
