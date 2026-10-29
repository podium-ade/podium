import { memo, useEffect, useLayoutEffect, useMemo, useRef, useState, type ReactNode } from "react";
import {
  Box,
  Braces,
  Brain,
  ChevronRight,
  Link2,
  ListRestart,
  MessageSquare,
  Minus,
  Plus,
  Server,
  Terminal,
  Wrench,
} from "lucide-react";
import { pretty } from "../../lib/activity";
import { entryJson, fieldsOf, linkTo, type Entry, type Severity } from "../../lib/entries";
import { absolute } from "../../lib/format";
import { cn } from "../../lib/utils";
import { Button } from "../ui/button";

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
function SourceIcon({ source }: { source: string }) {
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
 * LogRow is one entry of the explorer: a line to scan, and — opened — its whole record in
 * place, the way a log explorer opens a row, with every field one click from a filter.
 */
export const LogRow = memo(function LogRow({
  entry,
  open,
  current,
  inBand,
  bandSize,
  query,
  filtered,
  onToggle,
  onHeight,
  onTerm,
  onSurrounding,
  onClearBand,
}: {
  entry: Entry;
  open: boolean;
  /** current is the row a link points at. */
  current: boolean;
  /** inBand marks a line printed while the transcript step it was opened from ran. */
  inBand: boolean;
  /** bandSize is set on the step itself: how many lines are marked for it. */
  bandSize?: number;
  query: string;
  filtered: boolean;
  onToggle: (key: string) => void;
  onHeight: (key: string, px: number) => void;
  onTerm: (term: string) => void;
  onSurrounding: () => void;
  onClearBand: () => void;
}) {
  return (
    <div data-testid="log-entry" data-open={open || undefined}>
      <div
        role="button"
        tabIndex={0}
        aria-expanded={open}
        aria-current={current || undefined}
        data-testid="log-line"
        data-key={entry.key}
        data-source={entry.source}
        data-severity={entry.severity}
        data-band={inBand || undefined}
        onClick={() => onToggle(entry.key)}
        onKeyDown={(k) => {
          if (k.key === "Enter" || k.key === " ") {
            k.preventDefault();
            onToggle(entry.key);
          }
        }}
        style={{ height: ROW_PX }}
        className={cn(
          "group/row flex w-max min-w-full cursor-pointer items-center border-b border-l-2 border-b-hairline/50 outline-none",
          SEVERITY_BAR[entry.severity],
          entry.severity === "error" && "bg-err-bg/40",
          "hover:bg-raised focus-visible:bg-raised",
          inBand && "bg-run-bg",
          open && "bg-raised",
          current && "bg-accent/15 hover:bg-accent/20",
        )}
      >
        {/* The gutter is sticky so the time survives scrolling a wide log sideways, which
            is exactly when you need it to quote a line. */}
        <span
          className={cn(
            "tabular sticky left-0 z-10 flex h-full w-[8.75rem] shrink-0 items-center gap-1.5 border-r border-hairline pr-3 pl-2 text-muted select-none",
            current ? "bg-raised" : open ? "bg-raised" : "bg-bg group-hover/row:bg-raised",
          )}
          title={absolute(entry.ts)}
        >
          <ChevronRight
            aria-hidden
            className={cn("size-3.5 shrink-0 text-faint transition-transform", open && "rotate-90")}
          />
          <span className="sr-only">{entry.severity}</span>
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
      {open ? (
        <Measured id={entry.key} onHeight={onHeight}>
          <Detail
            entry={entry}
            query={query}
            filtered={filtered}
            bandSize={bandSize}
            onTerm={onTerm}
            onSurrounding={onSurrounding}
            onClearBand={onClearBand}
          />
        </Measured>
      ) : null}
    </div>
  );
});

/** Measured reports its height, so the virtualizer can place the rows that follow it. */
function Measured({ id, onHeight, children }: { id: string; onHeight: (key: string, px: number) => void; children: ReactNode }) {
  const ref = useRef<HTMLDivElement>(null);
  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    const ro = new ResizeObserver(([e]) => {
      const px = e.borderBoxSize?.[0]?.blockSize ?? el.offsetHeight;
      onHeight(id, Math.ceil(px));
    });
    ro.observe(el);
    return () => ro.disconnect();
  }, [id, onHeight]);
  return <div ref={ref}>{children}</div>;
}

function Detail({
  entry,
  query,
  filtered,
  bandSize,
  onTerm,
  onSurrounding,
  onClearBand,
}: {
  entry: Entry;
  query: string;
  filtered: boolean;
  bandSize?: number;
  onTerm: (term: string) => void;
  onSurrounding: () => void;
  onClearBand: () => void;
}) {
  const [copied, setCopied] = useState<string>();
  const [raw, setRaw] = useState(false);
  useEffect(() => {
    if (!copied) return;
    const id = setTimeout(() => setCopied(undefined), 1600);
    return () => clearTimeout(id);
  }, [copied]);
  const json = useMemo(() => JSON.stringify(entryJson(entry), null, 2), [entry]);
  const fields = useMemo(() => fieldsOf(entry), [entry]);
  const copy = (what: string, text: string) => {
    void navigator.clipboard?.writeText(text);
    setCopied(what);
  };

  return (
    <section
      aria-label="Entry details"
      data-testid="entry-detail"
      className="sticky left-0 border-b border-hairline bg-panel py-3 pr-4 pl-[calc(8.75rem+0.75rem)] font-sans"
    >
      <div className="mb-2.5 flex flex-wrap items-center gap-x-3 gap-y-1.5">
        <span className="tabular text-xs text-fg">{absolute(entry.ts)}</span>
        <span className="font-mono text-2xs text-faint">seq {String(entry.seq)}</span>
        {bandSize ? (
          <span className="inline-flex items-center gap-1.5 rounded-sm bg-run-bg px-2 py-0.5 text-2xs text-run">
            {`${bandSize} ${bandSize === 1 ? "line" : "lines"} above were printed while this ran`}
            <button type="button" onClick={onClearBand} className="underline-offset-2 hover:underline">
              clear
            </button>
          </span>
        ) : null}
        <div className="ml-auto flex items-center gap-0.5">
          {filtered ? (
            <Button variant="ghost" size="xs" onClick={onSurrounding}>
              <ListRestart />
              Show surrounding
            </Button>
          ) : null}
          <Button variant="ghost" size="xs" onClick={() => copy("link", linkTo(entry.key, query))}>
            <Link2 />
            {copied === "link" ? "Copied" : "Copy link"}
          </Button>
          <Button variant="ghost" size="xs" onClick={() => copy("json", json)}>
            <Braces />
            {copied === "json" ? "Copied" : "Copy JSON"}
          </Button>
        </div>
      </div>

      <Body entry={entry} />

      <dl className="mt-3 grid grid-cols-[max-content_minmax(0,1fr)] gap-x-4 gap-y-0.5 text-xs">
        {fields.map((f) => (
          <div key={f.path} className="group/field contents">
            <dt className="py-0.5 font-mono text-2xs leading-5 text-faint">{f.path}</dt>
            <dd className="flex min-w-0 items-start gap-1 py-0.5">
              <span className="min-w-0 font-mono text-xs leading-5 break-all text-fg">{f.value}</span>
              {f.term ? (
                <span className="flex shrink-0 gap-0.5 opacity-0 transition-opacity group-hover/field:opacity-100 focus-within:opacity-100">
                  <FieldAction label={`Show only entries with ${f.path} ${f.value}`} onClick={() => onTerm(f.term!)}>
                    <Plus />
                  </FieldAction>
                  <FieldAction label={`Hide entries with ${f.path} ${f.value}`} onClick={() => onTerm(`-${f.term!}`)}>
                    <Minus />
                  </FieldAction>
                </span>
              ) : null}
            </dd>
          </div>
        ))}
      </dl>

      <button
        type="button"
        onClick={() => setRaw((r) => !r)}
        className="mt-2.5 inline-flex items-center gap-1 text-2xs text-muted hover:text-fg"
      >
        <ChevronRight className={cn("size-3 transition-transform", raw && "rotate-90")} />
        Raw record
      </button>
      {raw ? (
        <pre className="mt-1.5 max-h-72 overflow-auto rounded-md border border-hairline bg-bg px-3 py-2 font-mono text-xs leading-relaxed">
          {json}
        </pre>
      ) : null}
    </section>
  );
}

function FieldAction({ label, onClick, children }: { label: string; onClick: () => void; children: ReactNode }) {
  return (
    <button
      type="button"
      aria-label={label}
      title={label}
      onClick={onClick}
      className="grid size-5 place-items-center rounded-sm text-faint hover:bg-raised hover:text-fg [&_svg]:size-3"
    >
      {children}
    </button>
  );
}

/** Body is what the entry said, in full: a tool call's input beside its output, a line unwrapped. */
function Body({ entry }: { entry: Entry }) {
  const a = entry.activity;
  if (a?.kind === "tool") {
    const failed = a.status === "error";
    const out = failed ? a.error : a.output;
    return (
      <div className="grid gap-2 lg:grid-cols-2">
        <Block label="Input">{a.input ? pretty(a.input) : "(none)"}</Block>
        <Block label={failed ? "Error" : "Output"} error={failed}>
          {out || "(no output)"}
        </Block>
      </div>
    );
  }
  if (a?.kind === "reasoning") return <Prose>{a.text}</Prose>;
  if (entry.message) return <Prose>{entry.message.text}</Prose>;
  if (entry.line) return <Block label={entry.source}>{entry.line.text}</Block>;
  return null;
}

function Block({ label, error, children }: { label: string; error?: boolean; children: string }) {
  return (
    <figure className="min-w-0 overflow-hidden rounded-md border border-hairline bg-bg">
      <figcaption className="border-b border-hairline px-3 py-1 text-2xs text-faint">{label}</figcaption>
      <pre
        className={cn(
          "max-h-72 overflow-auto px-3 py-2 font-mono text-xs leading-relaxed break-words whitespace-pre-wrap",
          error ? "text-err" : "text-fg",
        )}
      >
        {children}
      </pre>
    </figure>
  );
}

function Prose({ children }: { children: string }) {
  return (
    <p className="max-h-72 max-w-3xl overflow-auto text-sm leading-6 break-words whitespace-pre-wrap text-fg">{children}</p>
  );
}
