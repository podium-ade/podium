import { useState, type ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { ChevronLeft, ChevronRight, Coins, Cpu, LayoutDashboard, TrendingDown, TrendingUp } from "lucide-react";
import { NavLink, Route, Routes } from "react-router";
import { ConductorDown } from "../components/agent/ConductorDown";
import { Chip } from "../components/Badge";
import { Empty } from "../components/Empty";
import { PageHeader } from "../components/PageHeader";
import { Skeleton, TableSkeleton } from "../components/Skeleton";
import { BackendTable } from "../components/usage/BackendTable";
import { RangePicker } from "../components/usage/RangePicker";
import { TaskCostTable } from "../components/usage/TaskCostTable";
import { UsageBreakdown } from "../components/usage/UsageBreakdown";
import { UsageTrend } from "../components/usage/UsageTrend";
import { Alert } from "../components/ui/alert";
import { Button } from "../components/ui/button";
import { agent, errorMessage, isAgentUnreachable, tasks } from "../lib/client";
import { useViewer } from "../lib/identity";
import {
  backendSummary,
  byTask,
  previousRange,
  rangeFor,
  spanDays,
  sumIn,
  totalIn,
  tzOffsetMinutes,
  usdShort,
  type Range,
} from "../lib/usage";
import { cn } from "../lib/utils";

/** How many turn-cost rows to ask the conductor for. Newest first, so a range busier than
 *  this loses its oldest rows — which are the comparison window's, not the selected one's. */
const COST_LIMIT = 1000;

/** One page of the table. Tasks are paged by the control plane's own id cursor. */
const PAGE_SIZE = 50;

/**
 * The two views. Each is a real route, so the back button and a deep link both work — the
 * same reason the agent screens are routed rather than held in state.
 */
const TABS = [
  { to: "/usage", end: true, label: "Overview", icon: LayoutDashboard, path: "overview" },
  { to: "/usage/models", end: false, label: "By model", icon: Cpu, path: "models" },
];

function tabLink({ isActive }: { isActive: boolean }) {
  return cn(
    "inline-flex h-8 items-center gap-1.5 rounded-t-md border-b-2 px-3 text-xs font-medium",
    "transition-colors duration-150 outline-none focus-visible:ring-2 focus-visible:ring-ring/50",
    "[&_svg]:size-3.5 [&_svg]:shrink-0",
    isActive
      ? "border-accent text-fg"
      : "border-transparent text-muted hover:text-fg",
  );
}

/**
 * UsagePage is what everything ran and what it cost, over a range the operator picks.
 *
 * It asks two databases and joins them in the browser. The control plane owns the tasks and
 * their compute; the conductor owns what each turn spent, keyed by the task it ran as. That
 * split is deliberate — the conductor holds no Podium table — so a task with no agent turn
 * behind it has no cost at all rather than a cost of zero.
 */
export function UsagePage() {
  const viewer = useViewer();
  const [range, setRange] = useState<Range>(() => rangeFor("7d"));
  const [cursors, setCursors] = useState<string[]>([""]);
  const cursor = cursors[cursors.length - 1];

  const tz = tzOffsetMinutes();
  // The comparison window sits immediately before the selected one, so a single read covers
  // both and the headline figure has something honest to be measured against.
  const previous = previousRange(range);

  // Both reads are gated on there being a conductor, not just the screen below them: the
  // hooks run before the early return, so without this a control plane with no conductor
  // still fires a GetUsage at a proxy that can only answer Unavailable. Undefined is "not
  // asked yet" and stays enabled, so the first paint is not a wasted round trip either.
  const enabled = viewer?.agentEnabled !== false;

  const usage = useQuery({
    queryKey: ["usage", range.from.toISOString(), range.to.toISOString(), tz],
    queryFn: () =>
      agent.getUsage({
        // The range is the range. compare_from reaches further back for the day rows alone,
        // so the by-model grouping is not quietly totalling twice the window on screen.
        from: timestampFromDate(range.from),
        to: timestampFromDate(range.to),
        compareFrom: timestampFromDate(previous.from),
        tzOffsetMinutes: tz,
        limit: COST_LIMIT,
      }),
    enabled,
    placeholderData: (prev) => prev,
  });

  const list = useQuery({
    queryKey: ["usage-tasks", range.from.toISOString(), range.to.toISOString(), cursor],
    queryFn: () =>
      tasks.listTasks({
        filter: {
          createdAfter: timestampFromDate(range.from),
          createdBefore: timestampFromDate(range.to),
        },
        page: { limit: PAGE_SIZE, cursor },
      }),
    enabled,
    placeholderData: (prev) => prev,
  });

  const pick = (r: Range) => {
    setRange(r);
    setCursors([""]);
  };

  const days = usage.data?.days ?? [];
  // The costs the table joins against and the breakdown splits: only the selected range's,
  // never the comparison window's, which is read for its total alone.
  const windowCosts = (usage.data?.costs ?? []).filter((c) => {
    const at = c.startedAt ? Number(c.startedAt.seconds) * 1000 : 0;
    return at >= range.from.getTime() && at < range.to.getTime();
  });
  const costs = byTask(windowCosts);

  const spent = totalIn(days, range);
  const before = totalIn(days, previous);
  const turns = sumIn(days, range, (d) => d.turns);
  const modelTurns = sumIn(days, range, (d) => d.modelTurns);
  const unpriced = sumIn(days, range, (d) => d.unpriced);

  const rows = list.data?.tasks ?? [];
  const next = list.data?.nextCursor ?? "";
  const truncated = (usage.data?.costs.length ?? 0) >= COST_LIMIT;
  const down = isAgentUnreachable(usage.error);

  // Without a conductor there is no cost data anywhere on this control plane, so the screen
  // would be a table of dashes. The nav item that leads here is hidden in the same case.
  if (viewer && !viewer.agentEnabled) {
    return (
      <div className="space-y-5">
        <PageHeader title="Usage" description="What every task cost and what it used." />
        <Empty
          icon={Coins}
          title="There is no conductor on this control plane"
          hint="Model cost is recorded per agent turn by podium-agent. Set PODIUM_AGENT_URL and PODIUM_AGENT_TOKEN on podium-server to see spend here. See docs/agent.md."
        />
      </div>
    );
  }

  const taskSection = (
    <section aria-label="Tasks" className="space-y-3">
      <div className="flex flex-wrap items-baseline justify-between gap-2">
        <h2 className="text-sm font-medium text-fg">
          Tasks <span className="font-normal text-muted">· {range.label}</span>
        </h2>
        <p className="text-2xs text-faint">
          Newest first. Cost comes from the conductor and is joined by task id, so a task no
          agent turn ran shows a dash rather than a zero.
        </p>
      </div>

      {truncated ? (
        <Alert variant="warn" title="Some older costs are not shown">
          More than {COST_LIMIT.toLocaleString()} turns ran in this range. The newest are
          joined into the table; older rows may show a dash where a cost exists. The totals
          above are unaffected — they are summed by the server.
        </Alert>
      ) : null}

      {list.isPending ? (
        <TableSkeleton cols={9} />
      ) : list.isError ? (
        <Alert variant="destructive" title="Could not list tasks">
          {errorMessage(list.error)}
        </Alert>
      ) : rows.length === 0 ? (
        <Empty
          icon={Coins}
          title={`Nothing ran in ${range.label.toLowerCase()}`}
          hint="Pick a wider range. A task appears here as soon as the control plane accepts it, whether or not it cost anything."
        />
      ) : (
        <>
          <TaskCostTable tasks={rows} costs={costs} />
          <div className="flex items-center justify-between gap-3">
            <p className="text-2xs text-faint">
              {rows.length} {rows.length === 1 ? "task" : "tasks"} on this page
            </p>
            <div className="flex gap-2">
              <Button
                variant="outline"
                size="sm"
                disabled={cursors.length === 1}
                onClick={() => setCursors((c) => c.slice(0, -1))}
              >
                <ChevronLeft />
                Previous
              </Button>
              <Button
                variant="outline"
                size="sm"
                disabled={next === ""}
                onClick={() => setCursors((c) => [...c, next])}
              >
                Next
                <ChevronRight />
              </Button>
            </div>
          </div>
        </>
      )}
    </section>
  );

  const overview = (
    <div className="space-y-5">
      <div className="grid gap-4 lg:grid-cols-[minmax(0,1.4fr)_minmax(0,1fr)]">
        <UsageTrend days={days} range={range} />
        <UsageBreakdown costs={windowCosts} loading={usage.isFetching && usage.isPlaceholderData} />
      </div>
      {taskSection}
    </div>
  );

  const models = (
    <section aria-label="By model" className="space-y-3">
      <div className="flex flex-wrap items-baseline justify-between gap-2">
        <h2 className="text-sm font-medium text-fg">
          By model <span className="font-normal text-muted">· {range.label}</span>
        </h2>
        <p className="text-2xs text-faint">{backendSummary(usage.data?.backends ?? [])}</p>
      </div>
      {usage.isPending ? (
        <TableSkeleton cols={9} />
      ) : (
        <BackendTable backends={usage.data?.backends ?? []} />
      )}
      <p className="max-w-3xl text-2xs leading-relaxed text-faint">
        Recorded as each turn and each delegated task starts, never inferred from a playbook:
        the chat can override the model for a single message, and editing a playbook would
        otherwise relabel everything that ever ran under it. Rows from before the conductor
        recorded this group as <span className="text-muted">unrecorded</span> — their cost is
        real, only the attribution is missing.
      </p>
    </section>
  );

  return (
    <div className="space-y-5">
      <PageHeader
        title="Usage"
        description="What ran, what it cost and what it used — spend per task from the conductor, compute from the control plane."
        meta={
          <>
            <Chip>{range.label}</Chip>
            {unpriced > 0 ? (
              <Chip className="border-warn/35 bg-warn/12 text-warn">
                {unpriced} {unpriced === 1 ? "turn" : "turns"} reported no cost
              </Chip>
            ) : null}
          </>
        }
        actions={<RangePicker range={range} onChange={pick} />}
      />

      {down ? (
        <ConductorDown
          what="The spend for this range could not be read"
          onRetry={() => void usage.refetch()}
          retrying={usage.isFetching}
        />
      ) : null}
      {usage.isError && !down ? (
        <Alert variant="destructive" title="Could not read usage">
          {errorMessage(usage.error)}
        </Alert>
      ) : null}

      <nav aria-label="Usage views" className="flex items-center gap-0.5 border-b border-border pb-px">
        {TABS.map((t) => (
          <NavLink key={t.path} to={t.to} end={t.end} className={tabLink}>
            <t.icon />
            {t.label}
          </NavLink>
        ))}
      </nav>

      <div role="group" aria-label="Totals" className="grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
        <Stat
          label="Spent"
          value={usdShort(spent)}
          loading={usage.isPending}
          delta={<Delta now={spent} before={before} label={comparisonLabel(range)} />}
        />
        <Stat
          label="Turns and tasks"
          value={turns.toLocaleString()}
          loading={usage.isPending}
          hint={unpriced > 0 ? `${unpriced} reported no cost` : "every one reported a cost"}
        />
        <Stat
          label="Model turns"
          value={modelTurns.toLocaleString()}
          loading={usage.isPending}
          hint="what the runtimes did inside those tasks"
        />
        <Stat
          label="Average per task"
          value={turns > 0 ? usdShort(spent / turns) : "—"}
          loading={usage.isPending}
          hint={turns > 0 ? `over ${turns.toLocaleString()} tasks` : "nothing ran yet"}
        />
      </div>

      <Routes>
        <Route index element={overview} />
        <Route path="models" element={models} />
        <Route path="*" element={overview} />
      </Routes>
    </div>
  );
}

/** What the delta is measured against, in words the tile can end a sentence with. */
function comparisonLabel(r: Range): string {
  if (r.id === "month") return "vs last month";
  if (r.id === "last-month") return "vs the month before";
  const n = spanDays(r);
  return n === 1 ? "vs yesterday" : `vs the previous ${n} days`;
}

function Stat({
  label,
  value,
  hint,
  delta,
  loading,
}: {
  label: string;
  value: string;
  hint?: string;
  delta?: ReactNode;
  loading?: boolean;
}) {
  return (
    <div className="rounded-xl border border-border bg-card p-4 shadow-xs">
      <p className="truncate text-2xs font-medium tracking-wide text-faint uppercase">{label}</p>
      {loading ? (
        <Skeleton className="mt-2 h-7 w-24" />
      ) : (
        <p className="mt-1.5 text-2xl leading-none font-semibold tabular text-fg">{value}</p>
      )}
      <div className="mt-2 text-2xs text-muted">{delta ?? hint}</div>
    </div>
  );
}

/**
 * Delta compares this range against the one before it. It says nothing at all when there is
 * nothing to compare against — "+100%" against zero is not a fact about spending.
 */
function Delta({ now, before, label }: { now: number; before: number; label: string }) {
  if (before <= 0) {
    return <span className="text-faint">nothing spent {label.replace(/^vs /, "")}</span>;
  }
  const change = (now - before) / before;
  const up = change >= 0;
  const Icon = up ? TrendingUp : TrendingDown;
  return (
    <span className={cn("inline-flex items-center gap-1", up ? "text-warn" : "text-ok")}>
      <Icon aria-hidden className="size-3" />
      {up ? "+" : ""}
      {(change * 100).toFixed(0)}%<span className="text-faint">{label}</span>
    </span>
  );
}
