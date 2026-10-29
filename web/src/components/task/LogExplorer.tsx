import { useCallback, useEffect, useId, useLayoutEffect, useMemo, useRef, useState, type MouseEvent } from "react";
import { ArrowDownToLine, Braces, CircleAlert, ListRestart, Search, X } from "lucide-react";
import { entryJson, firstProblem, isTranscript, SEVERITIES, type Entry, type Severity } from "../../lib/entries";
import { parseQuery } from "../../lib/entryQuery";
import { toRawText } from "../../lib/logs";
import { cn } from "../../lib/utils";
import type { StreamPhase } from "../../hooks/useTaskEvents";
import { Badge, type Tone } from "../Badge";
import { Alert } from "../ui/alert";
import { Button } from "../ui/button";
import { Input } from "../ui/input";
import { Label } from "../ui/label";
import { Switch } from "../ui/switch";
import { Tooltip } from "../ui/tooltip";
import type { TimeRange } from "../../lib/buckets";
import { EventHistogram } from "./EventHistogram";
import { LogRow, ROW_PX } from "./LogRow";

/** OVERSCAN_PX is how far past the viewport rows are rendered, either way. */
const OVERSCAN_PX = 360;
/** An opened row is drawn before it can be measured; this is its height until it is. */
const DETAIL_GUESS_PX = 240;
/** jsdom and a not-yet-laid-out container both report clientHeight 0; render a screenful anyway. */
const FALLBACK_VIEWPORT_PX = 600;
const AT_BOTTOM_SLACK_PX = 24;
/** How far above a row a jump lands, so the lines that led to it are on screen too. */
const CONTEXT_ROWS = 3;

const PHASE_TEXT: Record<StreamPhase, string> = {
  connecting: "connecting…",
  streaming: "live",
  finished: "stream ended",
  error: "reconnecting…",
};

const PHASE_TONE: Record<StreamPhase, Tone> = {
  connecting: "idle",
  streaming: "run",
  finished: "idle",
  error: "err",
};

const SEVERITY_DOT: Record<Severity, string> = {
  info: "bg-idle/50",
  warn: "bg-warn",
  error: "bg-err",
};

/** Sources sort the way a run happens: the node, the container's streams, then what it said. */
function sourceRank(s: string): number {
  if (s === "node") return 0;
  if (s === "stdout") return 1;
  if (s === "stderr") return 2;
  if (s.startsWith("sidecar:")) return 3;
  if (s === "message") return 4;
  if (s === "agent") return 5;
  return 6;
}

/** clock is a row's time to the millisecond: entries of one chunk sit within it. */
function clock(ms: number): string {
  if (ms === 0) return "";
  const d = new Date(ms);
  const hms = d.toLocaleTimeString([], { hour12: false, hour: "2-digit", minute: "2-digit", second: "2-digit" });
  return `${hms}.${String(d.getMilliseconds()).padStart(3, "0")}`;
}

/**
 * bandOf is the stretch of the stream a tool call covers: everything after the transcript
 * step before it, up to the call itself. A tool call is reported when it finishes, so the
 * log lines in that stretch are what the container printed while it ran.
 */
function bandOf(entries: readonly Entry[], at: number): [number, number] | undefined {
  const e = entries[at];
  if (!e || e.kind !== "tool") return undefined;
  let start = at - 1;
  while (start >= 0 && !isTranscript(entries[start])) start--;
  return [start + 1, at];
}

/**
 * LogExplorer is every entry of the run, the way a log explorer shows them: a time, a
 * severity, a source and a line, filtered by facets and a query, over a histogram of where
 * in the run they fell. A row opens in place to its whole record, and any number can be
 * open at once; the last one opened is the one a link points at.
 */
export function LogExplorer({
  entries,
  phase,
  error,
  taskId,
  query,
  onQuery,
  selected,
  onSelect,
  trace = false,
}: {
  entries: readonly Entry[];
  phase: StreamPhase;
  /** The last stream failure. Shown only while the phase says it is still reconnecting. */
  error?: string;
  taskId: string;
  query: string;
  onQuery: (q: string) => void;
  /** selected is the key of the open row. */
  selected?: string;
  onSelect: (key: string | undefined) => void;
  /** trace says the selection came from a transcript step: mark what it printed. */
  trace?: boolean;
}) {
  const [hiddenSeverity, setHiddenSeverity] = useState<ReadonlySet<string>>(new Set());
  const [hiddenSource, setHiddenSource] = useState<ReadonlySet<string>>(new Set());
  const [range, setRange] = useState<TimeRange>();
  const [follow, setFollow] = useState(selected === undefined);
  const [scrollTop, setScrollTop] = useState(0);
  const [open, setOpen] = useState<ReadonlySet<string>>(() => new Set(selected ? [selected] : []));
  const [heights, setHeights] = useState<Readonly<Record<string, number>>>({});
  /** bandKey is the tool call whose lines are marked: set only when it was opened from the transcript. */
  const [bandKey, setBandKey] = useState<string>();
  const [viewport, setViewport] = useState(0);
  const ref = useRef<HTMLDivElement>(null);
  const uid = useId();

  const parsed = useMemo(() => parseQuery(query), [query]);
  const queried = useMemo(
    () => (parsed.empty ? entries : entries.filter(parsed.test)),
    [entries, parsed],
  );
  const faceted = useMemo(
    () => queried.filter((e) => !hiddenSeverity.has(e.severity) && !hiddenSource.has(e.source)),
    [queried, hiddenSeverity, hiddenSource],
  );
  const visible = useMemo(
    () => (range ? faceted.filter((e) => e.at >= range.from && e.at <= range.to) : faceted),
    [faceted, range],
  );

  const counts = useMemo(() => {
    const severity = new Map<string, number>();
    const source = new Map<string, number>();
    for (const e of queried) {
      severity.set(e.severity, (severity.get(e.severity) ?? 0) + 1);
      source.set(e.source, (source.get(e.source) ?? 0) + 1);
    }
    return { severity, source };
  }, [queried]);
  // Every source the run has produced keeps its chip, so hiding one never makes it vanish.
  const sources = useMemo(() => {
    const seen = new Set<string>();
    for (const e of entries) seen.add(e.source);
    return [...seen].sort((a, b) => sourceRank(a) - sourceRank(b) || a.localeCompare(b));
  }, [entries]);

  const span = useMemo(() => {
    let from = 0;
    let to = 0;
    for (const e of entries) {
      if (e.at === 0) continue;
      if (from === 0 || e.at < from) from = e.at;
      if (e.at > to) to = e.at;
    }
    return to > from ? { from, to } : undefined;
  }, [entries]);

  const selectedIndex = useMemo(
    () => (selected === undefined ? -1 : entries.findIndex((e) => e.key === selected)),
    [entries, selected],
  );
  const selectedEntry = selectedIndex >= 0 ? entries[selectedIndex] : undefined;
  const bandIndex = useMemo(
    () => (bandKey === undefined ? -1 : entries.findIndex((e) => e.key === bandKey)),
    [entries, bandKey],
  );
  const selectedRow = useMemo(
    () => (selected === undefined ? -1 : visible.findIndex((e) => e.key === selected)),
    [visible, selected],
  );
  const band = useMemo(() => bandOf(entries, bandIndex), [entries, bandIndex]);
  const bandSize = useMemo(() => {
    if (!band) return undefined;
    let n = 0;
    for (let i = band[0]; i < band[1]; i++) if (entries[i].line) n++;
    return n;
  }, [entries, band]);

  // offsets[i] is the top of row i; an open row is as tall as its row and its record.
  const offsets = useMemo(() => {
    const out = new Float64Array(visible.length + 1);
    for (let i = 0; i < visible.length; i++) {
      const key = visible[i].key;
      out[i + 1] = out[i] + ROW_PX + (open.has(key) ? (heights[key] ?? DETAIL_GUESS_PX) : 0);
    }
    return out;
  }, [visible, open, heights]);
  const problem = useMemo(() => firstProblem(visible), [visible]);
  const filtered = !parsed.empty || hiddenSeverity.size > 0 || hiddenSource.size > 0 || range !== undefined;

  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    const measure = () => setViewport(el.clientHeight);
    measure();
    window.addEventListener("resize", measure);
    return () => window.removeEventListener("resize", measure);
  }, [visible.length]);

  const toBottom = useCallback(() => {
    const el = ref.current;
    if (el) el.scrollTop = el.scrollHeight;
  }, []);

  const toRow = useCallback(
    (row: number) => {
      const el = ref.current;
      if (!el || row < 0) return;
      setFollow(false);
      const top = offsets[Math.max(0, row - CONTEXT_ROWS)];
      el.scrollTop = top;
      setScrollTop(top);
    },
    [offsets],
  );

  useEffect(() => {
    if (follow) toBottom();
  }, [follow, visible.length, toBottom]);

  // A selection made outside the list — a link, a transcript step — scrolls to its row once
  // the row exists. A click inside the list is already on screen, so it is left alone.
  const landed = useRef<string | undefined>(undefined);
  useEffect(() => {
    if (selected === undefined || landed.current === selected || selectedRow < 0) return;
    landed.current = selected;
    setOpen((prev) => (prev.has(selected) ? prev : new Set(prev).add(selected)));
    setBandKey(trace && selectedEntry?.kind === "tool" ? selected : undefined);
    toRow(selectedRow);
  }, [selected, selectedRow, selectedEntry, trace, toRow]);

  const onScroll = useCallback(() => {
    const el = ref.current;
    if (!el) return;
    setScrollTop(el.scrollTop);
    setViewport(el.clientHeight);
    setFollow(el.scrollHeight - el.scrollTop - el.clientHeight <= AT_BOTTOM_SLACK_PX);
  }, []);

  /** toggle opens a row in place, or closes it; the link follows the last row opened. */
  const toggle = useCallback(
    (key: string) => {
      landed.current = key;
      setBandKey(undefined);
      const next = new Set(open);
      if (next.has(key)) {
        next.delete(key);
        if (key === selected) onSelect(undefined);
      } else {
        next.add(key);
        onSelect(key);
      }
      setOpen(next);
    },
    [open, selected, onSelect],
  );

  const onHeight = useCallback((key: string, px: number) => {
    setHeights((prev) => (prev[key] === px ? prev : { ...prev, [key]: px }));
  }, []);

  /** addTerm narrows the query by one field of an opened row. */
  const addTerm = useCallback(
    (term: string) => {
      const terms = query.trim();
      if (` ${terms} `.includes(` ${term} `)) return;
      onQuery(terms ? `${terms} ${term}` : term);
    },
    [query, onQuery],
  );

  const toProblem = () => {
    if (!problem) return;
    landed.current = undefined;
    onSelect(problem.key);
  };

  /** showSurrounding drops every filter and keeps the open row, in the context it came from. */
  const showSurrounding = () => {
    onQuery("");
    setHiddenSeverity(new Set());
    setHiddenSource(new Set());
    setRange(undefined);
    landed.current = undefined;
  };

  const facet = (
    hidden: ReadonlySet<string>,
    set: (s: ReadonlySet<string>) => void,
    all: readonly string[],
  ) => (value: string, e: MouseEvent) => {
    // Alt-click shows only this one, or everything again when it already was the only one.
    if (e.altKey || e.metaKey) {
      const only = all.every((v) => v === value || hidden.has(v)) && !hidden.has(value);
      set(only ? new Set() : new Set(all.filter((v) => v !== value)));
      return;
    }
    const next = new Set(hidden);
    if (next.has(value)) next.delete(value);
    else next.add(value);
    set(next);
  };
  const toggleSeverity = facet(hiddenSeverity, setHiddenSeverity, SEVERITIES);
  const toggleSource = facet(hiddenSource, setHiddenSource, sources);

  const height = viewport > 0 ? viewport : FALLBACK_VIEWPORT_PX;
  const first = Math.max(0, rowAt(offsets, scrollTop - OVERSCAN_PX));
  const last = Math.min(visible.length, rowAt(offsets, scrollTop + height + OVERSCAN_PX) + 1);
  const window_ = visible.slice(first, last);

  const save = (body: string, type: string, name: string) => {
    const url = URL.createObjectURL(new Blob([body], { type }));
    const a = document.createElement("a");
    a.href = url;
    a.download = name;
    a.click();
    URL.revokeObjectURL(url);
  };
  const downloadRaw = () =>
    save(toRawText(visible.flatMap((e) => (e.line ? [e.line] : []))), "text/plain", `${taskId}.log`);
  const downloadJsonl = () =>
    save(
      visible.map((e) => JSON.stringify(entryJson(e))).join("\n") + (visible.length ? "\n" : ""),
      "application/x-ndjson",
      `${taskId}.jsonl`,
    );

  return (
    <div className="flex min-w-0 flex-col">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-2 border-b border-hairline px-4 py-2.5">
        <Badge tone={PHASE_TONE[phase]}>{PHASE_TEXT[phase]}</Badge>

        <div className="relative min-w-0 flex-1 basis-72">
          <Search
            aria-hidden
            className="pointer-events-none absolute top-1/2 left-2.5 size-3.5 -translate-y-1/2 text-faint"
          />
          <Input
            type="search"
            aria-label="Search logs"
            aria-describedby={`${uid}-syntax`}
            placeholder='text  "a phrase"  /re.gex/  source:tool  severity>=warn  -noise'
            value={query}
            onChange={(e) => onQuery(e.target.value)}
            aria-invalid={parsed.error ? true : undefined}
            className="h-7 w-full pr-28 pl-7.5 font-mono text-xs"
          />
          <span className="tabular pointer-events-none absolute top-1/2 right-2.5 -translate-y-1/2 text-2xs text-faint">
            {visible.length} / {entries.length} entries
          </span>
        </div>

        <div className="ml-auto flex items-center gap-2">
          {problem ? (
            <Button
              variant="ghost"
              size="xs"
              onClick={toProblem}
              className={problem.severity === "error" ? "text-err" : "text-warn"}
            >
              <CircleAlert />
              {problem.severity === "error" ? "First error" : "First warning"}
            </Button>
          ) : null}
          <Label htmlFor={`${uid}-follow`} className="text-2xs">
            Follow
          </Label>
          <Switch
            id={`${uid}-follow`}
            checked={follow}
            onCheckedChange={(on) => {
              setFollow(on);
              if (on) toBottom();
            }}
          />
          <Tooltip label="Download the log lines on screen as text">
            <Button variant="outline" size="icon-xs" aria-label="Download raw log" onClick={downloadRaw}>
              <ArrowDownToLine />
            </Button>
          </Tooltip>
          <Tooltip label="Download the entries on screen as JSON lines, for jq">
            <Button variant="outline" size="icon-xs" aria-label="Download JSON lines" onClick={downloadJsonl}>
              <Braces />
            </Button>
          </Tooltip>
        </div>
      </div>

      <p id={`${uid}-syntax`} className="sr-only">
        Terms are combined. Quote a phrase, wrap a regular expression in slashes, filter with
        source:, kind: or severity, and put a minus before a term to exclude it.
      </p>

      <div className="flex flex-wrap items-center gap-1.5 border-b border-hairline px-4 py-2">
        {SEVERITIES.map((s) => (
          <FacetChip
            key={s}
            label={s}
            count={counts.severity.get(s) ?? 0}
            shown={!hiddenSeverity.has(s)}
            dot={SEVERITY_DOT[s]}
            onClick={(e) => toggleSeverity(s, e)}
          />
        ))}
        <span aria-hidden className="mx-1 h-4 w-px bg-hairline" />
        {sources.map((s) => (
          <FacetChip
            key={s}
            label={s}
            count={counts.source.get(s) ?? 0}
            shown={!hiddenSource.has(s)}
            onClick={(e) => toggleSource(s, e)}
          />
        ))}
        {range ? (
          <button
            type="button"
            onClick={() => setRange(undefined)}
            className="ml-1 inline-flex items-center gap-1 rounded-md border border-accent/40 bg-accent/10 px-2 py-0.5 font-mono text-2xs text-fg hover:bg-accent/20"
          >
            {clock(range.from)} – {clock(range.to)}
            <X aria-label="Clear the time window" className="size-3" />
          </button>
        ) : null}
        <span className="ml-auto hidden text-2xs text-faint sm:inline">Alt-click a chip to show only it</span>
      </div>

      {parsed.error ? (
        <Alert variant="warn" className="rounded-none border-x-0 border-t-0">
          {parsed.error}. The rest of the query still applies.
        </Alert>
      ) : null}
      {phase === "error" && error ? (
        <Alert variant="warn" className="rounded-none border-x-0 border-t-0">
          Lost the log stream: {error}. Reconnecting. Nothing already delivered is lost.
        </Alert>
      ) : null}

      {span ? (
        <EventHistogram
          entries={faceted}
          span={span}
          range={range}
          onRange={(r) => {
            setRange(r);
            setFollow(false);
          }}
        />
      ) : null}

      {selectedEntry && selectedRow < 0 ? (
        <div className="flex items-center gap-3 border-b border-hairline bg-run-bg px-4 py-1.5 text-xs text-run">
          The linked entry is hidden by the current filter.
          <Button variant="ghost" size="xs" onClick={showSurrounding}>
            <ListRestart />
            Show surrounding
          </Button>
        </div>
      ) : null}

      <div
        ref={ref}
        onScroll={onScroll}
        data-testid="log-scroll"
        role="log"
        aria-label="Task output"
        className="h-[34rem] min-h-48 overflow-auto bg-bg font-mono text-[13px] leading-[28px]"
      >
        {visible.length === 0 ? (
          <div className="flex min-h-48 flex-col items-center justify-center gap-1 px-4 text-center font-sans">
            <p className="text-xs text-muted">
              {entries.length === 0 ? "no output yet" : "nothing matches the filter"}
            </p>
            <p className="text-2xs text-faint">
              {entries.length === 0
                ? "Everything the run reports, and anything the container writes, appears here as it happens."
                : "Clear the search, turn a chip back on, or clear the time window."}
            </p>
          </div>
        ) : (
          <div style={{ height: offsets[visible.length] }}>
            <div style={{ transform: `translateY(${offsets[first]}px)` }}>
              {window_.map((e) => (
                <LogRow
                  key={e.key}
                  entry={e}
                  open={open.has(e.key)}
                  current={e.key === selected}
                  inBand={band !== undefined && e.index >= band[0] && e.index < band[1]}
                  bandSize={band !== undefined && e.index === band[1] ? bandSize : undefined}
                  query={query}
                  filtered={filtered}
                  onToggle={toggle}
                  onHeight={onHeight}
                  onTerm={addTerm}
                  onSurrounding={showSurrounding}
                  onClearBand={() => setBandKey(undefined)}
                />
              ))}
            </div>
          </div>
        )}
      </div>
    </div>
  );
}

/** rowAt is the row that covers y: the last whose top is at or above it. */
function rowAt(offsets: Float64Array, y: number): number {
  let lo = 0;
  let hi = offsets.length - 2;
  if (hi < 0 || y <= 0) return 0;
  while (lo < hi) {
    const mid = (lo + hi + 1) >> 1;
    if (offsets[mid] <= y) lo = mid;
    else hi = mid - 1;
  }
  return lo;
}

function FacetChip({
  label,
  count,
  shown,
  dot,
  onClick,
}: {
  label: string;
  count: number;
  shown: boolean;
  dot?: string;
  onClick: (e: MouseEvent) => void;
}) {
  return (
    <button
      type="button"
      aria-pressed={shown}
      aria-label={`${label} ${count}`}
      onClick={onClick}
      className={cn(
        "inline-flex items-center gap-1.5 rounded-md border px-2 py-0.5 font-mono text-2xs transition-colors",
        shown ? "border-border bg-panel text-fg hover:bg-raised" : "border-dashed border-border text-faint line-through hover:text-muted",
      )}
    >
      {dot ? <span aria-hidden className={cn("size-1.5 rounded-[1px]", dot)} /> : null}
      {label}
      <span className="tabular text-faint">{count}</span>
    </button>
  );
}
