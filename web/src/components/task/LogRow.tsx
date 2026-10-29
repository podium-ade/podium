import { memo, type ReactNode } from "react";
import { Box, Brain, MessageSquare, Server, Terminal, Wrench } from "lucide-react";
import type { Entry, Severity } from "../../lib/entries";
import { absolute } from "../../lib/format";
import { cn } from "../../lib/utils";

export const ROW_PX = 28;

const SEVERITY_BAR: Record<Severity, string> = {
  info: "border-l-transparent",
  warn: "border-l-warn",
  error: "border-l-err",
};

const SEVERITY_TEXT: Record<Severity, string> = {
  info: "text-fg",
  warn: "text-warn",
  error: "text-err",
};

/** SourceIcon groups sources by who is speaking, so the column scans as a handful of shapes. */
export function SourceIcon({ source }: { source: string }) {
  const props = { "aria-hidden": true, className: "size-3.5 shrink-0 text-faint" };
  if (source === "node") return <Server {...props} />;
  if (source === "stdout" || source === "stderr") return <Terminal {...props} />;
  if (source.startsWith("sidecar:")) return <Box {...props} />;
  if (source === "message") return <MessageSquare {...props} />;
  if (source === "agent") return <Brain {...props} />;
  return <Wrench {...props} />;
}

/** sourceLabel drops the family a source's icon already says. */
function sourceLabel(source: string): string {
  if (source.startsWith("tool:")) return source.slice(5);
  if (source.startsWith("sidecar:")) return source.slice(8);
  return source;
}

/** clock is a row's time to the millisecond, the milliseconds quieter than the rest. */
function Clock({ ms }: { ms: number }) {
  if (ms === 0) return null;
  const d = new Date(ms);
  const hms = d.toLocaleTimeString([], { hour12: false, hour: "2-digit", minute: "2-digit", second: "2-digit" });
  return (
    <span>
      {hms}
      <span className="text-faint/70">.{String(d.getMilliseconds()).padStart(3, "0")}</span>
    </span>
  );
}

/** Summary draws one line by what the entry is, so a run reads as more than a wall of text. */
function Summary({ entry }: { entry: Entry }) {
  const text = entry.summary === "" ? " " : entry.summary;
  if (entry.kind === "message" && entry.message) {
    const prefix = `${entry.message.type}: `;
    return (
      <>
        <Label>{entry.message.type || "message"}</Label>
        {text.startsWith(prefix) ? text.slice(prefix.length) : text}
      </>
    );
  }
  if (entry.kind === "reasoning") return <span className="text-muted italic">{text}</span>;
  if (entry.source === "node") {
    const [kind, ...rest] = text.split(" · ");
    return (
      <>
        <Label>{kind}</Label>
        {rest.join(" · ")}
      </>
    );
  }
  return <>{text}</>;
}

function Label({ children }: { children: ReactNode }) {
  return (
    <span className="mr-2 inline-block rounded-sm bg-raised px-1.5 font-sans text-[11px] leading-[18px] font-medium text-muted">
      {children}
    </span>
  );
}

/**
 * LogRow is one entry of the explorer: a line to scan. Selecting it opens its record in the
 * panel beside the list, so the list itself never moves.
 */
export const LogRow = memo(function LogRow({
  entry,
  selected,
  inBand,
  onSelect,
}: {
  entry: Entry;
  selected: boolean;
  /** inBand marks a line printed while the transcript step it was opened from ran. */
  inBand: boolean;
  onSelect: (key: string) => void;
}) {
  return (
    <div
      role="option"
      tabIndex={-1}
      aria-selected={selected}
      data-testid="log-line"
      data-key={entry.key}
      data-source={entry.source}
      data-severity={entry.severity}
      data-band={inBand || undefined}
      onClick={() => onSelect(entry.key)}
      style={{ height: ROW_PX }}
      className={cn(
        "group/row flex w-max min-w-full cursor-pointer items-center border-b border-l-2 border-b-hairline/50",
        SEVERITY_BAR[entry.severity],
        entry.severity === "error" && "bg-err-bg/40",
        "hover:bg-raised",
        inBand && "bg-run-bg",
        selected && "bg-accent/20 hover:bg-accent/25",
      )}
    >
      {/* The gutter is sticky so the time survives scrolling a wide log sideways, which
          is exactly when you need it to quote a line. */}
      <span
        className={cn(
          "tabular sticky left-0 z-10 flex h-full w-[7.75rem] shrink-0 items-center border-r border-hairline pr-3 pl-3 text-muted select-none",
          selected ? "bg-raised" : "bg-bg group-hover/row:bg-raised",
        )}
        title={absolute(entry.ts)}
      >
        <span className="sr-only">{entry.severity} </span>
        <Clock ms={entry.at} />
      </span>
      <span className="flex w-32 shrink-0 items-center gap-1.5 px-3 text-muted" title={entry.source}>
        <SourceIcon source={entry.source} />
        <span className="truncate font-sans text-xs">{sourceLabel(entry.source)}</span>
      </span>
      <span data-testid="log-text" className={cn("pr-4 whitespace-pre", SEVERITY_TEXT[entry.severity])}>
        <Summary entry={entry} />
      </span>
    </div>
  );
});
