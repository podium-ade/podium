import { useState } from "react";
import type { TaskCost } from "../../gen/podium/agent/v1/agent_pb";
import { breakdown, ranBy, usd, type Slice } from "../../lib/usage";
import { cn } from "../../lib/utils";

type Dimension = { id: string; label: string; pick: (c: TaskCost) => string };

/**
 * The two questions worth asking of a bill: what spent it, and where the work came from.
 * Both are session fields the conductor already stores, so neither costs a query.
 *
 * "What" is the assistant or a playbook, which is why it is not called "by playbook": a
 * conversation is answered on the host and runs none.
 */
const DIMENSIONS: Dimension[] = [
  { id: "playbook", label: "By what ran it", pick: (c) => ranBy(c.playbook) },
  { id: "source", label: "By source", pick: (c) => c.sourceKind },
];

/**
 * UsageBreakdown splits the window's spend along one dimension at a time.
 *
 * It reads only the conductor's costs, so it accounts for agent spend and says so — a task
 * that no turn ran has nothing to attribute to a playbook, and inventing an "other" bar for it
 * would imply the money went somewhere.
 */
export function UsageBreakdown({ costs, loading }: { costs: TaskCost[]; loading?: boolean }) {
  const [dim, setDim] = useState(DIMENSIONS[0]);
  const slices = breakdown(costs, dim.pick);
  const total = slices.reduce((sum, s) => sum + s.cost, 0);

  return (
    <section aria-label="Where it went" className="flex flex-col rounded-xl border border-border bg-card p-4 shadow-xs">
      <header className="flex flex-wrap items-center justify-between gap-2">
        <h2 className="text-sm font-medium text-fg">Where it went</h2>
        <div className="inline-flex h-7 items-center gap-0.5 rounded-lg border border-border bg-panel p-0.5">
          {DIMENSIONS.map((d) => (
            <button
              key={d.id}
              type="button"
              onClick={() => setDim(d)}
              aria-pressed={d.id === dim.id}
              className={cn(
                "h-6 rounded-md px-2 text-2xs font-medium transition-colors",
                "outline-none focus-visible:ring-2 focus-visible:ring-ring/50",
                d.id === dim.id ? "bg-raised text-fg shadow-xs" : "text-muted hover:text-fg",
              )}
            >
              {d.label}
            </button>
          ))}
        </div>
      </header>

      <div className={cn("mt-4 flex-1 transition-opacity", loading && "opacity-50")}>
        {slices.length === 0 ? (
          <p className="py-8 text-center text-xs text-muted">
            No agent turns ran in this window, so there is nothing to attribute.
          </p>
        ) : (
          <ol className="space-y-2.5">
            {slices.slice(0, 8).map((s) => (
              <Bar key={s.key} slice={s} total={total} />
            ))}
          </ol>
        )}
        {slices.length > 8 ? (
          <p className="mt-3 text-2xs text-faint">
            and {slices.length - 8} more, each below {usd(slices[7].cost)}
          </p>
        ) : null}
      </div>
    </section>
  );
}

function Bar({ slice, total }: { slice: Slice; total: number }) {
  // Against a total of zero every share is undefined, not 100%: a window where nothing
  // reported a cost draws empty bars rather than full ones.
  const share = total > 0 ? slice.cost / total : 0;
  return (
    <li>
      <div className="flex items-baseline justify-between gap-3 text-xs">
        <span className="truncate font-mono text-fg" title={slice.key}>
          {slice.key}
        </span>
        <span className="shrink-0 tabular text-muted">
          {usd(slice.cost)}
          <span className="ml-1.5 text-faint">
            {slice.turns} {slice.turns === 1 ? "turn" : "turns"}
          </span>
        </span>
      </div>
      <div className="mt-1 h-1.5 overflow-hidden rounded-full bg-raised">
        <div
          className="h-full rounded-full bg-accent transition-[width] duration-300 ease-out"
          style={{ width: `${Math.max(share * 100, share > 0 ? 2 : 0)}%` }}
        />
      </div>
    </li>
  );
}
