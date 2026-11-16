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
import { agent } from "../../lib/client";
import type { Tone } from "../Badge";

/** How often the Linear view refreshes while it is open. */
export const POLL_MS = 15_000;

/** Where Podium is with an issue, in words and a tone. */
export const STATE: Record<string, { label: string; tone: Tone }> = {
  waiting: { label: "waiting on someone", tone: "warn" },
  working: { label: "working", tone: "run" },
  failed: { label: "failed", tone: "err" },
  done: { label: "done", tone: "ok" },
  new: { label: "not started", tone: "idle" },
};

/** podiumState is STATE's entry for a conversation's state, or the state itself, untoned. */
export const podiumState = (state: string) =>
  STATE[state] ?? { label: state || "unknown", tone: "idle" as Tone };

export type StateFilter = "" | "working" | "attention";

/** LinearFilters are the Linear view's filters, which the table and the board share. */
export type LinearFilters = { mine: boolean; team: string; state: StateFilter };

/** View is how the Linear view lays its issues out. */
export type View = "table" | "board";

/** VIEW_KEY is where a browser remembers the view it was last left on. */
export const VIEW_KEY = "podium.linear.view";

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
  /** type is the Linear state type the column lists, as ListLinearConversations names it. */
  type: string;
  label: string;
  icon: LucideIcon;
  /** optional is a column shown only while something is in it. */
  optional?: boolean;
};

/**
 * COLUMNS are Linear's state types in Linear's order, then what has none: a project, or an issue
 * whose state the webhooks never named. Triage is a team setting many workspaces leave off, so it
 * and No state appear only when something is in them; the rest always do, so the board keeps its
 * shape from one filter to the next.
 */
export const COLUMNS: Column[] = [
  { type: "triage", label: "Triage", icon: Inbox, optional: true },
  { type: "backlog", label: "Backlog", icon: CircleDashed },
  { type: "unstarted", label: "Todo", icon: Circle },
  { type: "started", label: "In progress", icon: CircleDot },
  { type: "completed", label: "Done", icon: CircleCheck },
  { type: "canceled", label: "Canceled", icon: CircleX },
  { type: "none", label: "No state", icon: CircleSlash, optional: true },
];

/**
 * boardColumnQuery is one column's pages: the filters plus the column's state type, paged on the
 * server ten at a time. The view's team filter reads its teams from the Backlog column's, so the
 * two share one cache entry and one request.
 */
export function boardColumnQuery(f: LinearFilters, type: string) {
  return infiniteQueryOptions({
    queryKey: ["agent", "linear", "board", f.mine, f.team, f.state, type],
    queryFn: ({ pageParam }) =>
      agent.listLinearConversations({
        mine: f.mine,
        team: f.team,
        state: f.state,
        linearStateType: type,
        page: { limit: BOARD_PAGE, cursor: pageParam },
      }),
    initialPageParam: "",
    getNextPageParam: (last) => last.nextCursor || undefined,
    refetchInterval: POLL_MS,
    // A filter not asked for yet keeps the cards already shown, dimmed, until its own arrive.
    placeholderData: keepPreviousData,
  });
}
