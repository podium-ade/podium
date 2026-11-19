import { useState, type ReactNode } from "react";
import { keepPreviousData, useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { Coins, Cpu, CreditCard, LayoutDashboard, TrendingDown, TrendingUp, Users, Waypoints } from "lucide-react";
import { Link, Route, Routes, useLocation, useNavigate, useSearchParams } from "react-router";
import type { GetUsageResponse } from "../gen/podium/agent/v1/agent_pb";
import { ConductorDown } from "../components/agent/ConductorDown";
import { Empty } from "../components/Empty";
import { PageFrame } from "../components/PageHeader";
import { Skeleton, TableSkeleton } from "../components/Skeleton";
import { BackendTable } from "../components/usage/BackendTable";
import { PeopleTable } from "../components/usage/PeopleTable";
import { RangePicker } from "../components/usage/RangePicker";
import { ConversationTable, CredentialTable, RoutineTable, SourceTable } from "../components/usage/SourceTables";
import { SpendTable } from "../components/usage/SpendTable";
import { UsageBreakdown } from "../components/usage/UsageBreakdown";
import { UsageFilters } from "../components/usage/UsageFilters";
import { UsageTrend } from "../components/usage/UsageTrend";
import { Alert } from "../components/ui/alert";
import { Button } from "../components/ui/button";
import { Card } from "../components/ui/card";
import { Tabs, TabsList, TabsTrigger } from "../components/ui/tabs";
import { agent, errorMessage, isAgentUnreachable } from "../lib/client";
import { useViewer } from "../lib/identity";
import {
  backendSummary,
  billed,
  FILTER_PARAMS,
  isFiltered,
  previousRange,
  rangeFor,
  readFilters,
  routineNames,
  spanDays,
  tokens,
  totals,
  totalsIn,
  toUsageFilter,
  tzOffsetMinutes,
  usdShort,
  ZERO,
  type Filters,
  type Range,
  type Totals,
} from "../lib/usage";
import { cn } from "../lib/utils";

/** One page of the spend table. The conductor pages it, so a busy range is never cut short. */
const PAGE_SIZE = 50;

/**
 * The views. Each is a real route, so the back button and a deep link both work, and the
 * filters ride along in the query string from one to the next.
 */
const TABS = [
  { path: "", label: "Overview", icon: LayoutDashboard },
  { path: "models", label: "By model", icon: Cpu },
  { path: "people", label: "By person", icon: Users },
  { path: "sources", label: "By source", icon: Waypoints },
  { path: "credentials", label: "By credential", icon: CreditCard },
];

/** tabOf is the view a /usage path names, so the tab strip can say which one is open. */
function tabOf(pathname: string): string {
  const rest = pathname.replace(/^\/usage\/?/, "").split("/")[0];
  return TABS.some((t) => t.path === rest) ? rest : "";
}

/**
 * UsagePage is what the agents spent and what paid for it, over a range the operator picks.
 *
 * Everything on it is the conductor's: the totals, the daily trend, every grouping and the
 * table of individual turns, delegated tasks and housekeeping, all read through one filter so
 * that narrowing to a person or a model narrows the whole screen at once.
 *
 * Money is split in two throughout. Billed is what API keys were charged. Covered is what a
 * subscription paid for, at the harness's list price: real work, and worth seeing, but not
 * money that left the company, so it is never folded into the billed figure.
 */
export function UsagePage() {
  const viewer = useViewer();
  const location = useLocation();
  const navigate = useNavigate();
  const [params, setParams] = useSearchParams();
  const [range, setRange] = useState<Range>(() => rangeFor("7d"));
  const filters = readFilters(params);

  const tz = tzOffsetMinutes();
  // The comparison window sits immediately before the selected one, so a single read covers
  // both and the headline figure has something honest to be measured against.
  const previous = previousRange(range);

  // Both reads are gated on there being a conductor: the hooks run before the early return,
  // so without this a control plane with no conductor still asks a proxy that can only
  // answer Unavailable. Undefined is "not asked yet" and stays enabled.
  const enabled = viewer?.agentEnabled !== false;
  const filter = toUsageFilter(filters);

  const usage = useQuery({
    queryKey: ["usage", range.from.toISOString(), range.to.toISOString(), tz, filters],
    queryFn: () =>
      agent.getUsage({
        from: timestampFromDate(range.from),
        to: timestampFromDate(range.to),
        compareFrom: timestampFromDate(previous.from),
        tzOffsetMinutes: tz,
        // The table is its own paged read; this one is only for the aggregates.
        limit: 1,
        filter,
      }),
    enabled,
    placeholderData: keepPreviousData,
  });

  const rows = useInfiniteQuery({
    queryKey: ["usage-rows", range.from.toISOString(), range.to.toISOString(), filters],
    queryFn: ({ pageParam }) =>
      agent.listUsageRows({
        from: timestampFromDate(range.from),
        to: timestampFromDate(range.to),
        filter,
        cursor: pageParam,
        limit: PAGE_SIZE,
      }),
    initialPageParam: "",
    getNextPageParam: (last) => last.nextCursor || undefined,
    // A new range or filter keeps the rows already shown, dimmed, until its own arrive.
    placeholderData: keepPreviousData,
    enabled,
  });

  const setFilters = (f: Filters) =>
    setParams(
      (p) => {
        for (const [k, name] of Object.entries(FILTER_PARAMS) as [keyof Filters, string][]) {
          if (f[k]) p.set(name, f[k]);
          else p.delete(name);
        }
        return p;
      },
      { replace: true },
    );
  // A row in a grouping narrows the screen to it and goes back to the table, which is where
  // the question "which turns were these?" is answered.
  const drill = (f: Partial<Filters>) => {
    const next = new URLSearchParams(params);
    for (const [k, v] of Object.entries(f) as [keyof Filters, string][]) next.set(FILTER_PARAMS[k], v);
    void navigate({ pathname: "/usage", search: `?${next.toString()}` });
  };

  const u = usage.data;
  const days = u?.days ?? [];
  const now = u ? responseTotals(u) : ZERO;
  const before = totalsIn(days, previous);
  const routines = routineNames(u?.routines ?? []);
  const list = rows.data?.pages.flatMap((p) => p.rows) ?? [];
  const down = isAgentUnreachable(usage.error);
  const tab = tabOf(location.pathname);

  // Without a conductor there is no cost data anywhere on this control plane, so the screen
  // would be a page of dashes. The nav item that leads here is hidden in the same case.
  if (viewer && !viewer.agentEnabled) {
    return (
      <PageFrame title="Usage">
        <Empty
          icon={Coins}
          title="There is no conductor on this control plane"
          hint="Model cost is recorded per agent turn by podium-agent. Set PODIUM_AGENT_URL and PODIUM_AGENT_TOKEN on podium-server to see spend here. See docs/agent.md."
        />
      </PageFrame>
    );
  }

  const loading = usage.isPending;
  const refreshing = usage.isFetching && usage.isPlaceholderData;

  const table = (
    <section aria-label="Spend" className="space-y-3">
      <div className="flex flex-wrap items-baseline justify-between gap-2">
        <h2 className="text-sm font-semibold text-fg">
          Every turn and task <span className="font-normal text-muted">· {range.label}</span>
        </h2>
        <p className="text-2xs text-faint">
          Newest first. A row opens its task, or its conversation when it ran on the chat host.
        </p>
      </div>
      {rows.isPending ? (
        <TableSkeleton cols={9} />
      ) : rows.isError ? (
        isAgentUnreachable(rows.error) ? null : (
          <Alert variant="destructive" title="Could not list spend">
            {errorMessage(rows.error)}
          </Alert>
        )
      ) : list.length === 0 ? (
        <Empty
          icon={Coins}
          title={isFiltered(filters) ? "Nothing matches these filters" : `Nothing ran in ${range.label.toLowerCase()}`}
          hint={isFiltered(filters) ? "Clear a filter to see more." : "Pick a wider range."}
        />
      ) : (
        <>
          <SpendTable rows={list} routines={routines} busy={rows.isFetching && rows.isPlaceholderData} />
          <div className="flex flex-col items-center gap-2">
            {rows.hasNextPage ? (
              <Button
                variant="outline"
                size="sm"
                disabled={rows.isFetchingNextPage}
                onClick={() => void rows.fetchNextPage()}
              >
                {rows.isFetchingNextPage ? "Loading…" : "Show more"}
              </Button>
            ) : null}
            <p className="text-2xs text-faint">
              {list.length.toLocaleString()} of {now.turns.toLocaleString()} shown
            </p>
          </div>
        </>
      )}
    </section>
  );

  const overview = (
    <div className="space-y-5">
      <div className="grid gap-4 lg:grid-cols-[minmax(0,1.4fr)_minmax(0,1fr)]">
        <UsageTrend days={days} range={range} />
        <UsageBreakdown usage={u} loading={refreshing} />
      </div>
      {table}
    </div>
  );

  const view = (title: string, note: ReactNode, body: ReactNode, aside?: ReactNode) => (
    <section aria-label={title} className={cn("space-y-3 transition-opacity", refreshing && "opacity-60")}>
      <div className="flex flex-wrap items-baseline justify-between gap-2">
        <h2 className="text-sm font-semibold text-fg">
          {title} <span className="font-normal text-muted">· {range.label}</span>
        </h2>
        {aside ? <p className="text-2xs text-faint">{aside}</p> : null}
      </div>
      {loading ? <TableSkeleton cols={8} /> : body}
      <p className="max-w-3xl text-2xs leading-relaxed text-faint">{note}</p>
    </section>
  );

  const models = view(
    "By model",
    <>
      Recorded as each turn and each delegated task starts, never inferred from a playbook: the
      chat can override the model for a single message, and editing a playbook would otherwise
      relabel everything that ever ran under it. Rows from before the conductor recorded this
      group as <span className="text-muted">unrecorded</span>. Their cost is real, only the
      attribution is missing. Select a model to see its turns.
    </>,
    <BackendTable backends={u?.backends ?? []} onPick={(model) => drill({ model })} />,
    backendSummary(u?.backends ?? []),
  );

  const people = view(
    "By person",
    <>
      Each turn is counted against the person who started it, wherever they started it: in a
      shared chat or a Slack thread several people talk in, every turn runs as its sender, not as
      the chat&apos;s owner. A delegated task counts against the person whose turn delegated it.
      A system routine counts as itself. Reviews for people outside Podium run as the GitHub App.
    </>,
    <PeopleTable people={u?.people ?? []} routines={routines} onPick={(person) => drill({ person })} />,
  );

  const sources = (
    <div className="space-y-8">
      {view(
        "By source",
        <>
          Where the work came from. A routine&apos;s run counts as the routine, whichever
          conversation it ran in. Housekeeping is the conductor&apos;s own: the rolling summaries
          that keep a long conversation within the model&apos;s context.
        </>,
        <SourceTable sources={u?.sources ?? []} onPick={(origin) => drill({ origin })} />,
      )}
      {view(
        "Routines",
        <>Each routine&apos;s runs, with the tasks those runs delegated.</>,
        <RoutineTable routines={u?.routines ?? []} />,
      )}
      {view(
        "Most expensive conversations",
        <>The fifty conversations that cost the most in this range. Select one to open it.</>,
        <ConversationTable conversations={u?.conversations ?? []} />,
      )}
    </div>
  );

  const credentials = view(
    "By credential",
    <>
      What paid for each turn, recorded as it starts: a subscription sign-in or an API key, and
      whether it was the company&apos;s or a person&apos;s own. A shared conversation always
      spends the company&apos;s. A subscription&apos;s cost is the harness&apos;s list-price
      figure for the same work; it is shown as covered and kept out of billed spend. Rows from
      before the conductor recorded this are <span className="text-muted">unrecorded</span>.
    </>,
    <CredentialTable credentials={u?.credentials ?? []} onPick={(credential) => drill({ credential })} />,
  );

  return (
    <PageFrame
      title="Usage"
      lead="What your assistants have spent, what paid for it, and who and what it was for."
      actions={<RangePicker range={range} onChange={setRange} />}
    >
      <Tabs value={tab} onValueChange={(v) => void navigate({ pathname: v ? `/usage/${v}` : "/usage", search: location.search })}>
        <TabsList aria-label="Usage views">
          {TABS.map((t) => (
            <TabsTrigger key={t.path} value={t.path} asChild>
              <Link to={{ pathname: t.path ? `/usage/${t.path}` : "/usage", search: location.search }}>
                <t.icon aria-hidden />
                {t.label}
              </Link>
            </TabsTrigger>
          ))}
        </TabsList>
      </Tabs>

      <UsageFilters usage={u} filters={filters} onChange={setFilters} />

      {down ? (
        <ConductorDown
          what="The spend for this range could not be read"
          onRetry={() => {
            void usage.refetch();
            void rows.refetch();
          }}
          retrying={usage.isFetching}
        />
      ) : null}
      {usage.isError && !down ? (
        <Alert variant="destructive" title="Could not read usage">
          {errorMessage(usage.error)}
        </Alert>
      ) : null}

      <Card role="group" aria-label="Totals" className="grid grid-cols-2 xl:grid-cols-4">
        <Stat
          label="Billed"
          value={usdShort(billed(now))}
          loading={loading}
          detail={<Delta now={billed(now)} before={billed(before)} label={comparisonLabel(range)} />}
        />
        <Stat
          label="Covered by subscription"
          value={usdShort(now.subscription)}
          loading={loading}
          tone={now.subscription > 0 ? "text-fg" : "text-muted"}
          detail={
            now.subscription > 0
              ? `list price, ${Math.round((now.subscription / Math.max(now.cost, 1e-9)) * 100)}% of the work`
              : "no subscription turns"
          }
        />
        <Stat
          label="Tokens in / out"
          value={now.input + now.output > 0 ? `${tokens(now.input)} / ${tokens(now.output)}` : "-"}
          loading={loading}
          detail={
            now.cacheRead > 0
              ? `${tokens(now.cacheRead)} read from cache`
              : now.input + now.output > 0
                ? "no cache reads"
                : "none reported in this range"
          }
        />
        <Stat
          label="Turns and tasks"
          value={now.turns.toLocaleString()}
          loading={loading}
          detail={
            now.unpriced > 0
              ? `${now.unpriced.toLocaleString()} reported no cost`
              : `${now.modelTurns.toLocaleString()} model steps`
          }
        />
      </Card>

      <Routes>
        <Route index element={overview} />
        <Route path="models" element={models} />
        <Route path="people" element={people} />
        <Route path="sources" element={sources} />
        <Route path="credentials" element={credentials} />
        <Route path="*" element={overview} />
      </Routes>
    </PageFrame>
  );
}

/**
 * responseTotals is the range's totals. A conductor from before UsageTotals sends only the
 * older total fields, which still say what was spent, with no subscription share or tokens.
 */
function responseTotals(u: GetUsageResponse): Totals {
  if (u.totals) return totals(u.totals);
  return { ...ZERO, cost: u.totalCostUsd, turns: u.totalTurns, modelTurns: u.totalModelTurns, unpriced: u.unpriced };
}

/** What the delta is measured against, in words the tile can end a sentence with. */
function comparisonLabel(r: Range): string {
  if (r.id === "month") return "vs last month";
  if (r.id === "last-month") return "vs the month before";
  const n = spanDays(r);
  return n === 1 ? "vs yesterday" : `vs the previous ${n} days`;
}

/** Stat is one cell of the totals strip: a label, a figure, and one line under it. */
function Stat({
  label,
  value,
  detail,
  tone = "text-fg",
  loading,
}: {
  label: string;
  value: string;
  detail: ReactNode;
  tone?: string;
  loading?: boolean;
}) {
  return (
    <div className="min-w-0 space-y-1 border-hairline px-5 py-4 even:border-l [&:nth-child(n+3)]:border-t xl:border-l xl:first:border-l-0 xl:[&:nth-child(n+3)]:border-t-0">
      <p className="truncate text-2xs tracking-wide text-faint uppercase">{label}</p>
      {loading ? (
        <Skeleton className="h-7 w-24" />
      ) : (
        <p className={cn("tabular text-xl leading-tight font-semibold", tone)}>{value}</p>
      )}
      <div className="truncate text-2xs text-faint">{detail}</div>
    </div>
  );
}

/**
 * Delta compares this range against the one before it. It says nothing at all when there is
 * nothing to compare against: "+100%" against zero is not a fact about spending.
 */
function Delta({ now, before, label }: { now: number; before: number; label: string }) {
  if (before <= 0) {
    return <span>nothing billed {label.replace(/^vs /, "")}</span>;
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
