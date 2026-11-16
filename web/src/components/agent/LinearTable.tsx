import { useState } from "react";
import { useNavigate } from "react-router";
import { keepPreviousData, useInfiniteQuery } from "@tanstack/react-query";
import { ChevronRight, ExternalLink, SquareKanban } from "lucide-react";
import type { LinearConversation } from "../../gen/podium/agent/v1/agent_pb";
import { agent, errorMessage, isAgentUnreachable } from "../../lib/client";
import { absolute, relative } from "../../lib/format";
import { money } from "../../lib/routines";
import { cn } from "../../lib/utils";
import { Badge, type Tone } from "../Badge";
import { Empty } from "../Empty";
import { TableSkeleton } from "../Skeleton";
import { Button } from "../ui/button";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "../ui/select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "../ui/table";
import { ToggleGroup, ToggleGroupItem } from "../ui/toggle-group";
import { ConductorDown } from "./ConductorDown";
import { LinearMcpHint } from "./LinearMcpHint";

/** How often the list refreshes while it is open. */
const POLL_MS = 15_000;

/** One page of the list. The server pages it, so a workspace's thousands of issues are never read at once. */
const PAGE = 50;

/** ALL_TEAMS is the team filter's "every team", which a Select cannot hold as "". */
const ALL_TEAMS = "*";

/** Where Podium is with an issue, in words and a tone. */
const STATE: Record<string, { label: string; tone: Tone }> = {
  waiting: { label: "waiting on someone", tone: "warn" },
  working: { label: "working", tone: "run" },
  failed: { label: "failed", tone: "err" },
  done: { label: "done", tone: "ok" },
  new: { label: "not started", tone: "idle" },
};

type StateFilter = "" | "working" | "attention";

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
 */
export function LinearTable() {
  const navigate = useNavigate();
  const [mine, setMine] = useState(false);
  const [team, setTeam] = useState("");
  const [state, setState] = useState<StateFilter>("");

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
  });

  const rows = list.data?.pages.flatMap((p) => p.conversations) ?? [];
  const teams = list.data?.pages[0]?.teams ?? [];
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
          <ToggleGroupItem value="all">All I can see</ToggleGroupItem>
          <ToggleGroupItem value="mine">Mine</ToggleGroupItem>
        </ToggleGroup>
        <ToggleGroup
          type="single"
          value={state || "any"}
          onValueChange={(v) => v && setState(v === "any" ? "" : (v as StateFilter))}
          aria-label="Podium's state"
        >
          <ToggleGroupItem value="any">Any state</ToggleGroupItem>
          <ToggleGroupItem value="working">Working</ToggleGroupItem>
          <ToggleGroupItem value="attention">Needs attention</ToggleGroupItem>
        </ToggleGroup>
        {teams.length > 0 || team !== "" ? (
          <Select value={team || ALL_TEAMS} onValueChange={(v) => setTeam(v === ALL_TEAMS ? "" : v)}>
            <SelectTrigger
              size="sm"
              className="h-[2.125rem] min-h-0 w-[9rem] rounded-lg border-border px-2.5 text-xs font-medium"
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
      </div>

      <LinearMcpHint />

      {list.isPending ? <TableSkeleton rows={6} cols={6} /> : null}

      {list.isError ? (
        isAgentUnreachable(list.error) ? (
          <ConductorDown
            what="Could not list Linear issues"
            onRetry={() => void list.refetch()}
            retrying={list.isFetching}
          />
        ) : (
          <Empty icon={SquareKanban} title="Could not list Linear issues" hint={errorMessage(list.error)} />
        )
      ) : null}

      {list.isSuccess && rows.length === 0 ? (
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
        <Table
          aria-busy={list.isPlaceholderData}
          className={cn("transition-opacity duration-150", list.isPlaceholderData && "opacity-60")}
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
              <LinearRow key={c.chatId} c={c} onOpen={() => void navigate(`/agent/chat/${c.chatId}`)} />
            ))}
          </TableBody>
        </Table>
      ) : null}

      {list.hasNextPage ? (
        <div className="flex justify-center">
          <Button
            variant="outline"
            size="sm"
            disabled={list.isFetchingNextPage}
            onClick={() => void list.fetchNextPage()}
          >
            {list.isFetchingNextPage ? "Loading…" : "Show more"}
          </Button>
        </div>
      ) : null}
    </div>
  );
}

function LinearRow({ c, onOpen }: { c: LinearConversation; onOpen: () => void }) {
  const state = STATE[c.state] ?? { label: c.state || "unknown", tone: "idle" as Tone };
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
