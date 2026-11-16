import { useState } from "react";
import { useNavigate, useSearchParams } from "react-router";
import { keepPreviousData, useInfiniteQuery } from "@tanstack/react-query";
import { ChevronRight, ExternalLink, Rows3, SquareKanban } from "lucide-react";
import type { LinearConversation } from "../../gen/podium/agent/v1/agent_pb";
import { agent, errorMessage, isAgentUnreachable } from "../../lib/client";
import { absolute, relative } from "../../lib/format";
import { money } from "../../lib/routines";
import { cn } from "../../lib/utils";
import { Badge } from "../Badge";
import { Empty } from "../Empty";
import { TableSkeleton } from "../Skeleton";
import { Button } from "../ui/button";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "../ui/select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "../ui/table";
import { ToggleGroup, ToggleGroupItem } from "../ui/toggle-group";
import { ConductorDown } from "./ConductorDown";
import { LinearBoard } from "./LinearBoard";
import { LinearMcpHint } from "./LinearMcpHint";
import {
  type LinearFilters,
  POLL_MS,
  boardColumnQuery,
  type StateFilter,
  type View,
  podiumState,
  storeView,
  storedView,
} from "./linearView";

/** One page of the list. The server pages it, so a workspace's thousands of issues are never read at once. */
const PAGE = 50;

/** ALL_TEAMS is the team filter's "every team", which a Select cannot hold as "". */
const ALL_TEAMS = "*";

/** ITEM is a filter toggle the height of the chat rail's filter, so the two read as one control. */
const ITEM = "h-9 px-2.5";

/** linearKey is where one filter's pages of the list are cached. */
const linearKey = (mine: boolean, team: string, state: StateFilter) => [
  "agent",
  "linear",
  mine,
  team,
  state,
];

/**
 * LinearTable is the Linear view: one row per Linear issue (or project) Podium works on, each a
 * conversation of its own however it was started — the issue delegated to the app, the app
 * @mentioned on it, or a routine that ran about it. There are too many for the chat rail, so they
 * are listed here, newest activity first, and filtered on the server.
 *
 * What a row says about the issue — its identifier, title, team and Linear state — is what the
 * webhooks said, kept by the conductor; nothing here asks Linear. A row opens the issue's
 * conversation, read-only as a pull request's is, and its identifier opens the issue in Linear.
 *
 * Admins and owners see every issue; anybody else sees the issues they touched in Linear (once
 * their Linear account is linked to Podium), spoke in, or were shared.
 *
 * The same issues lay out as a table or as a board of Linear's states (LinearBoard), under the
 * same filters. The layout is ?view=board in the URL, so a board can be linked, and the browser
 * remembers the last one chosen for a visit whose URL names none.
 */
export function LinearTable() {
  const navigate = useNavigate();
  const [params, setParams] = useSearchParams();
  const [remembered, setRemembered] = useState<View>(storedView);
  const [mine, setMine] = useState(false);
  const [team, setTeam] = useState("");
  const [state, setState] = useState<StateFilter>("");
  const filters: LinearFilters = { mine, team, state };

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

  const list = useInfiniteQuery({
    queryKey: linearKey(mine, team, state),
    queryFn: ({ pageParam }) =>
      agent.listLinearConversations({ mine, team, state, page: { limit: PAGE, cursor: pageParam } }),
    initialPageParam: "",
    getNextPageParam: (last) => last.nextCursor || undefined,
    refetchInterval: POLL_MS,
    // A filter not asked for yet keeps the rows already shown, dimmed, until its own arrive,
    // rather than swapping the table for a skeleton and back.
    placeholderData: keepPreviousData,
    enabled: view === "table",
  });
  // The board's Backlog column, always on it: its pages carry the teams for the team filter,
  // and its failure is the board's.
  const backlog = useInfiniteQuery({ ...boardColumnQuery(filters, "backlog"), enabled: view === "board" });
  const shown = view === "table" ? list : backlog;

  const rows = list.data?.pages.flatMap((p) => p.conversations) ?? [];
  const teams = shown.data?.pages[0]?.teams ?? [];
  const filtered = mine || team !== "" || state !== "";

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center gap-2" data-testid="linear-filters">
        <ToggleGroup
          type="single"
          value={mine ? "mine" : "all"}
          onValueChange={(v) => v && setMine(v === "mine")}
          aria-label="Whose issues"
        >
          <ToggleGroupItem value="all" className={ITEM}>All I can see</ToggleGroupItem>
          <ToggleGroupItem value="mine" className={ITEM}>Mine</ToggleGroupItem>
        </ToggleGroup>
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
        {teams.length > 0 || team !== "" ? (
          <Select value={team || ALL_TEAMS} onValueChange={(v) => setTeam(v === ALL_TEAMS ? "" : v)}>
            <SelectTrigger
              size="sm"
              className="h-[2.625rem] min-h-0 w-[9rem] rounded-lg border-border px-2.5 text-xs font-medium"
              aria-label="Team"
              data-testid="linear-team"
            >
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={ALL_TEAMS}>Every team</SelectItem>
              {teams.map((t) => (
                <SelectItem key={t} value={t}>
                  {t}
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

      <LinearMcpHint />

      {shown.isError ? (
        isAgentUnreachable(shown.error) ? (
          <ConductorDown
            what="Could not list Linear issues"
            onRetry={() => void shown.refetch()}
            retrying={shown.isFetching}
          />
        ) : (
          <Empty icon={SquareKanban} title="Could not list Linear issues" hint={errorMessage(shown.error)} />
        )
      ) : view === "board" ? (
        <LinearBoard filters={filters} />
      ) : (
        <LinearTableBody
          rows={rows}
          filtered={filtered}
          pending={list.isPending}
          success={list.isSuccess}
          dimmed={list.isPlaceholderData}
          more={list.hasNextPage}
          loadingMore={list.isFetchingNextPage}
          onMore={() => void list.fetchNextPage()}
          onOpen={(id) => void navigate(`/agent/chat/${id}`)}
        />
      )}
    </div>
  );
}

/** LinearTableBody is the table layout of the Linear view: one row per issue, fifty a page. */
function LinearTableBody({
  rows,
  filtered,
  pending,
  success,
  dimmed,
  more,
  loadingMore,
  onMore,
  onOpen,
}: {
  rows: LinearConversation[];
  filtered: boolean;
  pending: boolean;
  success: boolean;
  dimmed: boolean;
  more: boolean;
  loadingMore: boolean;
  onMore: () => void;
  onOpen: (chatId: string) => void;
}) {
  return (
    <>
      {pending ? <TableSkeleton rows={6} cols={6} /> : null}

      {success && rows.length === 0 ? (
        <Empty
          icon={SquareKanban}
          title={filtered ? "No issues match." : "No Linear issues yet."}
          hint={
            filtered
              ? "Clear a filter to see more."
              : "Delegate an issue to the Podium app in Linear, or @mention it in a comment. An issue you touched appears here once your Linear account is linked to Podium."
          }
        />
      ) : null}

      {rows.length > 0 ? (
        <Table aria-busy={dimmed} className={cn("transition-opacity duration-150", dimmed && "opacity-60")}>
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
              <LinearRow key={c.chatId} c={c} onOpen={() => onOpen(c.chatId)} />
            ))}
          </TableBody>
        </Table>
      ) : null}

      {more ? (
        <div className="flex justify-center">
          <Button variant="outline" size="sm" disabled={loadingMore} onClick={onMore}>
            {loadingMore ? "Loading…" : "Show more"}
          </Button>
        </div>
      ) : null}
    </>
  );
}

function LinearRow({ c, onOpen }: { c: LinearConversation; onOpen: () => void }) {
  const state = podiumState(c.state);
  const label = c.identifier || (c.kind === "project" ? "Project" : "Issue");
  return (
    <TableRow
      data-testid="linear-row"
      tabIndex={0}
      aria-label={`Open the conversation of ${c.identifier ? `${c.identifier} ` : ""}${c.title}`}
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
          {c.url ? (
            <a
              href={c.url}
              target="_blank"
              rel="noreferrer"
              onClick={(e) => e.stopPropagation()}
              className="inline-flex shrink-0 items-center gap-1 font-mono text-xs text-accent hover:underline"
              title="Open in Linear"
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
        {c.linearState ? <div className="text-2xs text-faint">In Linear: {c.linearState}</div> : null}
      </TableCell>
      <TableCell className="text-xs text-muted">{c.teamKey || "—"}</TableCell>
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
