import {
  useCallback,
  useEffect,
  useId,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type CSSProperties,
  type KeyboardEvent,
} from "react";
import { ArrowDownToLine, Braces, CircleAlert, ListRestart, Search, X } from "lucide-react";
import { entryJson, firstError, isTranscript, SEVERITIES, type Entry, type Severity } from "../../lib/entries";
import { parseQuery, tokenize } from "../../lib/entryQuery";
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
import type { TimeRange } from "../../lib/timeline";
import { RunTimeline } from "./RunTimeline";
import { EntryPanel } from "./EntryPanel";
import { LogRow, ROW_PX } from "./LogRow";
import { MIN_SIDE_PX, PanelResizer } from "./PanelResizer";

/** OVERSCAN_PX is how far past the viewport rows are rendered, either way. */
const OVERSCAN_PX = 360;
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

/** PANEL_WIDTH_KEY remembers, in this browser, how wide somebody dragged the entry panel. */
const PANEL_WIDTH_KEY = "podium.logs.panelWidth";

function readPanelWidth(): number | undefined {
  try {
    const v = Number(globalThis.localStorage?.getItem(PANEL_WIDTH_KEY));
    return Number.isFinite(v) && v >= MIN_SIDE_PX ? v : undefined;
  } catch {
    return undefined;
  }
}

function writePanelWidth(px: number | undefined) {
  try {
    if (px === undefined) globalThis.localStorage?.removeItem(PANEL_WIDTH_KEY);
    else globalThis.localStorage?.setItem(PANEL_WIDTH_KEY, String(px));
  } catch {
    // A browser that will not store it just forgets the width; the split still works.
  }
}

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
 * in the run they fell. The selected row's record opens in a panel beside the list, which
 * the arrow keys walk; the selected row is the one a link points at.
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
  // A chip narrows to its group: with none chosen in a row every entry shows, and each one
  // chosen adds its group to what does.
  const [onlySeverity, setOnlySeverity] = useState<ReadonlySet<string>>(new Set());
  const [onlySource, setOnlySource] = useState<ReadonlySet<string>>(new Set());
  const [range, setRange] = useState<TimeRange>();
  const [follow, setFollow] = useState(selected === undefined);
  const [scrollTop, setScrollTop] = useState(0);
  /** bandKey is the tool call whose lines are marked: set only when it was opened from the transcript. */
  const [bandKey, setBandKey] = useState<string>();
  const [viewport, setViewport] = useState(0);
  const ref = useRef<HTMLDivElement>(null);
  const split = useRef<HTMLDivElement>(null);
  const [panelWidth, setPanelWidth] = useState<number | undefined>(readPanelWidth);
  const resizePanel = useCallback((px: number | undefined) => {
    setPanelWidth(px);
    writePanelWidth(px);
  }, []);
  const uid = useId();

  const parsed = useMemo(() => parseQuery(query), [query]);
  const queried = useMemo(
    () => (parsed.empty ? entries : entries.filter(parsed.test)),
    [entries, parsed],
  );
  const faceted = useMemo(
    () =>
      queried.filter(
        (e) =>
          (onlySeverity.size === 0 || onlySeverity.has(e.severity)) &&
          (onlySource.size === 0 || onlySource.has(e.source)),
      ),
    [queried, onlySeverity, onlySource],
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

  const problem = useMemo(() => firstError(visible), [visible]);
  const filtered = !parsed.empty || onlySeverity.size > 0 || onlySource.size > 0 || range !== undefined;

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

  const toRow = useCallback((row: number) => {
    const el = ref.current;
    if (!el || row < 0) return;
    setFollow(false);
    const top = Math.max(0, (row - CONTEXT_ROWS) * ROW_PX);
    el.scrollTop = top;
    setScrollTop(top);
  }, []);

  /** reveal scrolls just enough to bring a row on screen, the way a list follows the keyboard. */
  const reveal = useCallback((row: number) => {
    const el = ref.current;
    if (!el || row < 0) return;
    setFollow(false);
    const h = el.clientHeight || FALLBACK_VIEWPORT_PX;
    let top = el.scrollTop;
    if (row * ROW_PX < top) top = row * ROW_PX;
    else if ((row + 1) * ROW_PX > top + h) top = (row + 1) * ROW_PX - h;
    el.scrollTop = top;
    setScrollTop(top);
  }, []);

  useEffect(() => {
    if (follow) toBottom();
  }, [follow, visible.length, toBottom]);

  // A selection made outside the list — a link, a transcript step — scrolls to its row once
  // the row exists. A click inside the list is already on screen, so it is left alone.
  const landed = useRef<string | undefined>(undefined);
  useEffect(() => {
    if (selected === undefined || landed.current === selected || selectedRow < 0) return;
    landed.current = selected;
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

  /** pick selects a row from inside the list, or lets go of it when it already was. */
  const pick = useCallback(
    (key: string) => {
      landed.current = key;
      setBandKey(undefined);
      onSelect(selected === key ? undefined : key);
    },
    [selected, onSelect],
  );

  /** step moves the selection by one visible row, the way arrow keys walk a list. */
  const step = (by: number) => {
    if (visible.length === 0) return;
    const at = selectedRow < 0 ? (by > 0 ? -1 : visible.length) : selectedRow;
    const row = Math.min(visible.length - 1, Math.max(0, at + by));
    landed.current = visible[row].key;
    setBandKey(undefined);
    onSelect(visible[row].key);
    reveal(row);
  };
  const onKeyDown = (k: KeyboardEvent) => {
    if (k.key === "ArrowDown" || k.key === "j") step(1);
    else if (k.key === "ArrowUp" || k.key === "k") step(-1);
    else if (k.key === "Escape" && selected !== undefined) onSelect(undefined);
    else return;
    k.preventDefault();
  };

  /**
   * toggleTerm narrows the query by one field of the open record, or takes the term out
   * again when it is already in. Showing and hiding the same value are opposites, so
   * choosing one replaces the other.
   */
  const toggleTerm = useCallback(
    (term: string) => {
      const opposite = term.startsWith("-") ? term.slice(1) : `-${term}`;
      const terms = tokenize(query);
      const next = terms.includes(term)
        ? terms.filter((t) => t !== term)
        : [...terms.filter((t) => t !== opposite), term];
      onQuery(next.join(" "));
    },
    [query, onQuery],
  );

  /** jumpTo selects an entry from outside the list — the timeline — and scrolls to its row. */
  const jumpTo = useCallback(
    (key: string) => {
      landed.current = undefined;
      setBandKey(undefined);
      onSelect(key);
    },
    [onSelect],
  );

  const toProblem = () => {
    if (!problem) return;
    landed.current = undefined;
    onSelect(problem.key);
  };

  /** showSurrounding drops every filter and keeps the open row, in the context it came from. */
  const showSurrounding = () => {
    onQuery("");
    setOnlySeverity(new Set());
    setOnlySource(new Set());
    setRange(undefined);
    landed.current = undefined;
  };

  const facet = (only: ReadonlySet<string>, set: (s: ReadonlySet<string>) => void) => (value: string) => {
    const next = new Set(only);
    if (next.has(value)) next.delete(value);
    else next.add(value);
    set(next);
  };
  const toggleSeverity = facet(onlySeverity, setOnlySeverity);
  const toggleSource = facet(onlySource, setOnlySource);

  const height = viewport > 0 ? viewport : FALLBACK_VIEWPORT_PX;
  const first = Math.max(0, Math.floor((scrollTop - OVERSCAN_PX) / ROW_PX));
  const last = Math.min(visible.length, Math.ceil((scrollTop + height + OVERSCAN_PX) / ROW_PX));
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
            <Button variant="ghost" size="xs" onClick={toProblem} className="text-err">
              <CircleAlert />
              First error
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
            chosen={onlySeverity.has(s)}
            narrowed={onlySeverity.size > 0}
            dot={SEVERITY_DOT[s]}
            onClick={() => toggleSeverity(s)}
          />
        ))}
        <span aria-hidden className="mx-1 h-4 w-px bg-hairline" />
        {sources.map((s) => (
          <FacetChip
            key={s}
            label={s}
            count={counts.source.get(s) ?? 0}
            chosen={onlySource.has(s)}
            narrowed={onlySource.size > 0}
            onClick={() => toggleSource(s)}
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
        {onlySeverity.size > 0 || onlySource.size > 0 ? (
          <button
            type="button"
            onClick={() => {
              setOnlySeverity(new Set());
              setOnlySource(new Set());
            }}
            className="ml-auto text-2xs text-muted underline-offset-2 hover:text-fg hover:underline"
          >
            Show all
          </button>
        ) : (
          <span className="ml-auto hidden text-2xs text-faint sm:inline">Click a chip to show only it</span>
        )}
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
        <RunTimeline
          all={entries}
          shown={faceted}
          span={span}
          range={range}
          selected={selected}
          onRange={(r) => {
            setRange(r);
            setFollow(false);
          }}
          onSelect={jumpTo}
        />
      ) : null}

      {selectedEntry && selectedRow < 0 ? (
        <div className="flex items-center gap-3 border-b border-hairline bg-run-bg px-4 py-1.5 text-xs text-run">
          The selected entry is hidden by the current filter.
          <Button variant="ghost" size="xs" onClick={showSurrounding}>
            <ListRestart />
            View in context
          </Button>
        </div>
      ) : null}

      {/* The list and the open record side by side: the list never moves, and the arrow
          keys walk it while the record follows. Narrower than that, the record sits below. */}
      <div
        ref={split}
        style={panelWidth !== undefined ? ({ "--panel-width": `${panelWidth}px` } as CSSProperties) : undefined}
        className={cn(
          "grid min-w-0",
          selectedEntry && "lg:h-[34rem] lg:grid-cols-[minmax(0,1fr)_0.5rem_var(--panel-width,minmax(20rem,32%))]",
        )}
      >
        <div
          ref={ref}
          onScroll={onScroll}
          onKeyDown={onKeyDown}
          tabIndex={0}
          data-testid="log-scroll"
          role="listbox"
          aria-label="Task output"
          className="h-[34rem] min-h-48 overflow-auto bg-bg font-mono text-[13px] leading-[28px] outline-none focus-visible:ring-2 focus-visible:ring-ring/50 focus-visible:ring-inset"
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
            <div style={{ height: visible.length * ROW_PX }}>
              <div style={{ transform: `translateY(${first * ROW_PX}px)` }}>
                {window_.map((e) => (
                  <LogRow
                    key={e.key}
                    entry={e}
                    selected={e.key === selected}
                    inBand={band !== undefined && e.index >= band[0] && e.index < band[1]}
                    onSelect={pick}
                  />
                ))}
              </div>
            </div>
          )}
        </div>
        {selectedEntry ? (
          <PanelResizer
            width={panelWidth}
            onWidth={resizePanel}
            onReset={() => resizePanel(undefined)}
            container={split}
          />
        ) : null}
        {selectedEntry ? (
          <EntryPanel
            entry={selectedEntry}
            previous={selectedIndex > 0 ? entries[selectedIndex - 1] : undefined}
            query={query}
            filtered={filtered}
            bandSize={band !== undefined && bandIndex === selectedIndex ? bandSize : undefined}
            onTerm={toggleTerm}
            onSurrounding={showSurrounding}
            onClearBand={() => setBandKey(undefined)}
            onClose={() => onSelect(undefined)}
          />
        ) : null}
      </div>
    </div>
  );
}

/**
 * FacetChip is one group of a facet. Chosen, it is part of what the list shows; while
 * another chip of its row is chosen and it is not, it recedes, so the row reads as which
 * groups are in.
 */
function FacetChip({
  label,
  count,
  chosen,
  narrowed,
  dot,
  onClick,
}: {
  label: string;
  count: number;
  chosen: boolean;
  /** narrowed says some chip of this row is chosen. */
  narrowed: boolean;
  dot?: string;
  onClick: () => void;
}) {
  return (
    <button
      type="button"
      aria-pressed={chosen}
      aria-label={`${label} ${count}`}
      onClick={onClick}
      className={cn(
        "inline-flex items-center gap-1.5 rounded-md border px-2 py-0.5 font-mono text-2xs transition-colors",
        chosen
          ? "border-accent bg-accent/20 text-fg hover:bg-accent/25"
          : narrowed
            ? "border-border text-faint hover:bg-raised hover:text-muted"
            : "border-border bg-panel text-fg hover:bg-raised",
      )}
    >
      {dot ? <span aria-hidden className={cn("size-1.5 rounded-[1px]", dot)} /> : null}
      {label}
      <span className="tabular text-faint">{count}</span>
    </button>
  );
}
