import { useMemo, useRef, useState, type PointerEvent } from "react";
import { BUCKETS, bucketize, type TimeRange } from "../../lib/buckets";
import type { Entry } from "../../lib/entries";
import { durationMs } from "../../lib/format";
import { cn } from "../../lib/utils";

const HEIGHT = 36;
/** A press that moves less than this is a click on one bucket, not a drag across several. */
const DRAG_PX = 4;

/** hms is the axis's clock, in the same 24-hour form as the rows beneath it. */
function hms(ms: number): string {
  return new Date(ms).toLocaleTimeString([], { hour12: false });
}

/**
 * EventHistogram is where in the run things happened: the entries that pass the filter,
 * counted over time, with warnings and errors stacked on top so a burst of stderr before
 * an exit is the first thing the eye lands on. Drag across it to keep one window of the
 * run; click a bar to keep that slice.
 *
 * Colour here is status, not series: the severity chips beside the search box are its
 * legend, and every bar names its counts on hover.
 */
export function EventHistogram({
  entries,
  span,
  range,
  onRange,
}: {
  entries: readonly Entry[];
  /** span is the whole run, so the axis does not move when a filter hides its ends. */
  span: TimeRange;
  range?: TimeRange;
  onRange: (r: TimeRange | undefined) => void;
}) {
  const buckets = useMemo(() => bucketize(entries, span.from, span.to), [entries, span.from, span.to]);
  const max = Math.max(1, ...buckets.map((b) => b.info + b.warn + b.error));
  const ref = useRef<SVGSVGElement>(null);
  const [drag, setDrag] = useState<{ x0: number; x1: number; t0: number; t1: number }>();
  const [hover, setHover] = useState<number>();

  const width = span.to - span.from;
  const toX = (t: number) => ((t - span.from) / width) * 100;
  const fromPx = (px: number) => {
    const box = ref.current?.getBoundingClientRect();
    if (!box || box.width === 0) return span.from;
    return span.from + (Math.min(Math.max(px - box.left, 0), box.width) / box.width) * width;
  };

  const down = (e: PointerEvent<SVGSVGElement>) => {
    e.currentTarget.setPointerCapture?.(e.pointerId);
    const t = fromPx(e.clientX);
    setDrag({ x0: e.clientX, x1: e.clientX, t0: t, t1: t });
  };
  const move = (e: PointerEvent<SVGSVGElement>) => {
    const box = ref.current?.getBoundingClientRect();
    if (box && box.width > 0) {
      setHover(Math.min(BUCKETS - 1, Math.max(0, Math.floor(((e.clientX - box.left) / box.width) * BUCKETS))));
    }
    if (drag) setDrag({ ...drag, x1: e.clientX, t1: fromPx(e.clientX) });
  };
  const up = () => {
    if (!drag) return;
    if (Math.abs(drag.x1 - drag.x0) < DRAG_PX) {
      const b = hover !== undefined ? buckets[hover] : undefined;
      if (b && b.info + b.warn + b.error > 0) onRange({ from: b.from, to: b.to });
    } else {
      onRange({ from: Math.min(drag.t0, drag.t1), to: Math.max(drag.t0, drag.t1) });
    }
    setDrag(undefined);
  };

  const selected = drag
    ? { from: Math.min(drag.t0, drag.t1), to: Math.max(drag.t0, drag.t1) }
    : range;
  const hovered = hover !== undefined ? buckets[hover] : undefined;
  const barW = 100 / BUCKETS;

  return (
    <div className="relative border-b border-hairline px-4 pt-2 pb-1.5" data-testid="event-histogram">
      <svg
        ref={ref}
        role="img"
        aria-label={`Entries over the run, ${durationMs(width)} wide. Drag to keep a window.`}
        viewBox={`0 0 100 ${HEIGHT}`}
        preserveAspectRatio="none"
        className="block h-9 w-full cursor-crosshair touch-none select-none"
        onPointerDown={down}
        onPointerMove={move}
        onPointerUp={up}
        onPointerLeave={() => setHover(undefined)}
      >
        {selected ? (
          <rect
            x={toX(selected.from)}
            y={0}
            width={Math.max(0.3, toX(selected.to) - toX(selected.from))}
            height={HEIGHT}
            className="fill-accent/20"
          />
        ) : null}
        {buckets.map((b, i) => {
          const total = b.info + b.warn + b.error;
          if (total === 0) return null;
          const scale = (HEIGHT - 2) / max;
          const x = i * barW + barW * 0.12;
          const w = barW * 0.76;
          let y = HEIGHT;
          return (
            <g key={i} className={cn(hover !== undefined && hover !== i && "opacity-60")}>
              {(["info", "warn", "error"] as const).map((s) => {
                const h = b[s] * scale;
                if (h === 0) return null;
                y -= h;
                return (
                  <rect
                    key={s}
                    x={x}
                    y={y}
                    width={w}
                    height={Math.max(h - 0.4, 0.6)}
                    className={s === "error" ? "fill-err" : s === "warn" ? "fill-warn" : "fill-idle/60"}
                  />
                );
              })}
            </g>
          );
        })}
      </svg>
      <div className="tabular mt-0.5 flex justify-between text-2xs text-faint">
        <span>{hms(span.from)}</span>
        {hovered && hovered.info + hovered.warn + hovered.error > 0 ? (
          <span className="text-muted" role="status">
            {hms(hovered.from)} · {hovered.info + hovered.warn + hovered.error} entries
            {hovered.warn ? ` · ${hovered.warn} warn` : ""}
            {hovered.error ? ` · ${hovered.error} error` : ""}
          </span>
        ) : null}
        <span>{hms(span.to)}</span>
      </div>
    </div>
  );
}
