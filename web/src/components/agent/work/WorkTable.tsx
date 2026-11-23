import { useState } from "react";
import { useNavigate, useSearchParams } from "react-router";
import { keepPreviousData, useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { ChevronRight, ExternalLink, Rows3, SquareKanban } from "lucide-react";
import type { WorkItem } from "../../../gen/podium/agent/v1/agent_pb";
import { agent, errorMessage, isAgentUnreachable } from "../../../lib/client";
import { absolute, relative } from "../../../lib/format";
import { money } from "../../../lib/routines";
import { useViewer } from "../../../lib/identity";
import { canManageInfra } from "../../../lib/rbac";
import { cn } from "../../../lib/utils";
import { Badge } from "../../Badge";
import { Empty } from "../../Empty";
import { Pager } from "../../Pager";
import { usePager, type PagerState } from "../../../lib/usePager";
import { TableSkeleton } from "../../Skeleton";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "../../ui/select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "../../ui/table";
import { ToggleGroup, ToggleGroupItem } from "../../ui/toggle-group";
import { ConductorDown } from "../ConductorDown";
import { LinearMcpHint } from "../LinearMcpHint";
import { ProviderMark } from "./ProviderMark";
import { WorkBoard } from "./WorkBoard";
import {
  POLL_MS,
  type StateFilter,
  type View,
  type WorkFilters,
  FIRST_COLUMN,
  boardColumnQuery,
  itemLabel,
  podiumState,
  providerName,
  storeView,
  storedView,
} from "./workView";

/** ALL is a Select's "every one", which a Select cannot hold as "". */
const ALL = "*";

/** ITEM is a filter toggle the height of the chat rail's filter, so the two read as one control. */
const ITEM = "h-9 px-2.5";

/** SELECT is a filter Select the height, border, corners and type of the toggles beside it. */
const SELECT = "h-[2.625rem] min-h-0 rounded-lg border-border px-2.5 text-xs font-medium";

/** teamValue is one provider's team as a Select value; a team key is unique within its provider only. */
const teamValue = (provider: string, key: string) => `${provider}:${key}`;

/** workKey is where one filter's page of the list is cached. */
const workKey = (f: WorkFilters, size: number, cursor: string) => [
  "agent",
  "work",
  f.all,
  f.provider,
  f.team,
  f.state,
  size,
  cursor,
];

/**
 * WorkTable is the Issues view: one row per issue (or project) the viewer is involved in, from
 * every issue tracker Podium is connected to — today Linear — each a conversation of its own
 * however it was started: the issue delegated to the app, the app @mentioned on it, or a routine
 * that ran about it. There are too many for the chat rail, so they are listed here, newest
 * activity first, and filtered on the server.
 *
 * What a row says about the issue — its key, title, team and state in its tracker — is what the
 * tracker's webhooks said, kept by the conductor; nothing here asks the tracker. A row opens the
 * issue's conversation, read-only as a pull request's is, and its key opens the issue where it
 * lives.
 *
 * Involved is assigned or created the issue, asked Podium about it (delegated it, @mentioned the
 * app, prompted it, or spoke in its conversation), or was @mentioned in it — in the tracker, once
 * the viewer's account there is linked to Podium. Admins and owners have one more toggle, All
 * issues, which lists every one. Who may open a conversation is wider than the list: a person who
 * commented on an issue, or was shared its conversation, opens it from a link.
 *
 * The same issues lay out as a table or as a board of Podium's states (WorkBoard), under the same
 * filters; on the board the columns are the state filter, so its toggle is the table's only. The layout is ?view=board in the URL, so a board can be linked, and the browser
 * remembers the last one chosen for a visit whose URL names none.
 */
export function WorkTable() {
  const navigate = useNavigate();
  const [params, setParams] = useSearchParams();
  const [remembered, setRemembered] = useState<View>(storedView);
  const admin = canManageInfra(useViewer());
  const [everyIssue, setEveryIssue] = useState(false);
  const all = admin && everyIssue;
  const [provider, setProvider] = useState("");
  const [team, setTeam] = useState("");
  const [state, setState] = useState<StateFilter>("");
  const filters: WorkFilters = { all, provider, team, state };

  const asked = params.get("view");
  const view: View = asked === "board" || asked === "table" ? asked : remembered;
  const setView = (v: View) => {
    storeView(v);
    setRemembered(v);
    setParams(
      (p) => {
        if (v === "board") p.set("view", "board");
        else p.delete("view");
        return p;
      },
      { replace: true },
    );
  };

  // The server pages the list, so a workspace's thousands of issues are never read at once. A
  // new filter starts again from the first page.
  const pager = usePager([all, provider, team, state]);
  const read = (cursor: string) => () =>
    agent.listWorkItems({ all, provider, team, state, page: { limit: pager.size, cursor } });
  const list = useQuery({
    queryKey: workKey(filters, pager.size, pager.cursor),
    queryFn: read(pager.cursor),
    refetchInterval: POLL_MS,
    // A filter not asked for yet keeps the rows already shown, dimmed, until its own arrive,
    // rather than swapping the table for a skeleton and back.
    placeholderData: keepPreviousData,
    enabled: view === "table",
  });
  // The tracker counts the issues on the first page only; a later page reads it from there,
  // where it is still cached from when the list started.
  const firstPage = useQuery({ queryKey: workKey(filters, pager.size, ""), queryFn: read(""), enabled: false });
  // The board's first column, always on it: its pages carry the teams and providers for the
  // filters, and its failure is the board's.
  const firstColumn = useInfiniteQuery({ ...boardColumnQuery(filters, FIRST_COLUMN), enabled: view === "board" });
  const shown = view === "table" ? list : firstColumn;

  const rows = list.data?.items ?? [];
  const first = view === "table" ? list.data : firstColumn.data?.pages[0];
  const providers = first?.providers ?? [];
  const several = providers.length > 1;
  // With several providers a team is one provider's, so the list names whose.
  const teams = (first?.teams ?? []).filter((t) => provider === "" || t.provider === provider);
  const filtered = provider !== "" || team !== "" || (view === "table" && state !== "");

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center gap-2" data-testid="work-filters">
        {admin ? (
          <ToggleGroup
            type="single"
            value={all ? "all" : "mine"}
            onValueChange={(v) => v && setEveryIssue(v === "all")}
            aria-label="Whose issues"
          >
            <ToggleGroupItem value="mine" className={ITEM}>Mine</ToggleGroupItem>
            <ToggleGroupItem value="all" className={ITEM}>All issues</ToggleGroupItem>
          </ToggleGroup>
        ) : null}
        {view === "table" ? (
          <ToggleGroup
            type="single"
            value={state || "any"}
            onValueChange={(v) => v && setState(v === "any" ? "" : (v as StateFilter))}
            aria-label="Podium's state"
          >
            <ToggleGroupItem value="any" className={ITEM}>Any state</ToggleGroupItem>
            <ToggleGroupItem value="working" className={ITEM}>Working</ToggleGroupItem>
            <ToggleGroupItem value="attention" className={ITEM}>Needs attention</ToggleGroupItem>
          </ToggleGroup>
        ) : null}
        {several || provider !== "" ? (
          <Select
            value={provider || ALL}
            onValueChange={(v) => {
              setProvider(v === ALL ? "" : v);
              setTeam("");
            }}
          >
            <SelectTrigger size="sm" className={cn(SELECT, "w-[9rem]")} aria-label="Tracker" data-testid="work-provider">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={ALL}>Every tracker</SelectItem>
              {providers.map((p) => (
                <SelectItem key={p} value={p}>
                  {providerName(p)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        ) : null}
        {teams.length > 0 || team !== "" ? (
          <Select
            value={team ? teamValue(provider || (first?.teams.find((t) => t.key === team)?.provider ?? ""), team) : ALL}
            onValueChange={(v) => {
              if (v === ALL) return setTeam("");
              const t = teams.find((t) => teamValue(t.provider, t.key) === v);
              if (!t) return;
              setTeam(t.key);
              if (several) setProvider(t.provider);
            }}
          >
            <SelectTrigger size="sm" className={cn(SELECT, "w-[9rem]")} aria-label="Team" data-testid="work-team">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={ALL}>Every team</SelectItem>
              {teams.map((t) => (
                <SelectItem key={teamValue(t.provider, t.key)} value={teamValue(t.provider, t.key)}>
                  {several ? `${t.key} · ${providerName(t.provider)}` : t.key}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        ) : null}
        <ToggleGroup
          type="single"
          value={view}
          onValueChange={(v) => v && setView(v as View)}
          aria-label="Layout"
          className="sm:ml-auto"
        >
          <ToggleGroupItem value="table" className={ITEM}>
            <Rows3 aria-hidden />
            Table
          </ToggleGroupItem>
          <ToggleGroupItem value="board" className={ITEM}>
            <SquareKanban aria-hidden />
            Board
          </ToggleGroupItem>
        </ToggleGroup>
      </div>

      {providers.includes("linear") ? <LinearMcpHint /> : null}

      {shown.isError ? (
        isAgentUnreachable(shown.error) ? (
          <ConductorDown
            what="Could not list issues"
            onRetry={() => void shown.refetch()}
            retrying={shown.isFetching}
          />
        ) : (
          <Empty icon={SquareKanban} title="Could not list issues" hint={errorMessage(shown.error)} />
        )
      ) : view === "board" ? (
        <WorkBoard filters={filters} />
      ) : (
        <WorkTableBody
          rows={rows}
          filtered={filtered}
          linear={providers.includes("linear")}
          all={all}
          pending={list.isPending}
          success={list.isSuccess}
          dimmed={list.isPlaceholderData}
          pager={pager}
          total={firstPage.data?.total ?? list.data?.total}
          nextCursor={list.data?.nextCursor ?? ""}
          onOpen={(id) => void navigate(`/agent/chat/${id}`)}
        />
      )}
    </div>
  );
}

/** WorkTableBody is the table layout of the Issues view: one row per issue, a page at a time. */
function WorkTableBody({
  rows,
  filtered,
  linear,
  all,
  pending,
  success,
  dimmed,
  pager,
  total,
  nextCursor,
  onOpen,
}: {
  rows: WorkItem[];
  filtered: boolean;
  /** linear is Linear among the trackers, which the empty view says how to reach. */
  linear: boolean;
  /** all is every issue listed, not the viewer's own. */
  all: boolean;
  pending: boolean;
  success: boolean;
  dimmed: boolean;
  pager: PagerState;
  /** total is every issue under the filters, when the tracker counted them. */
  total?: number;
  nextCursor: string;
  onOpen: (chatId: string) => void;
}) {
  return (
    <>
      {pending ? <TableSkeleton rows={6} cols={6} /> : null}

      {success && rows.length === 0 ? (
        <Empty
          icon={SquareKanban}
          title={filtered ? "No issues match." : all ? "No issues yet." : "No issues of yours yet."}
          hint={
            filtered
              ? "Clear a filter to see more."
              : all
                ? "An issue appears here once an assistant works on it in a connected issue tracker."
                : !linear
                  ? "An issue is yours here once you are assigned it, created it, asked Podium about it, or were @mentioned in it."
                  : "An issue is yours here once you are assigned it, created it, asked Podium about it, or were @mentioned in it in Linear. Delegate an issue to the Podium app or @mention it in a comment; your Linear account has to be linked to Podium."
          }
        />
      ) : null}

      {rows.length > 0 ? (
        <Table
          aria-busy={dimmed}
          className={cn("transition-opacity duration-150", dimmed && "opacity-60")}
          footer={
            <Pager
              pager={pager}
              count={rows.length}
              noun={{ one: "issue", other: "issues" }}
              total={total || undefined}
              nextCursor={nextCursor}
              loading={dimmed}
            />
          }
        >
          <TableHeader>
            <TableRow className="hover:bg-transparent">
              <TableHead>Issue</TableHead>
              <TableHead>Team</TableHead>
              <TableHead>Podium</TableHead>
              <TableHead>Last activity</TableHead>
              <TableHead className="text-right">Spend</TableHead>
              <TableHead />
            </TableRow>
          </TableHeader>
          <TableBody>
            {rows.map((c) => (
              <WorkRow key={c.chatId} c={c} onOpen={() => onOpen(c.chatId)} />
            ))}
          </TableBody>
        </Table>
      ) : null}

    </>
  );
}

function WorkRow({ c, onOpen }: { c: WorkItem; onOpen: () => void }) {
  const state = podiumState(c.state);
  const label = itemLabel(c);
  const where = providerName(c.provider);
  return (
    <TableRow
      data-testid="work-row"
      tabIndex={0}
      aria-label={`Open the conversation of ${c.key ? `${c.key} ` : ""}${c.title}`}
      onClick={onOpen}
      onKeyDown={(e) => {
        if (e.key === "Enter" || e.key === " ") {
          e.preventDefault();
          onOpen();
        }
      }}
      className="group/row cursor-pointer outline-none focus-visible:ring-2 focus-visible:ring-ring/50 focus-visible:ring-inset"
    >
      <TableCell className="max-w-[28rem]">
        <div className="flex min-w-0 items-baseline gap-2">
          <ProviderMark provider={c.provider} />
          {c.url ? (
            <a
              href={c.url}
              target="_blank"
              rel="noreferrer"
              onClick={(e) => e.stopPropagation()}
              className="inline-flex shrink-0 items-center gap-1 font-mono text-xs text-accent hover:underline"
              title={`Open in ${where}`}
            >
              {label}
              <ExternalLink aria-hidden className="size-3" />
            </a>
          ) : (
            <span className="shrink-0 font-mono text-xs text-muted">{label}</span>
          )}
          <span className="min-w-0 truncate text-sm text-fg" title={c.title}>
            {c.title || "Untitled"}
          </span>
        </div>
        {c.statusLabel ? (
          <div className="text-2xs text-faint">
            In {where}: {c.statusLabel}
          </div>
        ) : null}
      </TableCell>
      <TableCell className="text-xs text-muted">{c.team || "—"}</TableCell>
      <TableCell>
        <Badge tone={state.tone}>{state.label}</Badge>
        {c.state === "waiting" && c.askedOf ? (
          <div className="mt-0.5 truncate text-2xs text-faint">asked of {c.askedOf}</div>
        ) : null}
      </TableCell>
      <TableCell className="max-w-[20rem]">
        <div className="text-xs whitespace-nowrap text-fg" title={absolute(c.lastActivityAt)}>
          {relative(c.lastActivityAt)}
        </div>
        {c.lastText ? <div className="truncate text-2xs text-faint">{c.lastText}</div> : null}
      </TableCell>
      <TableCell className="text-right text-xs tabular-nums text-muted">{money(c.costUsd)}</TableCell>
      <TableCell className="w-0">
        <ChevronRight aria-hidden className="size-4 text-faint transition-colors group-hover/row:text-muted" />
      </TableCell>
    </TableRow>
  );
}
