import { infiniteQueryOptions, keepPreviousData } from "@tanstack/react-query";
import {
  Circle,
  CircleCheck,
  CircleDashed,
  CircleDot,
  CircleSlash,
  CircleX,
  Inbox,
  type LucideIcon,
} from "lucide-react";
import type { WorkItem } from "../../../gen/podium/agent/v1/agent_pb";
import linear from "../../../assets/marks/linear.svg";
import { agent } from "../../../lib/client";
import type { Tone } from "../../Badge";

/** PROVIDERS are the issue trackers the Issues view knows by name and mark. */
export const PROVIDERS: Record<string, { name: string; mark?: string }> = {
  linear: { name: "Linear", mark: linear },
};

/** providerName is how a provider is written in a sentence: "Linear", or its key when unknown. */
export const providerName = (provider: string) => PROVIDERS[provider]?.name ?? provider;

/** How often the Issues view refreshes while it is open. */
export const POLL_MS = 15_000;

/** Where Podium is with an issue, in words and a tone. */
export const STATE: Record<string, { label: string; tone: Tone }> = {
  waiting: { label: "waiting on someone", tone: "warn" },
  working: { label: "working", tone: "run" },
  failed: { label: "failed", tone: "err" },
  done: { label: "done", tone: "ok" },
  new: { label: "not started", tone: "idle" },
};

/** podiumState is STATE's entry for an item's state, or the state itself, untoned. */
export const podiumState = (state: string) =>
  STATE[state] ?? { label: state || "unknown", tone: "idle" as Tone };

/** itemLabel is an item's key, or what it is when its provider gave it none. */
export const itemLabel = (c: WorkItem) => c.key || (c.kind === "project" ? "Project" : "Issue");

export type StateFilter = "" | "working" | "attention";

/**
 * WorkFilters are the Issues view's filters, which the table and the board share. provider is ""
 * for every issue tracker; it is only ever set while more than one is connected.
 */
export type WorkFilters = { mine: boolean; provider: string; team: string; state: StateFilter };

/** View is how the Issues view lays its issues out. */
export type View = "table" | "board";

/** VIEW_KEY is where a browser remembers the view it was last left on. */
export const VIEW_KEY = "podium.work.view";

/** storedView is the view this browser was last left on, "table" when it never chose. */
export function storedView(): View {
  try {
    return localStorage.getItem(VIEW_KEY) === "board" ? "board" : "table";
  } catch {
    return "table";
  }
}

/** storeView remembers the view for the next visit; a browser that keeps nothing forgets it. */
export function storeView(view: View) {
  try {
    localStorage.setItem(VIEW_KEY, view);
  } catch {
    // Storage blocked: the URL still carries the choice.
  }
}

/** One page of a column: a column shows this many, and Load more the next as many. */
export const BOARD_PAGE = 10;

export type Column = {
  /** status is the status the column lists, as ListWorkItems names it ("none" for the rest). */
  status: string;
  label: string;
  icon: LucideIcon;
  /** optional is a column shown only while something is in it. */
  optional?: boolean;
};

/**
 * COLUMNS are the statuses every issue tracker's states map to, in a board's order, then Other:
 * a project, or an issue whose state the provider never named or named one Podium does not
 * know. Triage is a setting many workspaces leave off, so it and Other appear only when
 * something is in them; the rest always do, so the board keeps its shape from one filter to the
 * next.
 */
export const COLUMNS: Column[] = [
  { status: "triage", label: "Triage", icon: Inbox, optional: true },
  { status: "backlog", label: "Backlog", icon: CircleDashed },
  { status: "todo", label: "Todo", icon: Circle },
  { status: "in_progress", label: "In progress", icon: CircleDot },
  { status: "done", label: "Done", icon: CircleCheck },
  { status: "canceled", label: "Canceled", icon: CircleX },
  { status: "none", label: "Other", icon: CircleSlash, optional: true },
];

/**
 * boardColumnQuery is one column's pages: the filters plus the column's status, paged on the
 * server ten at a time. The view's team and provider filters read from the Backlog column's
 * pages, so the two share one cache entry and one request.
 */
export function boardColumnQuery(f: WorkFilters, status: string) {
  return infiniteQueryOptions({
    queryKey: ["agent", "work", "board", f.mine, f.provider, f.team, f.state, status],
    queryFn: ({ pageParam }) =>
      agent.listWorkItems({
        mine: f.mine,
        provider: f.provider,
        team: f.team,
        state: f.state,
        status,
        page: { limit: BOARD_PAGE, cursor: pageParam },
      }),
    initialPageParam: "",
    getNextPageParam: (last) => last.nextCursor || undefined,
    refetchInterval: POLL_MS,
    // A filter not asked for yet keeps the cards already shown, dimmed, until its own arrive.
    placeholderData: keepPreviousData,
  });
}
