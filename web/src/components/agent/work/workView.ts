import { infiniteQueryOptions, keepPreviousData } from "@tanstack/react-query";
import { CircleCheck, CircleDashed, CircleDot, CircleX, Hourglass, type LucideIcon } from "lucide-react";
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
 * WorkFilters are the Issues view's filters, which the table and the board share. all is every
 * issue rather than the viewer's own, an admin's or an owner's to ask for. provider is "" for
 * every issue tracker; it is only ever set while more than one is connected. state is the
 * table's: the board's columns are the states.
 */
export type WorkFilters = { all: boolean; provider: string; team: string; state: StateFilter };

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
  /** state is the Podium state the column lists, as ListWorkItems names it. */
  state: string;
  label: string;
  icon: LucideIcon;
};

/**
 * COLUMNS are where Podium is with each issue, in the order work moves through them: not started,
 * working, waiting on someone, failed, done. Every column always shows, so the board keeps its
 * shape from one filter to the next. The tracker's own state is on each card.
 */
export const COLUMNS: Column[] = [
  { state: "new", label: "Not started", icon: CircleDashed },
  { state: "working", label: "Working", icon: CircleDot },
  { state: "waiting", label: "Waiting on someone", icon: Hourglass },
  { state: "failed", label: "Failed", icon: CircleX },
  { state: "done", label: "Done", icon: CircleCheck },
];

/** FIRST_COLUMN is the column whose pages carry the teams and providers for the filters. */
export const FIRST_COLUMN = COLUMNS[0].state;

/**
 * boardColumnQuery is one column's pages: the filters plus the column's state, paged on the
 * server ten at a time. The view's team and provider filters read from the first column's pages,
 * so the two share one cache entry and one request.
 */
export function boardColumnQuery(f: WorkFilters, state: string) {
  return infiniteQueryOptions({
    queryKey: ["agent", "work", "board", f.all, f.provider, f.team, state],
    queryFn: ({ pageParam }) =>
      agent.listWorkItems({
        all: f.all,
        provider: f.provider,
        team: f.team,
        state,
        page: { limit: BOARD_PAGE, cursor: pageParam },
      }),
    initialPageParam: "",
    getNextPageParam: (last) => last.nextCursor || undefined,
    refetchInterval: POLL_MS,
    // A filter not asked for yet keeps the cards already shown, dimmed, until its own arrive.
    placeholderData: keepPreviousData,
  });
}
