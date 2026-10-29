import { useMemo, useRef, useState, type PointerEvent, type ReactNode } from "react";
import type { Entry } from "../../lib/entries";
import { durationMs } from "../../lib/format";
import {
  density,
  LANE_LABEL,
  LANES,
  laneOf,
  marksOf,
  phasesOf,
  ticks,
  type Lane,
  type Mark,
  type TimeRange,
} from "../../lib/timeline";
import { cn } from "../../lib/utils";

/** How many slices the output lane is drawn in. */
const BINS = 160;
/** A press that moves less than this is a click, not a drag across the run. */
const DRAG_PX = 4;

const PHASE_FILL: Record<string, string> = {
  provisioning: "bg-idle/30",
  pulling: "bg-idle/50",
  running: "bg-run/35",
};

function hms(ms: number): string {
  return new Date(ms).toLocaleTimeString([], { hour12: false });
}

function sourceLabel(source: string): string {
  if (source.startsWith("tool:")) return source.slice(5);
  return source;
}

/**
 * RunTimeline is where in the run things happened, one lane per actor: the node's stages,
 * what the agent said, the tools it called, and how much the container printed. Every
 * mark names itself on hover and opens in the panel on a click; dragging across the lanes
 * keeps one window of the run, the way the list below is filtered.
 *
 * Colour is status, not series: red is what failed, amber what warned, green the answer.
 * The lane names say which actor a mark is, so no colour has to.
 */
export function RunTimeline({
  all,
  shown,
  span,
  range,
  selected,
  onRange,
  onSelect,
}: {
  /** all is every entry of the run: the phases and the axis do not move with a filter. */
  all: readonly Entry[];
  /** shown is the entries the filter keeps; only they are drawn. */
  shown: readonly Entry[];
  span: TimeRange;
  range?: TimeRange;
  selected?: string;
  onRange: (r: TimeRange | undefined) => void;
  onSelect: (key: string) => void;
}) {
  const plot = useRef<HTMLDivElement>(null);
  const [drag, setDrag] = useState<{ x0: number; x1: number; t0: number; t1: number }>();
  const [hover, setHover] = useState<Mark>();

  const width = Math.max(1, span.to - span.from);
  const pct = (t: number) => ((t - span.from) / width) * 100;

  const marks = useMemo(() => {
    const keep = new Set(shown.map((e) => e.key));
    return marksOf(all).filter((m) => keep.has(m.entry.key));
  }, [all, shown]);
  const byLane = useMemo(() => {
    const out: Record<Lane, Mark[]> = { node: [], agent: [], tools: [], output: [] };
    for (const m of marks) out[m.lane].push(m);
    return out;
  }, [marks]);
  const bins = useMemo(() => density(shown, span, BINS), [shown, span]);
  const maxBin = Math.max(1, ...bins.map((b) => b.count));
  const phases = useMemo(() => phasesOf(all, span.to), [all, span.to]);
  const axis = useMemo(() => ticks(span), [span]);
  const exit = useMemo(() => {
    let e: Entry | undefined;
    for (let i = all.length - 1; i >= 0 && !e; i--) {
      if (all[i].kind === "exited" || all[i].kind === "finished") e = all[i];
    }
    return e ? { at: e.at, ok: e.severity !== "error", label: /exit -?\d+/.exec(e.summary)?.[0] ?? "exited" } : undefined;
  }, [all]);
  const current = selected ? marks.find((m) => m.entry.key === selected) : undefined;
  const currentAt = current?.to ?? (selected ? shown.find((e) => e.key === selected)?.at : undefined);

  // The lanes are the run's, not the filter's: narrowing the list empties a lane rather
  // than taking it away, so the timeline keeps its shape while the marks change.
  const lanes = useMemo(() => {
    const present = new Set<Lane>(all.filter((e) => e.at > 0).map(laneOf));
    return LANES.filter((l) => present.has(l));
  }, [all]);

  const timeAt = (clientX: number) => {
    const box = plot.current?.getBoundingClientRect();
    if (!box || box.width === 0) return span.from;
    return span.from + (Math.min(Math.max(clientX - box.left, 0), box.width) / box.width) * width;
  };
  const down = (e: PointerEvent<HTMLDivElement>) => {
    if ((e.target as HTMLElement).closest("[data-mark]")) return;
    e.currentTarget.setPointerCapture?.(e.pointerId);
    const t = timeAt(e.clientX);
    setDrag({ x0: e.clientX, x1: e.clientX, t0: t, t1: t });
  };
  const move = (e: PointerEvent<HTMLDivElement>) => {
    if (drag) setDrag({ ...drag, x1: e.clientX, t1: timeAt(e.clientX) });
  };
  const up = () => {
    if (drag && Math.abs(drag.x1 - drag.x0) >= DRAG_PX) {
      onRange({ from: Math.min(drag.t0, drag.t1), to: Math.max(drag.t0, drag.t1) });
    }
    setDrag(undefined);
  };
  const window_ = drag ? { from: Math.min(drag.t0, drag.t1), to: Math.max(drag.t0, drag.t1) } : range;

  return (
    <div data-testid="run-timeline" className="border-b border-hairline px-4 pt-2 pb-2.5 select-none">
      <div className="grid grid-cols-[3.5rem_minmax(0,1fr)] gap-x-3">
        {/* Axis */}
        <span />
        <div className="relative mb-1 h-4 text-2xs text-faint tabular">
          {axis.map((t, i) => (
            <span
              key={t.at}
              className={cn("absolute top-0", i === 0 ? "" : "-translate-x-1/2")}
              style={{ left: `${pct(t.at)}%` }}
            >
              {t.label}
            </span>
          ))}
          <span className="absolute top-0 right-0 bg-bg pl-1 text-faint" title="When the run ended">
            {hms(span.to)}
          </span>
        </div>

        {/* Lane names */}
        <div className="flex flex-col">
          <LaneName>phase</LaneName>
          {lanes.map((l) => (
            <LaneName key={l}>{LANE_LABEL[l]}</LaneName>
          ))}
        </div>

        {/* Plot */}
        <div
          ref={plot}
          role="img"
          aria-label={`The run over ${durationMs(width)}: ${marks.length} steps. Drag to keep a window.`}
          onPointerDown={down}
          onPointerMove={move}
          onPointerUp={up}
          onPointerCancel={() => setDrag(undefined)}
          onPointerLeave={() => setHover(undefined)}
          className="relative flex cursor-crosshair touch-none flex-col"
        >
          {/* Gridlines at each tick, behind everything. */}
          {axis.map((t) => (
            <span
              key={t.at}
              aria-hidden
              className="pointer-events-none absolute inset-y-0 w-px bg-hairline/60"
              style={{ left: `${pct(t.at)}%` }}
            />
          ))}

          <LaneRow>
            {phases.map((p) => (
              <span
                key={p.name}
                title={`${p.name} · ${durationMs(p.to - p.from)}`}
                className={cn(
                  "absolute top-1/2 h-3.5 -translate-y-1/2 overflow-hidden rounded-sm px-1.5 text-[10px] leading-[14px] whitespace-nowrap text-fg/80",
                  PHASE_FILL[p.name] ?? "bg-idle/30",
                )}
                style={{ left: `${pct(p.from)}%`, width: `max(2px, ${pct(p.to) - pct(p.from)}%)` }}
              >
                {pct(p.to) - pct(p.from) > 12 ? `${p.name} · ${durationMs(p.to - p.from)}` : ""}
              </span>
            ))}
            {exit ? (
              <span
                className={cn(
                  "absolute top-1/2 -translate-x-full -translate-y-1/2 rounded-sm px-1.5 text-[10px] leading-[14px] font-medium whitespace-nowrap",
                  exit.ok ? "bg-ok-bg text-ok" : "bg-err-bg text-err",
                )}
                style={{ left: `${Math.min(100, pct(exit.at))}%` }}
              >
                {exit.label}
              </span>
            ) : null}
          </LaneRow>

          {lanes.map((lane) => (
            <LaneRow key={lane}>
              {lane === "output"
                ? bins.map((b, i) =>
                    b.count === 0 ? null : (
                      <span
                        key={i}
                        aria-hidden
                        className="absolute bottom-1 flex flex-col justify-end"
                        style={{
                          left: `${(i / BINS) * 100}%`,
                          width: `${100 / BINS}%`,
                          height: `${4 + Math.sqrt(b.count / maxBin) * 16}px`,
                        }}
                        title={`${b.count} ${b.count === 1 ? "line" : "lines"}${b.warn ? `, ${b.warn} on stderr` : ""} · ${hms(b.from)}`}
                      >
                        <span className="block flex-1 rounded-[1px] bg-idle/60" />
                        {b.warn ? (
                          <span className="block rounded-[1px] bg-warn" style={{ height: `${(b.warn / b.count) * 100}%` }} />
                        ) : null}
                      </span>
                    ),
                  )
                : byLane[lane].map((m) => (
                    <MarkView
                      key={m.entry.key}
                      mark={m}
                      left={pct(m.from)}
                      width={pct(m.to) - pct(m.from)}
                      selected={m.entry.key === selected}
                      onHover={setHover}
                      onSelect={onSelect}
                    />
                  ))}
            </LaneRow>
          ))}

          {window_ ? (
            <span
              aria-hidden
              className="pointer-events-none absolute inset-y-0 border-x border-accent/60 bg-accent/15"
              style={{ left: `${pct(window_.from)}%`, width: `${Math.max(0.2, pct(window_.to) - pct(window_.from))}%` }}
            />
          ) : null}
          {currentAt ? (
            <span
              aria-hidden
              className="pointer-events-none absolute inset-y-0 w-px bg-accent"
              style={{ left: `${pct(currentAt)}%` }}
            />
          ) : null}

          {hover ? <Tip mark={hover} left={pct(hover.to)} /> : null}
        </div>
      </div>
    </div>
  );
}

function LaneName({ children }: { children: ReactNode }) {
  return <span className="h-[26px] text-[10px] leading-[26px] font-medium tracking-wide text-faint uppercase">{children}</span>;
}

function LaneRow({ children }: { children: ReactNode }) {
  return (
    <div className="relative h-[26px] border-b border-hairline/40 last:border-b-0">
      {children}
    </div>
  );
}

/**
 * MarkView is one entry on its lane. Its hit area is wider than the mark, so a point a
 * pixel wide can still be hovered and clicked.
 */
function MarkView({
  mark,
  left,
  width,
  selected,
  onHover,
  onSelect,
}: {
  mark: Mark;
  left: number;
  width: number;
  selected: boolean;
  onHover: (m: Mark | undefined) => void;
  onSelect: (key: string) => void;
}) {
  const bar = mark.lane === "tools";
  const fill =
    mark.tone === "error"
      ? "bg-err"
      : mark.tone === "warn"
        ? "bg-warn"
        : mark.tone === "answer"
          ? "bg-ok"
          : bar
            ? "bg-run/70"
            : "bg-muted";
  return (
    <span
      data-mark
      data-testid="timeline-mark"
      data-key={mark.entry.key}
      onPointerEnter={() => onHover(mark)}
      onPointerLeave={() => onHover(undefined)}
      onClick={() => onSelect(mark.entry.key)}
      className="absolute inset-y-0 flex cursor-pointer items-center justify-center"
      style={
        bar
          ? { left: `${left}%`, width: `max(6px, ${width}%)` }
          : { left: `calc(${left}% - 5px)`, width: 10 }
      }
    >
      <span
        className={cn(
          "block",
          fill,
          bar ? "h-3 w-full rounded-[2px] border-r border-bg" : mark.lane === "node" ? "size-2.5 rotate-45 rounded-[1px]" : "size-2.5 rounded-full",
          mark.tone === "answer" && !bar && "size-3 ring-2 ring-ok/30",
          mark.tone === "error" && !bar && "size-3",
          selected && "ring-2 ring-accent ring-offset-1 ring-offset-bg",
        )}
      />
    </span>
  );
}

/** Tip names the hovered mark: who, what, and when. */
function Tip({ mark, left }: { mark: Mark; left: number }) {
  const e = mark.entry;
  const took = mark.lane === "tools" && mark.to > mark.from ? ` · ≤${durationMs(mark.to - mark.from)}` : "";
  return (
    <div
      role="status"
      className="pointer-events-none absolute bottom-full z-20 mb-1 max-w-[28rem] rounded-md border border-border bg-panel px-2.5 py-1.5 text-xs shadow-md"
      style={left > 60 ? { right: `${100 - left}%` } : { left: `${left}%` }}
    >
      <span className="font-mono text-fg">{sourceLabel(e.source)}</span>
      <span className="text-faint">
        {" "}
        · {hms(e.at)}
        {took}
      </span>
      <p className="mt-0.5 truncate font-mono text-2xs text-muted">{e.summary}</p>
    </div>
  );
}
