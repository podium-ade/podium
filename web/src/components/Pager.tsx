import { useEffect, useId, useRef } from "react";
import { ChevronLeft, ChevronRight } from "lucide-react";
import { Button } from "./ui/button";
import { Label } from "./ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "./ui/select";
import { PAGE_SIZES, type PagerState } from "../lib/usePager";

/** Noun is what a list holds, in the singular and the plural: "task" and "tasks". */
export type Noun = { one: string; other: string };


/**
 * Pager is the footer under a paged list: what the page holds on the left, then how many rows
 * a page shows and Previous / Next. Under a table it goes in the table's `footer`, inside the
 * table's card; under a list of cards it sits directly below the list.
 *
 * A list paged by a server cursor passes nextCursor ("" on the last page) and Next steps to
 * it; a list paged by offset, or sliced on the client, passes only total, and Next is offered
 * while rows remain past this page. The summary reads "26–50 of 132 tasks" when the total is
 * known, and "25 tasks on page 2" when it is not.
 *
 * While a page is on its way (loading) Previous and Next ignore presses: the page on screen
 * is what they would step from, and a second press would only repeat the first.
 */
export function Pager({
  pager,
  count,
  noun,
  total,
  nextCursor,
  loading = false,
  shown,
}: {
  pager: PagerState;
  /** count is how many rows this page holds. */
  count: number;
  noun: Noun;
  /** total is how many rows the whole list holds, when it is known. */
  total?: number;
  /** nextCursor is the cursor the server returned for the page after this one, for a list paged by cursor. */
  nextCursor?: string;
  loading?: boolean;
  /**
   * shown is the page whose rows are on screen, for a screen that keeps the last page up until
   * the next one has fully arrived; by default it is the pager's own.
   */
  shown?: { first: number; page: number };
}) {
  const id = useId();
  const first = shown?.first ?? pager.first;
  const page = shown?.page ?? pager.page;
  const last = first + count - 1;
  const more = nextCursor !== undefined ? nextCursor !== "" : total !== undefined && last < total;
  const word = (n: number) => (n === 1 ? noun.one : noun.other);
  // A press is spent until the page it asked for is on screen. loading only says so from the
  // next render, so two presses inside one frame are held off here as well.
  const moving = useRef(false);
  useEffect(() => {
    if (!loading) moving.current = false;
  }, [loading, pager.page, pager.size]);
  const go = (move: () => void) => () => {
    if (loading || moving.current) return;
    moving.current = true;
    move();
  };

  return (
    <nav
      aria-label={`${noun.other} pages`}
      aria-busy={loading || undefined}
      data-slot="pager"
      className="flex flex-wrap items-center justify-between gap-x-4 gap-y-3"
    >
      <p className="text-xs text-muted" data-testid="pager-summary" aria-live="polite">
        {total !== undefined && total >= last ? (
          <>
            <span className="tabular text-fg">
              {count === 0 ? 0 : `${first.toLocaleString()}–${last.toLocaleString()}`}
            </span>{" "}
            of <span className="tabular text-fg">{total.toLocaleString()}</span> {word(total)}
          </>
        ) : (
          <>
            <span className="tabular text-fg">{count.toLocaleString()}</span> {word(count)} on page{" "}
            <span className="tabular text-fg">{page}</span>
          </>
        )}
      </p>
      <div className="flex items-center gap-2">
        <Label htmlFor={id} className="text-2xs tracking-wide text-faint uppercase">
          Rows
        </Label>
        <Select value={String(pager.size)} onValueChange={(v) => pager.setSize(Number(v))}>
          <SelectTrigger id={id} size="sm" className="w-20">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {PAGE_SIZES.map((n) => (
              <SelectItem key={n} value={String(n)}>
                {n}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <Button variant="outline" size="sm" disabled={pager.page <= 1} onClick={go(pager.previous)}>
          <ChevronLeft />
          Previous
        </Button>
        <Button variant="outline" size="sm" disabled={!more} onClick={go(() => pager.next(nextCursor ?? "", count))}>
          Next
          <ChevronRight />
        </Button>
      </div>
    </nav>
  );
}
