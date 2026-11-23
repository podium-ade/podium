import { useState } from "react";

/** PAGE_SIZES are the page sizes a paged list offers. */
export const PAGE_SIZES = [25, 50, 100];

/** DEFAULT_PAGE_SIZE is how many rows a paged list shows until someone picks another size. */
export const DEFAULT_PAGE_SIZE = PAGE_SIZES[0];

/** A page on the trail: the cursor that reads it, and the row it starts at (1-based). */
type Step = { cursor: string; first: number };

const FIRST_PAGE: Step[] = [{ cursor: "", first: 1 }];

/** PagerState is where a paged list is, and how to move it. usePager returns one. */
export type PagerState = {
  /** size is how many rows a page holds. */
  size: number;
  /** cursor is the server cursor that reads this page; "" is the first. */
  cursor: string;
  /** first is the row this page starts at, 1-based. */
  first: number;
  /** offset is how many rows come before this page: first - 1. */
  offset: number;
  /** page is the page number, 1-based. */
  page: number;
  setSize: (n: number) => void;
  /** previous steps back one page. A no-op on the first. */
  previous: () => void;
  /** next steps on to the page after one holding count rows, read by cursor ("" for a list paged by offset). */
  next: (cursor: string, count: number) => void;
};

/**
 * usePager keeps a paged list's place: the pages walked so far (each with its cursor and the
 * row it starts at) and the page size. A list paged by a server cursor reads `cursor`; one paged
 * by offset, or sliced on the client, reads `offset`. Either way Previous steps back along the
 * trail rather than jumping to a numbered page.
 *
 * scope is everything that decides which rows the list holds — its filters, search and range.
 * When it changes the list starts again from the first page; the page size stays as it was.
 */
export function usePager(scope: unknown, initialSize = DEFAULT_PAGE_SIZE): PagerState {
  const key = JSON.stringify(scope ?? null);
  const [state, setState] = useState({ key, size: initialSize, trail: FIRST_PAGE });
  // Adjusted during render, not in an effect, so no frame ever shows a later page of the old
  // scope under the new one.
  if (state.key !== key) setState({ ...state, key, trail: FIRST_PAGE });
  const trail = state.key === key ? state.trail : FIRST_PAGE;
  const at = trail[trail.length - 1];
  return {
    size: state.size,
    cursor: at.cursor,
    first: at.first,
    offset: at.first - 1,
    page: trail.length,
    setSize: (size) => setState({ key, size, trail: FIRST_PAGE }),
    previous: () =>
      setState((s) => (s.trail.length > 1 ? { ...s, trail: s.trail.slice(0, -1) } : s)),
    next: (cursor, count) =>
      setState((s) => {
        const last = s.trail[s.trail.length - 1];
        return { ...s, trail: [...s.trail, { cursor, first: last.first + count }] };
      }),
  };
}
