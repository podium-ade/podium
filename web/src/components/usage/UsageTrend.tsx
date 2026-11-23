import type { UsageDay } from "../../gen/podium/agent/v1/agent_pb";
import {
  billed,
  byDay,
  dayKey,
  dayLabel,
  dayTotals,
  eachDay,
  spanDays,
  usd,
  ZERO,
  type Range,
  type Totals,
} from "../../lib/usage";
import { Card, CardContent, CardHeader, CardTitle } from "../ui/card";
import { Tooltip } from "../ui/tooltip";
import { dimmed } from "../../lib/useLagging";
import { cn } from "../../lib/utils";

/** The two series, one tone each. Billed is the solid accent; covered is the same hue, lighter. */
const BILLED = "bg-accent";
const COVERED = "bg-accent/35";

/**
 * UsageTrend is spend per day across the selected range, as bars stacked in two parts: what
 * was billed, and what a subscription covered at list price. They stack because together they
 * are the work done that day, and apart they are two different kinds of money.
 *
 * It draws a bar for every day in the range, including the ones nothing ran on: the server
 * sends only days that have rows, and a chart with the empty days omitted would compress the
 * gaps and read as continuous spending.
 *
 * The labels thin out as the range grows (every day at a week, then weekly, then monthly)
 * because the alternative at ninety days is ninety overlapping dates.
 */
export function UsageTrend({
  days,
  range,
  loading,
}: {
  days: UsageDay[];
  range: Range;
  /** loading fades the chart while the next answer is on its way. */
  loading?: boolean;
}) {
  const index = byDay(days);
  const cells = eachDay(range).map((d) => {
    const row = index.get(dayKey(d));
    return { day: d, row, t: row ? dayTotals(row) : ZERO };
  });
  const span = spanDays(range);
  const max = cells.reduce((m, c) => Math.max(m, c.t.cost), 0);
  const covered = cells.some((c) => c.t.subscription > 0);
  // One label every nth bar, so they never collide however wide the range is.
  const every = span <= 14 ? 1 : span <= 45 ? 7 : 30;

  return (
    <Card aria-label="Spend per day" role="region" className="flex flex-col">
      <CardHeader className="pb-1">
        <div>
          <CardTitle>Spend per day</CardTitle>
          <p className="text-2xs text-faint">
            {span} {span === 1 ? "day" : "days"}
            {max > 0 ? <> · busiest {usd(max)}</> : null}
          </p>
        </div>
        {/* Kept in place, hidden, when nothing was covered, so a filter that changes that
            never rewraps the header. */}
        <ul
          aria-hidden={!covered}
          aria-label={covered ? "Legend" : undefined}
          className={cn("flex items-center gap-3 text-2xs text-muted", !covered && "invisible")}
        >
          <li className="inline-flex items-center gap-1.5">
            <span aria-hidden className={`size-2.5 rounded-[1px] ${BILLED}`} />
            Billed
          </li>
          <li className="inline-flex items-center gap-1.5">
            <span aria-hidden className={`size-2.5 rounded-[1px] ${COVERED}`} />
            Covered by subscription
          </li>
        </ul>
      </CardHeader>

      {/* Beside the breakdown the bars take whatever height the row has; alone, 6rem. */}
      <CardContent className={cn("flex flex-1 flex-col", dimmed(loading))}>
        {max === 0 ? (
          // The chart's own height (bars, gap and day labels), so an empty range or filter
          // leaves the card the size it was.
          <p className="mt-3 flex min-h-[7.375rem] flex-1 items-center justify-center text-center text-xs text-muted">
            Nothing with a recorded cost ran in this range.
          </p>
        ) : (
          <>
            <div className="mt-3 flex min-h-24 flex-1 items-end gap-px" role="img" aria-label={summary(cells.map((c) => c.t), range)}>
              {cells.map(({ day, row, t }) => {
                const paid = billed(t);
                // A day that spent something never renders as nothing: a floor keeps a cheap
                // day distinguishable from a day with no turns at all.
                const total = t.cost > 0 ? Math.max((t.cost / max) * 100, 2) : 0;
                const paidShare = t.cost > 0 ? paid / t.cost : 0;
                return (
                  <Tooltip key={dayKey(day)} label={label(day, row ? t : undefined)}>
                    <div className="group flex h-full flex-1 cursor-default flex-col justify-end">
                      {t.cost > 0 ? (
                        <div
                          className="flex w-full flex-col-reverse overflow-hidden rounded-t-[2px] group-hover:opacity-85"
                          style={{ height: `${total}%` }}
                        >
                          <div className={BILLED} style={{ height: `${paidShare * 100}%` }} />
                          <div className={`${COVERED} flex-1`} />
                        </div>
                      ) : (
                        <div className="h-[2px] w-full bg-raised" />
                      )}
                    </div>
                  </Tooltip>
                );
              })}
            </div>
            {/* Labels are positioned on the bar they name rather than laid out in cells of the
                same width, so a label may be wider than its bar without being clipped. */}
            <div aria-hidden className="relative mt-1.5 h-4 text-2xs text-faint">
              {cells.map(({ day }, i) =>
                i % every === 0 ? (
                  <span
                    key={dayKey(day)}
                    className="absolute -translate-x-1/2 whitespace-nowrap"
                    style={{ left: `${((i + 0.5) / cells.length) * 100}%` }}
                  >
                    {dayLabel(day)}
                  </span>
                ) : null,
              )}
            </div>
          </>
        )}
      </CardContent>
    </Card>
  );
}

function label(d: Date, t: Totals | undefined): string {
  if (!t) return `${dayLabel(d)}: nothing ran`;
  const n = `${t.turns} ${t.turns === 1 ? "turn or task" : "turns and tasks"}`;
  const covered = t.subscription > 0 ? `, ${usd(t.subscription)} covered by subscription` : "";
  const unpriced = t.unpriced > 0 ? `, ${t.unpriced} unpriced` : "";
  return `${dayLabel(d)}: ${usd(billed(t))} billed${covered} over ${n}${unpriced}`;
}

/** The chart's one-sentence equivalent, for a reader who cannot see the bars. */
function summary(cells: Totals[], range: Range): string {
  const paid = cells.reduce((s, t) => s + billed(t), 0);
  const covered = cells.reduce((s, t) => s + t.subscription, 0);
  const sub = covered > 0 ? ` and ${usd(covered)} covered by subscription` : "";
  return `Spend per day over ${range.label}: ${usd(paid)} billed${sub} across ${cells.length} days.`;
}
