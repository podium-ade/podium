import type { UsageDay } from "../../gen/podium/agent/v1/agent_pb";
import { byDay, dayKey, dayLabel, eachDay, spanDays, usd, type Range } from "../../lib/usage";
import { cn } from "../../lib/utils";
import { Tooltip } from "../ui/tooltip";

/**
 * UsageTrend is spend per day across the selected range, as bars.
 *
 * It draws a bar for every day in the range, including the ones nothing ran on: the server
 * sends only days that have rows, and a chart with the empty days omitted would compress the
 * gaps and read as continuous spending.
 *
 * The labels thin out as the range grows — every day at a week, then weekly, then monthly —
 * because the alternative at ninety days is ninety overlapping dates.
 */
export function UsageTrend({ days, range }: { days: UsageDay[]; range: Range }) {
  const index = byDay(days);
  const cells = eachDay(range);
  const span = spanDays(range);
  const max = cells.reduce((m, d) => Math.max(m, index.get(dayKey(d))?.costUsd ?? 0), 0);
  // One label every nth bar, so they never collide however wide the range is.
  const every = span <= 14 ? 1 : span <= 45 ? 7 : 30;

  return (
    <section aria-label="Spend per day" className="rounded-xl border border-border bg-card p-4 shadow-xs">
      <header className="flex flex-wrap items-baseline justify-between gap-2">
        <h2 className="text-sm font-medium text-fg">Spend per day</h2>
        <p className="text-2xs text-faint">
          {span} {span === 1 ? "day" : "days"}
          {max > 0 ? <> · busiest {usd(max)}</> : null}
        </p>
      </header>

      {max === 0 ? (
        <p className="py-10 text-center text-xs text-muted">
          Nothing with a recorded cost ran in this range.
        </p>
      ) : (
        <>
          <div className="mt-4 flex h-32 items-end gap-px" role="img" aria-label={summary(cells, index, range)}>
            {cells.map((d) => {
              const row = index.get(dayKey(d));
              const cost = row?.costUsd ?? 0;
              // A day that spent something never renders as nothing: a 1px floor keeps a
              // cheap day distinguishable from a day with no turns at all.
              const pct = cost > 0 ? Math.max((cost / max) * 100, 2) : 0;
              return (
                <Tooltip key={dayKey(d)} label={label(d, row)}>
                  <div className="group flex h-full flex-1 cursor-default flex-col justify-end">
                    <div
                      className={cn(
                        "w-full rounded-t-[2px] transition-colors",
                        cost > 0 ? "bg-accent/70 group-hover:bg-accent" : "bg-raised",
                      )}
                      style={{ height: cost > 0 ? `${pct}%` : "2px" }}
                    />
                  </div>
                </Tooltip>
              );
            })}
          </div>
          <div aria-hidden className="mt-1.5 flex gap-px text-2xs text-faint">
            {cells.map((d, i) => (
              <span key={dayKey(d)} className="flex-1 truncate text-center">
                {i % every === 0 ? dayLabel(d) : ""}
              </span>
            ))}
          </div>
        </>
      )}
    </section>
  );
}

function label(d: Date, row: UsageDay | undefined): string {
  if (!row) return `${dayLabel(d)} — nothing ran`;
  const turns = `${row.turns} ${row.turns === 1 ? "task" : "tasks"}`;
  const unpriced = row.unpriced > 0 ? `, ${row.unpriced} unpriced` : "";
  return `${dayLabel(d)} — ${usd(row.costUsd)} over ${turns}${unpriced}`;
}

/** The chart's one-sentence equivalent, for a reader who cannot see the bars. */
function summary(cells: Date[], index: Map<string, UsageDay>, range: Range): string {
  const total = cells.reduce((s, d) => s + (index.get(dayKey(d))?.costUsd ?? 0), 0);
  return `Spend per day over ${range.label}, ${usd(total)} in total across ${cells.length} days.`;
}
